#!/usr/bin/env bash
# Render testdata/helm-worker.yaml from testdata/values.yaml with the pinned
# helm in scripts/ci/helm-lib.sh. --check re-renders and diffs the committed file.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(cd "${here}/../../../.." && pwd)"
# shellcheck source-path=SCRIPTDIR
# shellcheck source=../../../../scripts/ci/helm-lib.sh
source "${root}/scripts/ci/helm-lib.sh"
install_helm

full="$(mktemp)"
filtered="$(mktemp)"
dev_render="$(mktemp)"
dev_secret="$(mktemp)"
nil_render="$(mktemp)"
# shellcheck disable=SC2329 # invoked by the EXIT trap
cleanup() {
  rm -f "${full}" "${filtered}" "${dev_render}" "${dev_secret}" "${nil_render}"
}
trap cleanup EXIT

"${HELM}" template factory "${root}/deploy/helm/factory" \
  --namespace factory \
  -f "${here}/values.yaml" >"${full}"

python3 - "${full}" "${filtered}" <<'PY'
import pathlib
import sys

src = pathlib.Path(sys.argv[1]).read_text()
parts = src.split("\n---\n")
keep_names = {"factory-worker-standard", "factory-worker-standard-config"}
keep_kinds = {"ServiceAccount", "ConfigMap", "Role", "RoleBinding", "Deployment"}
kept = []
for part in parts:
    body = part.strip()
    if body.startswith("---\n"):
        body = body[4:].strip()
    if not body or body == "---":
        continue
    kind = ""
    name = ""
    for line in body.splitlines():
        if line.startswith("kind:"):
            kind = line.split(":", 1)[1].strip()
        elif line.startswith("  name:") and name == "":
            name = line.split(":", 1)[1].strip()
    if kind in keep_kinds and name in keep_names:
        kept.append(body if body.endswith("\n") else body + "\n")
if len(kept) != 5:
    sys.exit(f"expected 5 worker objects, got {len(kept)}")
text = "---\n" + "---\n".join(kept)
if not text.endswith("\n"):
    text += "\n"
pathlib.Path(sys.argv[2]).write_text(text)
PY

out="${here}/helm-worker.yaml"
if [[ "${1:-}" == "--check" ]]; then
  diff -u "${out}" "${filtered}"
else
  cp "${filtered}" "${out}"
  echo "wrote ${out}"
fi

# values-dev creates the shared API secret (controlPlane.api.create). The
# operator env must use the same helpers as the chart worker.
"${HELM}" template factory "${root}/deploy/helm/factory" \
  --namespace factory \
  -f "${root}/deploy/helm/factory/values-dev.yaml" \
  --set operator.enabled=true >"${dev_render}"
python3 - "${dev_render}" "${dev_secret}" <<'PY'
import pathlib
import sys

text = pathlib.Path(sys.argv[1]).read_text()
want = ("WORKER_API_KEY_SECRET_NAME", "WORKER_API_KEY_SECRET_KEY")
found = {}
lines = text.splitlines()
for i, line in enumerate(lines):
    stripped = line.strip()
    if not stripped.startswith("- name:"):
        continue
    name = stripped.split(":", 1)[1].strip()
    if name not in want or i + 1 >= len(lines):
        continue
    value = lines[i + 1].strip()
    if value.startswith("value:"):
        found[name] = value.split(":", 1)[1].strip().strip('"')
missing = [name for name in want if name not in found]
if missing:
    sys.exit("operator env missing " + ", ".join(missing))
pathlib.Path(sys.argv[2]).write_text(
    "WORKER_API_KEY_SECRET_NAME=" + found["WORKER_API_KEY_SECRET_NAME"] + "\n"
    "WORKER_API_KEY_SECRET_KEY=" + found["WORKER_API_KEY_SECRET_KEY"] + "\n"
)
PY
secret_golden="${here}/operator-dev-api-secret.txt"
if [[ "${1:-}" == "--check" ]]; then
  diff -u "${secret_golden}" "${dev_secret}"
else
  cp "${dev_secret}" "${secret_golden}"
  echo "wrote ${secret_golden}"
fi

# A nil operator.workerPools must still render the factory-runs default.
"${HELM}" template factory "${root}/deploy/helm/factory" \
  --namespace factory \
  --set global.image.tag=test \
  --set operator.enabled=true \
  -f "${here}/nil-workerpools.yaml" >"${nil_render}"
python3 - "${nil_render}" <<'PY'
import pathlib
import sys

text = pathlib.Path(sys.argv[1]).read_text()
expect = {
    "WORKERPOOL_TASK_NAMESPACES": "factory-runs",
    "WORKER_API_KEY_SECRET_NAME": "factory-worker",
    "WORKER_API_KEY_SECRET_KEY": "FACTORY_API_KEY",
}
lines = text.splitlines()
found = {}
for i, line in enumerate(lines):
    stripped = line.strip()
    if not stripped.startswith("- name:"):
        continue
    name = stripped.split(":", 1)[1].strip()
    if name not in expect or i + 1 >= len(lines):
        continue
    value = lines[i + 1].strip()
    if value.startswith("value:"):
        found[name] = value.split(":", 1)[1].strip().strip('"')
for name, want in expect.items():
    got = found.get(name)
    if got != want:
        sys.exit(f"{name}={got!r}, want {want!r}")
PY
