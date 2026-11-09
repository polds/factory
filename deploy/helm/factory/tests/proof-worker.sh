#!/usr/bin/env bash
# Proof pool render. A worker with proof: true fails if setupCommand or
# teardownCommand is set. Absent commands leave both keys out of the config.
# A worker without proof still renders a configured command.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd)"
# shellcheck source-path=SCRIPTDIR
# shellcheck source=../../../../scripts/ci/helm-lib.sh
source "${root}/scripts/ci/helm-lib.sh"
install_helm

chart="${root}/deploy/helm/factory"
ns=(--namespace factory --set global.image.tag=test)

render() {
  "${HELM}" template factory "${chart}" "${ns[@]}" "$@"
}

proof_config() {
  awk '
    $0 ~ /worker_id: "proof-worker"/ {p=1}
    p && $0 ~ /worker_id: "/ && $0 !~ /worker_id: "proof-worker"/ {p=0}
    p {print}
  '
}

echo "proof pool omits setup and teardown"
cfg="$(render | proof_config)"
printf '%s\n' "${cfg}" | grep -q 'worker_id: "proof-worker"'
if printf '%s\n' "${cfg}" | grep -Eq 'setup_command:|teardown_command:'; then
  echo "proof pool rendered a setup or teardown command" >&2
  printf '%s\n' "${cfg}" >&2
  exit 1
fi

inject_values() {
  local key="$1"
  local value="$2"
  local which="$3"
  local dest="$4"
  python3 - "$root/deploy/helm/factory/values.yaml" "$key" "$value" "$which" "$dest" <<'PY'
import pathlib, sys
src, key, value, which, dest = sys.argv[1:]
text = pathlib.Path(src).read_text()
if which == "proof":
    start = text.index("  - name: proof\n")
else:
    start = text.index("  - name: standard\n")
rel = text[start:]
mark = "    kubernetesBackend:\n"
at = rel.index(mark) + len(mark)
line = f"      {key}: {value}\n"
pathlib.Path(dest).write_text(text[:start] + rel[:at] + line + rel[at:])
PY
}

refuse() {
  local label="$1"
  local key="$2"
  local err values
  err="$(mktemp)"
  values="$(mktemp)"
  inject_values "$key" "echo-${label}" proof "$values"
  if render -f "$values" >/dev/null 2>"${err}"; then
    echo "${label}: rendering succeeded" >&2
    rm -f "${err}" "$values"
    exit 1
  fi
  if ! grep -q "proof pool proof cannot set setupCommand or teardownCommand" "${err}"; then
    echo "${label}: unexpected render error" >&2
    cat "${err}" >&2
    rm -f "${err}" "$values"
    exit 1
  fi
  echo "${label}: refused"
  rm -f "${err}" "$values"
}

refuse "setupCommand" setupCommand
refuse "teardownCommand" teardownCommand

echo "legacy pool still renders setup_command"
legacy_values="$(mktemp)"
inject_values setupCommand echo-legacy standard "$legacy_values"
legacy="$(render -f "$legacy_values")"
rm -f "$legacy_values"
printf '%s\n' "${legacy}" | awk '
  $0 ~ /worker_id: "k8s-standard"/ {p=1}
  p && $0 ~ /worker_id: "/ && $0 !~ /worker_id: "k8s-standard"/ {p=0}
  p {print}
' | grep -q 'setup_command: |-'
proof_after="$(printf '%s\n' "${legacy}" | proof_config)"
if printf '%s\n' "${proof_after}" | grep -Eq 'setup_command:|teardown_command:'; then
  echo "legacy setup leaked into the proof pool" >&2
  exit 1
fi

echo "proof worker render PASS"
