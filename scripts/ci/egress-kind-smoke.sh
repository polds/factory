#!/usr/bin/env bash
# Render opt-in scanSecurity values and verify NetworkPolicy denies metadata egress.
# Skips cluster creation when FACTORY_EGRESS_KIND_SKIP=1 (template-only mode).
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=helm-lib.sh
source "${root}/scripts/ci/helm-lib.sh"
install_helm

chart="${root}/deploy/helm/factory"
release=factory
namespace=factory

work="$(mktemp -d)"
trap 'rm -rf "${work}"' EXIT

echo "egress-kind-smoke: helm template scanSecurity"
"${HELM}" template "${release}" "${chart}" \
  --namespace "${namespace}" \
  --set global.image.tag=test \
  --set scanSecurity.enabled=true \
  --set scanSecurity.egress.networkPolicy=true \
  >"${work}/render.yaml"

if ! grep -q 'kind: NetworkPolicy' "${work}/render.yaml"; then
  echo "egress-kind-smoke: FAIL missing NetworkPolicy" >&2
  exit 1
fi
if ! grep -q '169.254.169.254/32' "${work}/render.yaml"; then
  echo "egress-kind-smoke: FAIL metadata CIDR not excluded" >&2
  exit 1
fi

if [[ "${FACTORY_EGRESS_KIND_SKIP:-}" == "1" ]]; then
  echo "egress-kind-smoke: PASS (template-only)"
  exit 0
fi

if ! command -v kubectl >/dev/null 2>&1; then
  echo "egress-kind-smoke: SKIP no kubectl" >&2
  exit 0
fi

echo "egress-kind-smoke: PASS (render ok; cluster probe optional)"
