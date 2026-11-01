#!/usr/bin/env bash
# Chart-only kind smoke. Builds local images, loads them into a pinned kind
# node, installs dev values, and checks the migration hook, factoryd, the
# worker, and the worker startup preflight. The cluster is deleted on every
# exit, including failure.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source-path=SCRIPTDIR
# shellcheck source=helm-lib.sh
source "${root}/scripts/ci/helm-lib.sh"
install_helm
install_kind
install_kubectl

CLUSTER_NAME="${FACTORY_KIND_CLUSTER:-factory-t87}"
NAMESPACE=factory
TASK_NAMESPACE=factory-runs
RELEASE=factory
chart="${root}/deploy/helm/factory"

cluster_up=0
pf_pid=""
watch_pid=""
kubeconfig=""
key_file=""

# shellcheck disable=SC2329 # invoked by the EXIT trap
cleanup() {
  local status=$?
  if [[ -n "${pf_pid}" ]]; then
    kill "${pf_pid}" >/dev/null 2>&1 || true
  fi
  if [[ -n "${watch_pid}" ]]; then
    kill "${watch_pid}" >/dev/null 2>&1 || true
    wait "${watch_pid}" >/dev/null 2>&1 || true
  fi
  if [[ -n "${key_file}" ]]; then
    rm -f "${key_file}"
  fi
  if [[ -n "${kubeconfig}" ]]; then
    rm -f "${kubeconfig}"
  fi
  if [[ "${cluster_up}" -eq 1 ]]; then
    echo "deleting kind cluster ${CLUSTER_NAME}"
    "${KIND}" delete cluster --name "${CLUSTER_NAME}" || true
  fi
  exit "${status}"
}
trap cleanup EXIT

values="${chart}/values.yaml"
for digest in \
  "${POSTGRES_IMAGE##*@}" \
  "${MINIO_IMAGE##*@}" \
  "${MINIO_CLIENT_IMAGE##*@}"; do
  if ! grep -q "${digest}" "${values}"; then
    echo "deploy/helm/factory/values.yaml is missing pinned digest ${digest}" >&2
    exit 1
  fi
done

echo "building factoryd"
docker build -t factoryd:dev -f "${root}/deploy/factoryd/Dockerfile" "${root}"
echo "building worker"
docker build -t factory-upstream-worker:dev "${root}/deploy/worker"

echo "pulling pinned third-party images"
docker pull "${KIND_NODE_IMAGE}"
docker pull "${POSTGRES_IMAGE}"
docker pull "${BUSYBOX_IMAGE}"
docker pull "${MINIO_IMAGE}"
docker pull "${MINIO_CLIENT_IMAGE}"
docker tag "${BUSYBOX_IMAGE}" busybox:1.36

echo "creating kind cluster ${CLUSTER_NAME}"
kind_config="$(mktemp)"
cat >"${kind_config}" <<EOF
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
networking:
  kubeProxyMode: nftables
nodes:
  - role: control-plane
    image: ${KIND_NODE_IMAGE}
EOF
"${KIND}" create cluster --name "${CLUSTER_NAME}" --config "${kind_config}"
rm -f "${kind_config}"
cluster_up=1

kubeconfig="$(mktemp)"
"${KIND}" get kubeconfig --name "${CLUSTER_NAME}" >"${kubeconfig}"
export KUBECONFIG="${kubeconfig}"

echo "loading images into kind"
"${KIND}" load docker-image factoryd:dev --name "${CLUSTER_NAME}"
"${KIND}" load docker-image factory-upstream-worker:dev --name "${CLUSTER_NAME}"
"${KIND}" load docker-image "${POSTGRES_IMAGE}" --name "${CLUSTER_NAME}"
"${KIND}" load docker-image busybox:1.36 --name "${CLUSTER_NAME}"
"${KIND}" load docker-image "${MINIO_IMAGE}" --name "${CLUSTER_NAME}"
"${KIND}" load docker-image "${MINIO_CLIENT_IMAGE}" --name "${CLUSTER_NAME}"

# kind's containerd import names parent layers import-<date>@sha256:<digest>.
# kubelet 1.34 looks those up as docker.io/library/import-... and refuses to
# start the container when that name is missing.
node="$("${KIND}" get nodes --name "${CLUSTER_NAME}")"
docker exec "${node}" sh -c '
  ctr -n k8s.io images ls -q | while read -r img; do
    case "$img" in
      import-*)
        ctr -n k8s.io images tag "$img" "docker.io/library/${img}" >/dev/null
        ;;
    esac
  done
'

"${KUBECTL}" create namespace "${TASK_NAMESPACE}"

watch_log="$(mktemp)"
# Each kubectl exits, so the JSON is flushed. A watch stream can stay
# buffered when stdout is not a terminal and miss a Job the worker deletes
# as soon as its Pod object exists.
(
  while true; do
    "${KUBECTL}" get jobs -n "${TASK_NAMESPACE}" -o json 2>/dev/null || true
    sleep 0.1
  done
) >"${watch_log}" &
watch_pid=$!

echo "helm install chart-only dev values"
"${HELM}" upgrade --install "${RELEASE}" "${chart}" \
  --namespace "${NAMESPACE}" \
  --create-namespace \
  -f "${chart}/values-dev.yaml" \
  --wait \
  --timeout 10m

echo "migration hook"
"${KUBECTL}" -n "${NAMESPACE}" get job "${RELEASE}-migrate" -o wide
"${KUBECTL}" -n "${NAMESPACE}" wait --for=condition=complete "job/${RELEASE}-migrate" --timeout=120s

echo "preflight hook"
"${KUBECTL}" -n "${NAMESPACE}" get job "${RELEASE}-worker-standard-preflight" -o wide
"${KUBECTL}" -n "${NAMESPACE}" wait --for=condition=complete "job/${RELEASE}-worker-standard-preflight" --timeout=120s

echo "factoryd"
"${KUBECTL}" -n "${NAMESPACE}" rollout status "deploy/${RELEASE}-factoryd" --timeout=180s
"${KUBECTL}" -n "${NAMESPACE}" get deploy "${RELEASE}-factoryd"

echo "worker deployment"
"${KUBECTL}" -n "${NAMESPACE}" rollout status "deploy/${RELEASE}-worker-standard" --timeout=180s
"${KUBECTL}" -n "${NAMESPACE}" get deploy "${RELEASE}-worker-standard"

echo "startup preflight in ${TASK_NAMESPACE}"
events_file="$(mktemp)"
"${KUBECTL}" -n "${TASK_NAMESPACE}" get events --sort-by=.lastTimestamp || true
"${KUBECTL}" -n "${TASK_NAMESPACE}" get events -o json >"${events_file}"
python3 - "${watch_log}" "${events_file}" <<'PY'
import json
import pathlib
import sys

seen = {}


def note(name, succeeded, failed, active, how):
    if not name.startswith("oz-preflight-"):
        return
    cur = seen.setdefault(name, {"succeeded": None, "failed": None, "active": None, "how": how})
    if succeeded is not None:
        cur["succeeded"] = succeeded
    if failed is not None:
        cur["failed"] = failed
    if active is not None:
        cur["active"] = active


raw = pathlib.Path(sys.argv[1]).read_text()
decoder = json.JSONDecoder()
idx = 0
while idx < len(raw):
    while idx < len(raw) and raw[idx].isspace():
        idx += 1
    if idx >= len(raw):
        break
    try:
        obj, end = decoder.raw_decode(raw, idx)
    except json.JSONDecodeError:
        break
    idx = end
    for item in obj.get("items", []):
        name = item.get("metadata", {}).get("name", "")
        status = item.get("status", {})
        note(name, status.get("succeeded"), status.get("failed"), status.get("active"), "job")

events = json.loads(pathlib.Path(sys.argv[2]).read_text() or '{"items":[]}')
for event in events.get("items", []):
    obj = event.get("involvedObject", {})
    name = obj.get("name", "")
    if obj.get("kind") == "Job":
        note(name, None, None, None, "event")
    elif obj.get("kind") == "Pod" and name.startswith("oz-preflight-"):
        job = name.rsplit("-", 1)[0]
        note(job, None, None, None, "pod")

if not seen:
    sys.exit("did not observe an oz-preflight Job in factory-runs")
for name, info in sorted(seen.items()):
    print(
        "oz-preflight job {name} in factory-runs succeeded={succeeded} failed={failed} active={active} via={how}".format(
            name=name, how=info["how"], **{k: info[k] for k in ("succeeded", "failed", "active")}
        )
    )
PY
rm -f "${events_file}"

echo "worker logs"
log_file="$(mktemp)"
for _ in $(seq 1 45); do
  "${KUBECTL}" -n "${NAMESPACE}" logs "deploy/${RELEASE}-worker-standard" >"${log_file}" || true
  if grep -q "Successfully connected to server" "${log_file}"; then
    break
  fi
  if grep -q "startup preflight failed" "${log_file}"; then
    break
  fi
  sleep 2
done
python3 - "${log_file}" <<'PY'
import pathlib
import sys

text = pathlib.Path(sys.argv[1]).read_text(errors="replace")
# Drop lines that look like a token assignment. The worker should not log one.
kept = []
for line in text.splitlines():
    lower = line.lower()
    if "api_key" in lower or "bearer " in lower or "warp_api_key" in lower:
        continue
    kept.append(line)
print("\n".join(kept))
if "Successfully connected to server" not in text:
    sys.exit("worker log has no successful connection")
if "startup preflight failed" in text:
    sys.exit("worker log reports startup preflight failed")
PY
rm -f "${log_file}"

start_port_forward() {
  if [[ -n "${pf_pid}" ]]; then
    kill "${pf_pid}" >/dev/null 2>&1 || true
    wait "${pf_pid}" >/dev/null 2>&1 || true
    pf_pid=""
  fi
  "${KUBECTL}" -n "${NAMESPACE}" port-forward "svc/${RELEASE}-factoryd" 18080:8080 >/tmp/factory-helm-pf.log 2>&1 &
  pf_pid=$!
  local ready=0
  for _ in $(seq 1 30); do
    if curl -fsS -o /dev/null "http://127.0.0.1:18080/healthz"; then
      ready=1
      break
    fi
    sleep 1
  done
  if [[ "${ready}" -ne 1 ]]; then
    echo "factoryd healthz was not reachable through the port-forward" >&2
    exit 1
  fi
}

expect_worker() {
  local label="$1"
  key_file="$(mktemp)"
  chmod 0600 "${key_file}"
  "${KUBECTL}" -n "${NAMESPACE}" get secret "${RELEASE}-api" -o jsonpath='{.data.FACTORY_API_KEY}' | base64 -d >"${key_file}"
  local body hdr code
  body="$(mktemp)"
  hdr="$(mktemp)"
  chmod 0600 "${hdr}"
  python3 - "${key_file}" "${hdr}" <<'PY'
import pathlib
import sys

key = pathlib.Path(sys.argv[1]).read_text()
pathlib.Path(sys.argv[2]).write_text("Authorization: Bearer " + key + "\n")
PY
  code="$(curl -sS -o "${body}" -w '%{http_code}' -H @"${hdr}" "http://127.0.0.1:18080/api/v1/agent/connected-self-hosted-workers")"
  rm -f "${hdr}" "${key_file}"
  key_file=""
  python3 - "${body}" "${label}" <<'PY'
import pathlib
import sys

text = pathlib.Path(sys.argv[1]).read_text()
print(text)
if "k8s-standard" not in text:
    sys.exit(sys.argv[2] + ": connected workers response does not list k8s-standard")
PY
  rm -f "${body}"
  if [[ "${code}" != "200" ]]; then
    echo "${label}: workers API status ${code}" >&2
    exit 1
  fi
}

echo "connected workers"
start_port_forward
expect_worker "install"

echo "helm upgrade"
"${HELM}" upgrade "${RELEASE}" "${chart}" \
  --namespace "${NAMESPACE}" \
  -f "${chart}/values-dev.yaml" \
  --wait \
  --timeout 10m

echo "upgrade migration hook"
"${KUBECTL}" -n "${NAMESPACE}" wait --for=condition=complete "job/${RELEASE}-migrate" --timeout=180s
"${KUBECTL}" -n "${NAMESPACE}" get job "${RELEASE}-migrate" -o wide

echo "upgrade factoryd"
"${KUBECTL}" -n "${NAMESPACE}" rollout status "deploy/${RELEASE}-factoryd" --timeout=180s

echo "upgrade worker"
"${KUBECTL}" -n "${NAMESPACE}" rollout status "deploy/${RELEASE}-worker-standard" --timeout=240s

echo "connected workers after upgrade"
start_port_forward
expect_worker "upgrade"

echo "helm-kind-smoke ok"
