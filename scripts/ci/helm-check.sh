#!/usr/bin/env bash
# Lint the factory chart, fail an empty image tag, and diff helm template
# output against the committed goldens. Drift exits non-zero.
# UPDATE_GOLDENS=1 rewrites deploy/helm/factory/tests/golden.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source-path=SCRIPTDIR
# shellcheck source=helm-lib.sh
source "${root}/scripts/ci/helm-lib.sh"
install_helm
install_kubeconform

chart="${root}/deploy/helm/factory"
release=factory
namespace=factory
golden="${chart}/tests/golden"
upstream="${chart}/tests/upstream/role.yaml"

echo "helm lint ${chart}"
"${HELM}" lint "${chart}"

refuse_tag() {
  local label="$1"
  shift
  local err
  err="$(mktemp)"
  if "${HELM}" template "${release}" "${chart}" --namespace "${namespace}" "$@" >/dev/null 2>"${err}"; then
    echo "${label}: rendering succeeded" >&2
    rm -f "${err}"
    exit 1
  fi
  if ! grep -q "global.image.tag is required and must be pinned" "${err}"; then
    echo "${label}: unexpected render error" >&2
    cat "${err}" >&2
    rm -f "${err}"
    exit 1
  fi
  echo "${label}:"
  cat "${err}"
  rm -f "${err}"
}

refuse_tag "empty tag"
refuse_tag "latest tag" --set global.image.tag=latest

work="$(mktemp -d "${TMPDIR:-/tmp}/helm-check.XXXXXX")"
# shellcheck disable=SC2329 # invoked by the EXIT trap
cleanup() {
  rm -rf "${work}"
}
trap cleanup EXIT

render() {
  local dest="$1"
  shift
  "${HELM}" template "${release}" "${chart}" --namespace "${namespace}" "$@" >"${dest}"
}

render "${work}/default.yaml" --set global.image.tag=test
render "${work}/dev.yaml" -f "${chart}/values-dev.yaml"
render "${work}/multi-pool.yaml" -f "${chart}/values-multi-pool.yaml"

if [[ "${UPDATE_GOLDENS:-}" == "1" ]]; then
  mkdir -p "${golden}"
  cp "${work}/default.yaml" "${golden}/default.yaml"
  cp "${work}/dev.yaml" "${golden}/dev.yaml"
  cp "${work}/multi-pool.yaml" "${golden}/multi-pool.yaml"
  echo "rewrote ${golden}"
fi

for name in default dev multi-pool; do
  if [[ ! -f "${golden}/${name}.yaml" ]]; then
    echo "missing golden ${golden}/${name}.yaml (run UPDATE_GOLDENS=1)" >&2
    exit 1
  fi
  if ! diff -u "${golden}/${name}.yaml" "${work}/${name}.yaml"; then
    echo "helm template drift: ${name}" >&2
    exit 1
  fi
  echo "golden ${name} matches"
done

python3 - "${upstream}" "${work}/default.yaml" "${work}/dev.yaml" "${work}/multi-pool.yaml" <<'PY'
import pathlib
import sys

banned = {
    "ClusterRole",
    "ClusterRoleBinding",
    "CustomResourceDefinition",
    "Namespace",
}


def docs(text):
    parts = text.split("\n---\n")
    out = []
    for part in parts:
        body = part.strip()
        if body.startswith("---\n"):
            body = body[4:].strip()
        if not body or body == "---":
            continue
        out.append(body)
    return out


def kinds_of(text):
    found = []
    for line in text.splitlines():
        if line.startswith("kind:"):
            found.append(line.split(":", 1)[1].strip())
    return found


def rules_of(doc):
    marker = "\nrules:\n"
    idx = doc.find(marker)
    if idx < 0 and doc.startswith("rules:\n"):
        return doc[len("rules:") + 1 :]
    if idx < 0:
        return None
    rules = doc[idx + 1 :]
    lines = []
    for line in rules.splitlines():
        if line.startswith("{{"):
            break
        lines.append(line.rstrip())
    while lines and lines[-1] == "":
        lines.pop()
    return "\n".join(lines) + "\n"


upstream = pathlib.Path(sys.argv[1]).read_text()
want = rules_of(upstream)
if want is None or not want.startswith("rules:\n"):
    sys.exit("upstream role has no rules block")

for path in sys.argv[2:]:
    text = pathlib.Path(path).read_text()
    kinds = kinds_of(text)
    print(f"{path} kinds: {' '.join(sorted(set(kinds)))}")
    bad = sorted(set(kinds) & banned)
    if bad:
        sys.exit(f"{path} rendered cluster-scoped kinds: {' '.join(bad)}")
    roles = []
    for doc in docs(text):
        if "\nkind: Role\n" in f"\n{doc}\n":
            rules = rules_of(doc)
            if rules is None:
                sys.exit(f"{path} Role has no rules")
            roles.append(rules)
    if not roles:
        sys.exit(f"{path} rendered no Role")
    for rules in roles:
        if rules != want:
            sys.exit(f"{path} Role rules differ from upstream\n--- upstream\n{want}--- rendered\n{rules}")
    print(f"{path} roles: {len(roles)} match upstream")
PY

echo "kubeconform kubernetes ${KUBERNETES_SCHEMA_VERSION}"
"${KUBECONFORM}" \
  -kubernetes-version "${KUBERNETES_SCHEMA_VERSION}" \
  -strict \
  -summary \
  -schema-location "${KUBECONFORM_SCHEMA_LOCATION}" \
  "${golden}/default.yaml" \
  "${golden}/dev.yaml" \
  "${golden}/multi-pool.yaml"

echo "helm-check ok"
