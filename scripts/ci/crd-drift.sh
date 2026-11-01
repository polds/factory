#!/usr/bin/env bash
# Fail when committed CRDs differ from controller-gen.
#
# Two copies stay in lockstep: Helm installs deploy/helm/factory/crds, and
# factoryctl embeds internal/convert/crds because go:embed cannot see a
# parent directory. Regenerating uses the controller-gen tool pinned in go.mod.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${root}"

tmp="$(mktemp -d)"
trap 'rm -rf "${tmp}"' EXIT

go tool controller-gen crd:allowDangerousTypes=true \
  paths=./api/factory/v1alpha1 \
  output:crd:dir="${tmp}"

diff -ru deploy/helm/factory/crds "${tmp}"
diff -ru internal/convert/crds "${tmp}"
