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
# shellcheck disable=SC2329 # invoked by the EXIT trap
cleanup() {
  rm -f "${full}" "${filtered}"
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
