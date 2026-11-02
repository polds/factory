#!/usr/bin/env bash
# M7 exit driver. MODE=offline|live|live-ci.
# Offline builds a pinned kind cluster, installs the chart in operator mode,
# and proves one pull request plus delete-to-cancel. The cluster is deleted
# on every exit. Live modes are not run from an agent; m7-live.yml is the
# dispatch. This script refuses --cleanup-remote when MODE=offline.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source-path=SCRIPTDIR
# shellcheck source=../lib/common.sh
source "$ROOT/spike/lib/common.sh"
# shellcheck source-path=SCRIPTDIR
# shellcheck source=../lib/driver.sh
source "$ROOT/spike/lib/driver.sh"
# shellcheck source-path=SCRIPTDIR
# shellcheck source=../../scripts/ci/helm-lib.sh
source "$ROOT/scripts/ci/helm-lib.sh"

MODE="${MODE:-offline}"
PHASE_PREFIX=m7
CLUSTER_NAME="${M7_KIND_CLUSTER:-factory-m7}"
NAMESPACE=factory
TASK_NAMESPACE=factory-runs
RELEASE=factory
PY_IMAGE="${M7_PY_IMAGE:-python:3.12-slim-bookworm@sha256:e697c81897302a4c4333bade000d98038fbb21eb064f8ee1fd42d02ebcd9d337}"
WORK="${M7_WORK:-$ROOT/spike/m7/.work}"
EVIDENCE="${M7_EVIDENCE:-$WORK/evidence}"
export PHASE_PREFIX WORK EVIDENCE MODE

cluster_up=0
kubeconfig=""
pf_pid=""

die() {
  printf 'M7 FAIL — %s\n' "$*" >&2
  exit 1
}

require_mode_secrets() {
  if [[ "$MODE" == "offline" ]]; then
    return 0
  fi
  if [[ "$MODE" != "live" && "$MODE" != "live-ci" ]]; then
    printf 'missing MODE\n' >&2
    return 1
  fi
  local missing=0 name
  for name in M1_OPENAI_BASE_URL M1_OPENAI_API_KEY M6_GITHUB_APP_ID; do
    if [[ -z "${!name:-}" ]]; then
      printf 'missing %s\n' "$name" >&2
      missing=1
    fi
  done
  if [[ -z "${M6_GITHUB_APP_PRIVATE_KEY:-}" && -z "${FACTORY_GITHUB_APP_PRIVATE_KEY_FILE:-}" ]]; then
    printf 'missing M6_GITHUB_APP_PRIVATE_KEY\n' >&2
    missing=1
  fi
  if [[ -z "${FACTORY_REVIEWERS_ADMINS:-}" ]]; then
    printf 'missing reviewer\n' >&2
    missing=1
  fi
  [[ "$missing" -eq 0 ]]
}

m7_run_remote() {
  export M7_WORK="$WORK"
  export M7_DEFINITION="${M7_DEFINITION:-$WORK/definition}"
  if [[ -z "${FACTORY_GITHUB_APP_ID:-}" && -n "${M6_GITHUB_APP_ID:-}" ]]; then
    export FACTORY_GITHUB_APP_ID="${M6_GITHUB_APP_ID}"
  fi
  if [[ -n "${M7_REMOTE_STUB:-}" ]]; then
    "$M7_REMOTE_STUB" "$@"
    return
  fi
  SPIKE_ENV_PREFIX=M7 SPIKE_RUN_REF_PREFIX=m7-run \
    python3 "$ROOT/spike/lib/remote.py" "$@"
}

# Live and live-ci preflight, then seed m7-run/<run-id>/main, before the
# definition is applied. Offline returns without touching the network.
m7_prepare_remote() {
  local reason=""
  if [[ "$MODE" == "offline" ]]; then
    return 0
  fi
  if ! m7_run_remote preflight; then
    reason="remote preflight failed"
    if [[ -f "$WORK/remote-fail" ]]; then
      IFS= read -r reason <"$WORK/remote-fail" || true
    fi
    die "$reason"
  fi
  if ! m7_run_remote seed; then
    reason="remote seed failed"
    if [[ -f "$WORK/remote-fail" ]]; then
      IFS= read -r reason <"$WORK/remote-fail" || true
    fi
    die "$reason"
  fi
}

# Preflight and seed run before convert and apply. Live seed rewrites the
# copied definition onto m7-run/<run-id>/main and leaves it in remote-seed.
m7_apply_definition() {
  local def="$1"
  local manifest
  m7_prepare_remote
  if [[ -d "$WORK/remote-seed" ]]; then
    def="$WORK/remote-seed"
  fi
  m7_stamp_reviewer "$def"
  (cd "$ROOT" && go build -o "$WORK/factoryctl" ./cmd/factoryctl)
  "$WORK/factoryctl" convert "$def" >"$WORK/manifests.yaml"
  rm -rf "$WORK/apply"
  python3 "$ROOT/spike/m7/apply_order.py" "$WORK/apply" <"$WORK/manifests.yaml"
  for manifest in "$WORK"/apply/*.yaml; do
    echo "apply $(basename "$manifest")"
    k apply -n "${NAMESPACE}" -f "$manifest"
  done
}

m7_cleanup_remote() {
  if [[ "${MODE:-}" == "offline" ]]; then
    return 0
  fi
  if ! m7_run_remote cleanup; then
    printf 'remote cleanup incomplete; ledger %s\n' "$WORK/remote-ledger.json" >&2
  fi
  m7_cleanup_summary
}

m7_cleanup_summary() {
  local ledger="${M7_LEDGER:-$WORK/remote-ledger.json}"
  SPIKE_ENV_PREFIX=M7 python3 - "$ledger" <<'PY'
import json
import sys
from pathlib import Path

path = Path(sys.argv[1])
deleted = {"ref": 0, "pr": 0}
left = {"ref": 0, "pr": 0}
if path.is_file():
    doc = json.loads(path.read_text(encoding="utf-8"))
    for entry in doc.get("entries") or []:
        if not isinstance(entry, dict):
            continue
        kind = entry.get("kind")
        if kind not in ("ref", "pr"):
            continue
        bucket = deleted if entry.get("phase") == "cleaned" else left
        bucket[kind] += 1
# Ledger rows are ref or pr. A ref is also the branch that was pushed.
print(
    "m7 cleanup: refs deleted={rd} left={rl} branches deleted={rd} left={rl} prs deleted={pd} left={pl}".format(
        rd=deleted["ref"],
        rl=left["ref"],
        pd=deleted["pr"],
        pl=left["pr"],
    )
)
PY
}

m7_budgeted_remote_cleanup() {
  local start="" end="" elapsed="" cap="" g=""
  if [[ "${MODE:-}" == "offline" ]]; then
    return 0
  fi
  start="$(python3 -c 'import time; print("%.3f" % time.time())')"
  M7_CLEANUP_DEADLINE="$(python3 -c 'import time; print("%.3f" % (time.time() + 5))')"
  export M7_CLEANUP_DEADLINE
  if ! m7_run_remote cleanup; then
    printf 'remote cleanup incomplete; ledger %s\n' "$WORK/remote-ledger.json" >&2
  fi
  unset M7_CLEANUP_DEADLINE
  end="$(python3 -c 'import time; print("%.3f" % time.time())')"
  elapsed="$(python3 -c 'import sys; print("%.3f" % (float(sys.argv[1]) - float(sys.argv[2])))' "$end" "$start")"
  cap="${SPIKE_STOP_GRACE:-5}"
  g="$(python3 -c 'import math,sys; e=float(sys.argv[1]); cap=int(float(sys.argv[2])); g=math.floor((6.2-e)/2); print(max(0, min(g, cap)))' "$elapsed" "$cap")"
  SPIKE_STOP_GRACE="$g"
  export SPIKE_STOP_GRACE
  if [[ -n "${M7_TRACE_CANCEL:-}" ]]; then
    printf 'cancel-window e=%s G=%s\n' "$elapsed" "$g"
  fi
  m7_cleanup_summary
}

# shellcheck disable=SC2317,SC2329 # invoked by the EXIT trap and by tests
cleanup() {
  local status=$?
  local why="${1:-EXIT}"
  if [[ "$why" == "INT" || "$why" == "TERM" ]]; then
    m7_budgeted_remote_cleanup || true
  else
    m7_cleanup_remote || true
  fi
  if [[ -n "${pf_pid}" ]]; then
    kill "${pf_pid}" >/dev/null 2>&1 || true
    wait "${pf_pid}" >/dev/null 2>&1 || true
    pf_pid=""
  fi
  if [[ "${cluster_up}" -eq 1 ]]; then
    echo "deleting kind cluster ${CLUSTER_NAME}"
    "${KIND}" delete cluster --name "${CLUSTER_NAME}" || true
    cluster_up=0
  fi
  if [[ -n "${kubeconfig}" ]]; then
    rm -f "${kubeconfig}"
    kubeconfig=""
  fi
  if [[ "$why" == "EXIT" ]]; then
    exit "${status}"
  fi
}

k() {
  "${KUBECTL}" --kubeconfig "${kubeconfig}" "$@"
}

phase() {
  phase_report "$1" "$2"
}

write_key_file() {
  local dest="$1"
  local value="$2"
  umask 077
  printf '%s' "$value" >"$dest"
  chmod 600 "$dest"
}

# shellcheck disable=SC2317,SC2329
collect_evidence() {
  mkdir -p "$EVIDENCE"
  k get agentruns,factories,runners,factoryagents -n "$NAMESPACE" -o yaml >"$EVIDENCE/crs.yaml" 2>"$EVIDENCE/crs.err" || true
  k get events -n "$NAMESPACE" --sort-by=.lastTimestamp >"$EVIDENCE/events.txt" 2>&1 || true
  k get pods,jobs -n "$NAMESPACE" -o wide >"$EVIDENCE/pods-factory.txt" 2>&1 || true
  k get pods,jobs -n "$TASK_NAMESPACE" -o wide >"$EVIDENCE/pods-runs.txt" 2>&1 || true
  k logs -n "$NAMESPACE" deploy/factory-factoryd --tail=400 >"$EVIDENCE/factoryd.log" 2>&1 || true
  k logs -n "$NAMESPACE" deploy/factory-operator --tail=400 >"$EVIDENCE/operator.log" 2>&1 || true
  k logs -n "$NAMESPACE" deploy/factory-worker-standard --tail=400 >"$EVIDENCE/worker.log" 2>&1 || true
}

scan_evidence() {
  local extra=""
  if [[ -f "$WORK/gh-token" ]]; then
    extra="M7_GH_TOKEN"
    M7_GH_TOKEN="$(tr -d '[:space:]' <"$WORK/gh-token")"
    export M7_GH_TOKEN
  fi
  if [[ -f "$WORK/api-key" ]]; then
    extra="${extra} M7_API_KEY"
    M7_API_KEY="$(tr -d '[:space:]' <"$WORK/api-key")"
    export M7_API_KEY
  fi
  # shellcheck disable=SC2086
  EVIDENCE_EXTRA_SECRETS="${extra}" python3 "$ROOT/spike/lib/redact_evidence.py" \
    --key-file "$WORK/api-key" \
    "$EVIDENCE"
}

# m7_stamp_reviewer writes the configured reviewer login into the implementer
# instructions. An empty login leaves the placeholder. A login that is not a
# GitHub handle fails closed and does not print the value.
m7_stamp_reviewer() {
  local def="$1" agent login
  agent="$def/agents/implementer/agent.md"
  if [[ ! -f "$agent" ]]; then
    return 0
  fi
  login="${FACTORY_REVIEWERS_ADMINS:-}"
  login="${login%%,*}"
  login="${login#"${login%%[![:space:]]*}"}"
  login="${login%"${login##*[![:space:]]}"}"
  if [[ -z "$login" ]]; then
    return 0
  fi
  if [[ ! "$login" =~ ^[A-Za-z0-9][A-Za-z0-9-]{0,38}$ ]]; then
    printf 'reviewer was not accepted\n' >&2
    return 1
  fi
  python3 - "$agent" "$login" <<'PY'
import pathlib
import sys

path, login = pathlib.Path(sys.argv[1]), sys.argv[2]
text = path.read_text(encoding="utf-8")
if "{{M7_REVIEWER}}" not in text:
    sys.stderr.write("implementer steps have no reviewer placeholder\n")
    raise SystemExit(1)
path.write_text(text.replace("{{M7_REVIEWER}}", login), encoding="utf-8")
PY
}

if [[ "${BASH_SOURCE[0]}" != "$0" ]]; then
  return 0
fi

if [[ "${1:-}" == "--cleanup-remote" ]]; then
  if [[ "$MODE" == "offline" ]]; then
    printf 'cleanup-remote refused in offline\n' >&2
    exit 1
  fi
  if ! require_mode_secrets; then
    exit 1
  fi
  m7_budgeted_remote_cleanup
  exit 0
fi

if ! require_mode_secrets; then
  exit 1
fi

mkdir -p "$WORK" "$EVIDENCE"
chmod 700 "$WORK"
trap 'cleanup EXIT' EXIT
trap 'cleanup INT' INT
trap 'cleanup TERM' TERM

if ! docker info >/dev/null 2>&1; then
  sudo -n service docker start
fi

install_helm
install_kind
install_kubectl
detect_platform

step_start=$SECONDS
echo "building images"
docker build --platform "$OCI_PLATFORM" -t factoryd:dev -f "$ROOT/deploy/factoryd/Dockerfile" "$ROOT"
docker build --platform "$OCI_PLATFORM" -t factory-operator:dev -f "$ROOT/deploy/operator/Dockerfile" "$ROOT"
docker build --platform "$OCI_PLATFORM" -t factory-upstream-worker:dev "$ROOT/deploy/worker"
(
  cd "$ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH="$GOARCH_TARGET" \
    go build -o harness/generic-openai/factory-harness ./cmd/factory-harness
)
docker build --platform "$OCI_PLATFORM" -t factory-harness:dev "$ROOT/harness/generic-openai"
docker build --platform "$OCI_PLATFORM" -t factory-m1-task:dev "$ROOT/spike/m1/task-image"
docker build --platform "$OCI_PLATFORM" -t factory-m7-git:dev -f "$ROOT/spike/m7/git.Dockerfile" "$ROOT/spike/m7"
docker pull "${KIND_NODE_IMAGE}"
docker pull "${POSTGRES_IMAGE}"
docker pull "${BUSYBOX_IMAGE}"
docker pull "${MINIO_IMAGE}"
docker pull "${MINIO_CLIENT_IMAGE}"
docker pull "${PY_IMAGE}"
docker tag "${BUSYBOX_IMAGE}" busybox:1.36
phase images "$step_start"

api_key="$(random_hex)"
worker_key="$(random_hex)"
gh_token="$(random_hex)"
write_key_file "$WORK/api-key" "$api_key"
write_key_file "$WORK/worker-key" "$worker_key"
write_key_file "$WORK/gh-token" "$gh_token"
umask 077
cat >"$WORK/values-secrets.yaml" <<EOF
controlPlane:
  api:
    apiKey: "${api_key}"
    workerKey: "${worker_key}"
EOF
chmod 600 "$WORK/values-secrets.yaml"
unset api_key worker_key

step_start=$SECONDS
echo "creating kind cluster ${CLUSTER_NAME}"
kind_config="$(mktemp)"
kind_cluster_config >"${kind_config}"
"${KIND}" create cluster --name "${CLUSTER_NAME}" --config "${kind_config}"
rm -f "${kind_config}"
cluster_up=1
kubeconfig="$(mktemp)"
"${KIND}" get kubeconfig --name "${CLUSTER_NAME}" >"${kubeconfig}"
export KUBECONFIG="${kubeconfig}"

echo "loading images"
for image in factoryd:dev factory-operator:dev factory-upstream-worker:dev factory-harness:dev factory-m1-task:dev factory-m7-git:dev busybox:1.36 "${PY_IMAGE}" "${POSTGRES_IMAGE}" "${MINIO_IMAGE}" "${MINIO_CLIENT_IMAGE}"; do
  "${KIND}" load docker-image "${image}" --name "${CLUSTER_NAME}"
done
node="$("${KIND}" get nodes --name "${CLUSTER_NAME}")"
retag_kind_import_layers "${node}"
k create namespace "${TASK_NAMESPACE}" --dry-run=client -o yaml | k apply -f -
phase kind "$step_start"

step_start=$SECONDS
k create namespace "${NAMESPACE}" --dry-run=client -o yaml | k apply -f -
if [[ "$MODE" == "offline" ]]; then
  write_key_file "$WORK/definition-git-url" "git://m7-git.factory.svc.cluster.local/target.git"
  write_key_file "$WORK/github-api-base" "http://m7-forge.factory.svc.cluster.local:8080"
else
  write_key_file "$WORK/definition-git-url" "${M7_DEFINITION_GIT_URL}"
  write_key_file "$WORK/github-api-base" "${M7_GITHUB_API_BASE}"
  write_key_file "$WORK/openai-key" "${M1_OPENAI_API_KEY}"
  write_key_file "$WORK/openai-base" "${M1_OPENAI_BASE_URL}"
  write_key_file "$WORK/app-id" "${M6_GITHUB_APP_ID}"
  write_key_file "$WORK/gh-token" "app-mint"
fi
k -n "${NAMESPACE}" create secret generic m7-runtime \
  --from-file=FACTORY_INFERENCE_OPENAI_API_KEY="$WORK/worker-key" \
  --from-file=FACTORY_SECRET_GH_TOKEN="$WORK/gh-token" \
  --from-file=FACTORY_SECRET_DEFINITION_GIT_URL="$WORK/definition-git-url" \
  --from-file=FACTORY_SECRET_GITHUB_API_BASE="$WORK/github-api-base" \
  --dry-run=client -o yaml | k apply -f -
if [[ "$MODE" != "offline" ]]; then
  k -n "${NAMESPACE}" create secret generic m7-runtime \
    --from-file=FACTORY_INFERENCE_OPENAI_API_KEY="$WORK/openai-key" \
    --from-file=FACTORY_INFERENCE_OPENAI_COMPATIBLE_BASE_URL="$WORK/openai-base" \
    --from-file=FACTORY_GITHUB_APP_ID="$WORK/app-id" \
    --from-file=FACTORY_SECRET_GH_TOKEN="$WORK/gh-token" \
    --from-file=FACTORY_SECRET_DEFINITION_GIT_URL="$WORK/definition-git-url" \
    --from-file=FACTORY_SECRET_GITHUB_API_BASE="$WORK/github-api-base" \
    --dry-run=client -o yaml | k apply -f -
  k -n "${NAMESPACE}" create secret generic m7-github-app \
    --from-file=key.pem="${FACTORY_GITHUB_APP_PRIVATE_KEY_FILE}" \
    --dry-run=client -o yaml | k apply -f -
fi

echo "helm install"
values_file="$ROOT/spike/m7/values-offline.yaml"
if [[ "$MODE" != "offline" ]]; then
  values_file="$ROOT/spike/m7/values-live.yaml"
fi
"${HELM}" upgrade --install "${RELEASE}" "$ROOT/deploy/helm/factory" \
  --namespace "${NAMESPACE}" \
  --create-namespace \
  -f "${values_file}" \
  -f "$WORK/values-secrets.yaml" \
  --set operator.enabled=true \
  --set operator.installCRDs=true \
  --wait \
  --timeout 10m
k -n "${NAMESPACE}" wait --for=condition=complete "job/${RELEASE}-migrate" --timeout=180s
k -n "${NAMESPACE}" wait --for=condition=complete "job/${RELEASE}-worker-standard-preflight" --timeout=180s
k -n "${NAMESPACE}" rollout status deploy/factory-factoryd --timeout=180s
k -n "${NAMESPACE}" rollout status deploy/factory-operator --timeout=180s
k -n "${NAMESPACE}" rollout status deploy/factory-worker-standard --timeout=180s
echo "webhook endpoint"
ready=""
for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20 21 22 23 24 25 26 27 28 29 30; do
  ready="$(k -n "${NAMESPACE}" get endpoints factory-operator-webhook -o jsonpath='{.subsets[0].addresses[0].ip}' 2>/dev/null || true)"
  if [[ -n "${ready}" ]]; then
    break
  fi
  sleep 2
done
if [[ -z "${ready}" ]]; then
  die "webhook endpoint not Ready"
fi
phase helm "$step_start"

if [[ "$MODE" == "offline" ]]; then
  step_start=$SECONDS
  k -n "${NAMESPACE}" create configmap m7-spike \
    --from-file=router.py="$ROOT/spike/lib/router.py" \
    --from-file=forge.py="$ROOT/spike/lib/forge.py" \
    --dry-run=client -o yaml | k apply -f -
  k -n "${NAMESPACE}" create secret generic m7-forge-token \
    --from-file=token="$WORK/gh-token" \
    --dry-run=client -o yaml | k apply -f -
  k apply -f - <<EOF
apiVersion: v1
kind: Service
metadata:
  name: m7-router
  namespace: ${NAMESPACE}
spec:
  selector:
    app: m7-router
  ports:
    - name: http
      port: 8080
      targetPort: http
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: m7-router
  namespace: ${NAMESPACE}
spec:
  replicas: 1
  selector:
    matchLabels:
      app: m7-router
  template:
    metadata:
      labels:
        app: m7-router
    spec:
      containers:
        - name: router
          image: ${PY_IMAGE}
          imagePullPolicy: IfNotPresent
          args: ["python3", "/opt/spike/router.py", "--addr", "0.0.0.0:8080"]
          env:
            - name: M7_DEFINITION_GIT_URL
              value: git://m7-git.factory.svc.cluster.local/target.git
            - name: M7_GITHUB_API_BASE
              value: http://m7-forge.factory.svc.cluster.local:8080
          ports:
            - name: http
              containerPort: 8080
          volumeMounts:
            - name: spike
              mountPath: /opt/spike
              readOnly: true
      volumes:
        - name: spike
          configMap:
            name: m7-spike
---
apiVersion: v1
kind: Service
metadata:
  name: m7-forge
  namespace: ${NAMESPACE}
spec:
  selector:
    app: m7-forge
  ports:
    - name: http
      port: 8080
      targetPort: http
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: m7-forge
  namespace: ${NAMESPACE}
spec:
  replicas: 1
  selector:
    matchLabels:
      app: m7-forge
  template:
    metadata:
      labels:
        app: m7-forge
    spec:
      containers:
        - name: forge
          image: ${PY_IMAGE}
          imagePullPolicy: IfNotPresent
          args: ["python3", "/opt/spike/forge.py", "--addr", "0.0.0.0:8080"]
          env:
            - name: SPIKE_ENV_PREFIX
              value: M7
            - name: SPIKE_FORGE_STANDALONE
              value: "1"
            - name: SPIKE_PR_HTML_BASE
              value: https://pr.invalid/m7/factory-m7-target/pull
            - name: M7_TOKEN_FILE
              value: /var/run/token/token
          ports:
            - name: http
              containerPort: 8080
          volumeMounts:
            - name: spike
              mountPath: /opt/spike
              readOnly: true
            - name: token
              mountPath: /var/run/token
              readOnly: true
      volumes:
        - name: spike
          configMap:
            name: m7-spike
        - name: token
          secret:
            secretName: m7-forge-token
---
apiVersion: v1
kind: Service
metadata:
  name: m7-git
  namespace: ${NAMESPACE}
spec:
  selector:
    app: m7-git
  ports:
    - name: git
      port: 9418
      targetPort: git
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: m7-git
  namespace: ${NAMESPACE}
spec:
  replicas: 1
  selector:
    matchLabels:
      app: m7-git
  template:
    metadata:
      labels:
        app: m7-git
    spec:
      initContainers:
        - name: seed
          image: factory-m7-git:dev
          imagePullPolicy: IfNotPresent
          command: ["bash", "-c"]
          args:
            - |
              set -euo pipefail
              git config --global init.defaultBranch main
              git config --global user.email seed@example.invalid
              git config --global user.name seed
              if [[ ! -f /srv/git/target.git/HEAD ]]; then
                git init --bare /srv/git/target.git
                git init /tmp/seed
                printf '%s\n' seed > /tmp/seed/README.md
                git -C /tmp/seed add README.md
                git -C /tmp/seed commit -m seed
                git -C /tmp/seed remote add origin /srv/git/target.git
                git -C /tmp/seed push origin HEAD:main
              fi
              git -C /srv/git/target.git config daemon.receivepack true
          volumeMounts:
            - name: repos
              mountPath: /srv/git
      containers:
        - name: git
          image: factory-m7-git:dev
          imagePullPolicy: IfNotPresent
          command: ["git"]
          args:
            - daemon
            - --reuseaddr
            - --base-path=/srv/git
            - --export-all
            - --enable=receive-pack
            - --listen=0.0.0.0
            - --port=9418
            - /srv/git
          ports:
            - name: git
              containerPort: 9418
          volumeMounts:
            - name: repos
              mountPath: /srv/git
      volumes:
        - name: repos
          emptyDir: {}
EOF
  k -n "${NAMESPACE}" rollout status deploy/m7-router --timeout=180s
  k -n "${NAMESPACE}" rollout status deploy/m7-forge --timeout=180s
  k -n "${NAMESPACE}" rollout status deploy/m7-git --timeout=180s
  phase backends "$step_start"
fi

step_start=$SECONDS
def="$WORK/definition"
rm -rf "$def"
cp -a "$ROOT/deploy/m7-factory" "$def"
if [[ "$MODE" != "offline" ]]; then
  model="${M7_LIVE_MODEL:-nous/claude-sonnet-5.5}"
  python3 - "$def" "$model" <<'PY'
import pathlib, sys
root, model = pathlib.Path(sys.argv[1]), sys.argv[2]
for path in root.rglob("*"):
    if path.suffix not in {".yaml", ".md"} or not path.is_file():
        continue
    path.write_text(path.read_text(encoding="utf-8").replace("m7-agent", model), encoding="utf-8")
PY
fi
m7_apply_definition "$def"
echo "Factory DefinitionValid"
ok=""
for _ in $(seq 1 60); do
  valid="$(k -n "${NAMESPACE}" get factory m7 -o jsonpath='{.status.conditions[?(@.type=="DefinitionValid")].status}' 2>/dev/null || true)"
  gen="$(k -n "${NAMESPACE}" get factory m7 -o jsonpath='{.metadata.generation}' 2>/dev/null || true)"
  obs="$(k -n "${NAMESPACE}" get factory m7 -o jsonpath='{.status.observedGeneration}' 2>/dev/null || true)"
  if [[ "$valid" == "True" && -n "$gen" && "$gen" == "$obs" ]]; then
    ok=1
    break
  fi
  sleep 2
done
if [[ -z "$ok" ]]; then
  k -n "${NAMESPACE}" describe factory m7 >"$EVIDENCE/factory-describe.txt" || true
  die "Factory DefinitionValid was not True"
fi
phase definition "$step_start"

step_start=$SECONDS
k apply -f "$ROOT/spike/m7/agentrun-pr.yaml"
echo "waiting for m7-pr"
pr_phase=""
run_id=""
for _ in $(seq 1 90); do
  pr_phase="$(k -n "${NAMESPACE}" get agentrun m7-pr -o jsonpath='{.status.phase}' 2>/dev/null || true)"
  run_id="$(k -n "${NAMESPACE}" get agentrun m7-pr -o jsonpath='{.status.runId}' 2>/dev/null || true)"
  if [[ "$pr_phase" == "succeeded" || "$pr_phase" == "failed" || "$pr_phase" == "cancelled" ]]; then
    break
  fi
  sleep 2
done
if [[ "$pr_phase" != "succeeded" || -z "$run_id" ]]; then
  collect_evidence || true
  die "m7-pr phase=${pr_phase:-empty} runId=${run_id:-empty}"
fi
pr_urls="$(k -n "${NAMESPACE}" get agentrun m7-pr -o jsonpath='{.status.prUrls}' 2>/dev/null || true)"
html=""
if [[ "$MODE" == "offline" ]]; then
  printf '%s\n' '{}' >"$WORK/forge-snapshot.json"
  k -n "${NAMESPACE}" port-forward svc/m7-forge 18080:8080 >/tmp/m7-forge-pf.log 2>&1 &
  pf_pid=$!
  for _ in $(seq 1 30); do
    if curl -fsS --connect-timeout 1 -m 3 -o "$WORK/forge-snapshot.json" http://127.0.0.1:18080/_spike/m6; then
      break
    fi
    sleep 1
  done
  kill "${pf_pid}" >/dev/null 2>&1 || true
  wait "${pf_pid}" >/dev/null 2>&1 || true
  pf_pid=""
  html="$(python3 -c 'import json,sys; doc=json.load(open(sys.argv[1], encoding="utf-8")); pulls=doc.get("pulls") or []; print(pulls[0]["html_url"] if pulls else "")' "$WORK/forge-snapshot.json")"
else
  if ! m7_run_remote record-prs; then
    collect_evidence || true
    die "remote pull request was not recorded"
  fi
  html="$(tr -d '[:space:]' <"$WORK/opened-pr-url")"
fi
if [[ -z "$html" ]]; then
  collect_evidence || true
  die "forge has no pull request"
fi
printf 'm7 pr assertion: forge=%s runId=%s phase=%s status.prUrls=%s\n' \
  "$html" "$run_id" "$pr_phase" "${pr_urls:-[]}"
pr_first="$(k -n "${NAMESPACE}" get agentrun m7-pr -o jsonpath='{.status.prUrls[0]}' 2>/dev/null || true)"
if [[ -z "$pr_first" ]]; then
  collect_evidence || true
  die "status.prUrls is empty"
fi
if [[ "$pr_first" != "$html" ]]; then
  collect_evidence || true
  die "status.prUrls does not match the forge pull request"
fi
phase pr "$step_start"

step_start=$SECONDS
for _ in $(seq 1 30); do
  running="$(k get pods -n "${TASK_NAMESPACE}" --field-selector=status.phase=Running --no-headers 2>/dev/null || true)"
  if [[ -z "$running" ]]; then
    break
  fi
  sleep 2
done
k apply -f "$ROOT/spike/m7/agentrun-hold.yaml"
echo "waiting for hold pod"
for _ in $(seq 1 30); do
  hold_id="$(k -n "${NAMESPACE}" get agentrun m7-hold -o jsonpath='{.status.runId}' 2>/dev/null || true)"
  if [[ -n "$hold_id" ]]; then
    break
  fi
  sleep 2
done
hold_pod=""
for _ in $(seq 1 60); do
  hold_pod="$(k get pods -n "${TASK_NAMESPACE}" --field-selector=status.phase=Running -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)"
  if [[ -n "$hold_pod" ]]; then
    break
  fi
  sleep 2
done
if [[ -z "$hold_pod" ]]; then
  collect_evidence || true
  die "hold pod was not Running"
fi
hold_run="$(k -n "${NAMESPACE}" get agentrun m7-hold -o jsonpath='{.status.runId}' 2>/dev/null || true)"
cancel_start="$(date +%s)"
k delete agentrun m7-hold -n "${NAMESPACE}" --wait=false
gone=""
for _ in $(seq 1 30); do
  if ! k -n "${NAMESPACE}" get agentrun m7-hold >/dev/null 2>&1; then
    gone=1
    break
  fi
  hold_phase="$(k -n "${NAMESPACE}" get agentrun m7-hold -o jsonpath='{.status.phase}' 2>/dev/null || true)"
  hold_conds="$(k -n "${NAMESPACE}" get agentrun m7-hold -o jsonpath='{range .status.conditions[*]}{.type}={.status} {end}' 2>/dev/null || true)"
  printf 'm7 hold still phase=%s conds=%s\n' "${hold_phase:-empty}" "${hold_conds:-none}"
  if [[ "$hold_conds" == *CancelTimedOut=True* ]]; then
    collect_evidence || true
    die "CancelTimedOut on m7-hold"
  fi
  sleep 5
done
cancel_end="$(date +%s)"
cancel_s="$((cancel_end - cancel_start))"
if [[ -z "$gone" ]]; then
  collect_evidence || true
  k -n "${NAMESPACE}" get agentrun m7-hold -o yaml >"$EVIDENCE/hold-agentrun.yaml" 2>/dev/null || true
  die "agentrun m7-hold still present after delete"
fi
active=1
for _ in $(seq 1 30); do
  active="$(k get pods -n "${TASK_NAMESPACE}" --field-selector=status.phase=Running --no-headers 2>/dev/null | wc -l | tr -d ' ')"
  jobs="$(k get jobs -n "${TASK_NAMESPACE}" -o jsonpath='{range .items[*]}{.status.active}{"\n"}{end}' 2>/dev/null || true)"
  if [[ "$active" == "0" && ! "$jobs" =~ [1-9] ]]; then
    active=0
    break
  fi
  sleep 2
done
if [[ "$active" != "0" ]]; then
  collect_evidence || true
  die "task pod or job still present after cancel"
fi
sleep 5
later="$(k get pods -n "${TASK_NAMESPACE}" --field-selector=status.phase=Running --no-headers 2>/dev/null || true)"
if [[ -n "$later" ]]; then
  die "new claim after cancel"
fi
printf 'm7 delete-to-cancel %ss\n' "$cancel_s"
if [[ "$cancel_s" -gt 120 ]]; then
  collect_evidence || true
  die "delete-to-cancel ${cancel_s}s exceeds 120s"
fi
if [[ -n "$hold_run" ]]; then
  k -n "${NAMESPACE}" port-forward svc/factory-factoryd 18081:8080 >/tmp/m7-factoryd-pf.log 2>&1 &
  pf_pid=$!
  python3 - "$WORK/api-key" "$WORK/curl.cfg" "http://127.0.0.1:18081/api/v1/agent/runs/${hold_run}" <<'PY'
import pathlib, sys
key = pathlib.Path(sys.argv[1]).read_text(encoding="utf-8").strip()
dest = pathlib.Path(sys.argv[2])
dest.write_text('header = "Authorization: Bearer %s"\n' % key, encoding="utf-8")
dest.chmod(0o600)
PY
  got=""
  for _ in $(seq 1 20); do
    if curl -fsS --connect-timeout 1 -m 3 -K "$WORK/curl.cfg" -o "$EVIDENCE/hold-run.json" \
      "http://127.0.0.1:18081/api/v1/agent/runs/${hold_run}"; then
      got=1
      break
    fi
    sleep 1
  done
  kill "${pf_pid}" >/dev/null 2>&1 || true
  wait "${pf_pid}" >/dev/null 2>&1 || true
  pf_pid=""
  if [[ -z "$got" ]]; then
    die "factoryd run detail was not readable"
  fi
  python3 - "$EVIDENCE/hold-run.json" <<'PY'
import json, sys
doc = json.load(open(sys.argv[1], encoding="utf-8"))
status = str(doc.get("status") or doc.get("state") or "")
if status not in {"cancelled", "canceled"}:
    sys.stderr.write("factoryd run status is %s\n" % (status or "empty"))
    raise SystemExit(1)
PY
fi
phase cancel "$step_start"

collect_evidence
scan_evidence
mode_name="$(printf '%s' "$MODE" | tr '[:lower:]' '[:upper:]')"
# live-ci prints LIVE PASS; the workflow name is live-ci but the line is M7 LIVE.
if [[ "$MODE" == "live-ci" ]]; then
  mode_name="LIVE"
fi
printf 'M7 %s PASS wall %ss\n' "$mode_name" "$SECONDS"
