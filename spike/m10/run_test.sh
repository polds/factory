#!/usr/bin/env bash
# Mode and preflight checks. No model, no workflow dispatch.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

fail() {
  printf 'm10 test fail: %s\n' "$*" >&2
  exit 1
}

checks=(
  backend_attestation_present
  backend_attestation_fresh
  code_sha
  image_digest
  adapter_digest
  capability
  credential
  grant
  execute_target_disabled
  mounts_secure
  private_image_fetch
  paid_probe_closed
)

offline="$(bash "${ROOT}/spike/m10/preflight.sh" offline)"
for name in "${checks[@]}"; do
  if [[ "${offline}" != *"FAIL ${name}"* && "${offline}" != *"PASS ${name}"* ]]; then
    fail "offline missing ${name}"
  fi
done
if [[ "${offline}" != *"model_probe count=0"* || "${offline}" != *"m10 preflight offline PASS"* ]]; then
  fail "offline preflight"
fi

set +e
live="$(bash "${ROOT}/spike/m10/preflight.sh" live 2>&1)"
live_code=$?
set -e
if [[ "${live_code}" -eq 0 ]]; then
  fail "live preflight exited 0"
fi
for name in "${checks[@]}"; do
  if [[ "${live}" != *"${name}"* ]]; then
    fail "live missing ${name}"
  fi
done
if [[ "${live}" != *"model_probe count=0"* || "${live}" != *"m10 preflight live blocked"* ]]; then
  fail "live preflight"
fi
if [[ "${live}" == *"m10 offline PASS"* ]]; then
  fail "live printed offline pass"
fi

set +e
mode_out="$(MODE=live bash "${ROOT}/spike/m10/run.sh" 2>&1)"
mode_code=$?
set -e
if [[ "${mode_code}" -eq 0 || "${mode_out}" != *"m10 supports offline only"* || "${mode_out}" == *"m10 offline PASS"* ]]; then
  fail "MODE=live was accepted"
fi

set +e
lin_out="$(MODE=offline LINEAR_API_KEY=CANARY-SYNTHETIC bash "${ROOT}/spike/m10/run.sh" 2>&1)"
lin_code=$?
set -e
if [[ "${lin_code}" -eq 0 || "${lin_out}" != *"m10 linear-off: refusing Linear config"* ]]; then
  fail "linear config was accepted"
fi
if [[ "${lin_out}" == *"CANARY-SYNTHETIC"* ]]; then
  fail "linear value was printed"
fi

echo "m10 run_test PASS"
