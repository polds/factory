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

refuse_operator_replicas() {
  local err
  err="$(mktemp)"
  if "${HELM}" template "${release}" "${chart}" --namespace "${namespace}" \
    --set global.image.tag=test \
    --set operator.enabled=true >/dev/null 2>"${err}"; then
    echo "operator replicas guard: rendering succeeded" >&2
    rm -f "${err}"
    exit 1
  fi
  if ! grep -q "operator.enabled needs controlPlane.replicas=1 (API-backed definitions are per pod)" "${err}"; then
    echo "operator replicas guard: unexpected render error" >&2
    cat "${err}" >&2
    rm -f "${err}"
    exit 1
  fi
  echo "operator replicas guard:"
  cat "${err}"
  rm -f "${err}"
}

refuse_operator_replicas

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
# operator.enabled with the chart default replicas (2) is rejected above.
# replicas=1 is what reproduces a successful operator render. cert-manager
# swaps the self-signed webhook ClusterRole for Issuer and Certificate.
render "${work}/operator.yaml" \
  --set global.image.tag=test \
  --set operator.enabled=true \
  --set controlPlane.replicas=1
render "${work}/operator-certmanager.yaml" \
  --set global.image.tag=test \
  --set operator.enabled=true \
  --set controlPlane.replicas=1 \
  --set operator.admissionWebhook.certManager=true

if [[ "${UPDATE_GOLDENS:-}" == "1" ]]; then
  mkdir -p "${golden}"
  cp "${work}/default.yaml" "${golden}/default.yaml"
  cp "${work}/dev.yaml" "${golden}/dev.yaml"
  cp "${work}/multi-pool.yaml" "${golden}/multi-pool.yaml"
  cp "${work}/operator.yaml" "${golden}/operator.yaml"
  cp "${work}/operator-certmanager.yaml" "${golden}/operator-certmanager.yaml"
  echo "rewrote ${golden}"
fi

for name in default dev multi-pool operator operator-certmanager; do
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

python3 - "${upstream}" \
  "${work}/default.yaml" \
  "${work}/dev.yaml" \
  "${work}/multi-pool.yaml" \
  "${work}/operator.yaml" \
  "${work}/operator-certmanager.yaml" <<'PY'
import pathlib
import sys

banned = {
    "ClusterRole",
    "ClusterRoleBinding",
    "CustomResourceDefinition",
    "Namespace",
}
# Operator mode may render the webhook objects and, when the webhook cert is
# self-managed, the ClusterRole that patches that one ValidatingWebhookConfiguration.
# CRDs are allowed if a render includes them. Namespace stays banned.
operator_allowed = {
    "ValidatingWebhookConfiguration",
    "ClusterRole",
    "ClusterRoleBinding",
    "CustomResourceDefinition",
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

def webhook_cluster_role(doc):
    if "\nkind: ClusterRole\n" not in f"\n{doc}\n":
        return True
    resources = [
        line.strip()
        for line in doc.splitlines()
        if line.strip().startswith("resources:")
    ]
    return bool(resources) and all(
        line == 'resources: ["validatingwebhookconfigurations"]' for line in resources
    )


for path in sys.argv[2:]:
    text = pathlib.Path(path).read_text()
    kinds = kinds_of(text)
    print(f"{path} kinds: {' '.join(sorted(set(kinds)))}")
    operator_render = pathlib.Path(path).name.startswith("operator")
    if operator_render:
        bad = sorted(set(kinds) & banned - operator_allowed)
        if bad:
            sys.exit(f"{path} rendered cluster-scoped kinds: {' '.join(bad)}")
        for doc in docs(text):
            if not webhook_cluster_role(doc):
                sys.exit(f"{path} ClusterRole is not the webhook-cert role")
    else:
        bad = sorted(set(kinds) & banned)
        if bad:
            sys.exit(f"{path} rendered cluster-scoped kinds: {' '.join(bad)}")
    roles = []
    for doc in docs(text):
        if "\nkind: Role\n" not in f"\n{doc}\n":
            continue
        rules = rules_of(doc)
        if rules is None:
            sys.exit(f"{path} Role has no rules")
        # The operator Role is chart RBAC, not the upstream worker Role.
        if "app.kubernetes.io/component: operator" in doc:
            continue
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
  "${golden}/multi-pool.yaml" \
  "${golden}/operator.yaml"

# cert-manager.io/v1 Issuer and Certificate are not in the Kubernetes
# standalone schema set, so this file uses -ignore-missing-schemas.
# The other renders stay strict.
echo "kubeconform operator-certmanager (-ignore-missing-schemas: cert-manager Issuer, Certificate)"
"${KUBECONFORM}" \
  -kubernetes-version "${KUBERNETES_SCHEMA_VERSION}" \
  -strict \
  -ignore-missing-schemas \
  -summary \
  -schema-location "${KUBECONFORM_SCHEMA_LOCATION}" \
  "${golden}/operator-certmanager.yaml"

regen="${root}/internal/operator/workerpool/testdata/regen.sh"
if [[ -f "${regen}" ]]; then
  echo "workerpool regen --check"
  bash "${regen}" --check
else
  echo "workerpool regen.sh absent; skip"
fi

echo "helm-check ok"
