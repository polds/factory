#!/usr/bin/env bash
# Synthetic G14/G18 preflight for disposable Docker acceptance hosts.
# Runs offline checks only; PASS/FAIL lines never include secret material.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
export REPO_ROOT="${root}"
# shellcheck source-path=SCRIPTDIR
# shellcheck source=scripts/lib/preflight.sh
source "${root}/deploy/scan-security/scripts/lib/preflight.sh"

mode="${1:-all}"
case "${mode}" in
  all | g14 | g18) ;;
  *)
    echo "usage: $0 [all|g14|g18]" >&2
    exit 2
    ;;
esac

fail=0
if [[ "${mode}" == "all" || "${mode}" == "g14" ]]; then
  run_check g14_deny_default || fail=1
  run_check g14_no_docker_socket_mount || fail=1
  run_check g14_metadata_denied || fail=1
fi
if [[ "${mode}" == "all" || "${mode}" == "g18" ]]; then
  run_check g18_resolver_matrix || fail=1
  run_check g18_rotation_redaction || fail=1
fi

if [[ "${fail}" -ne 0 ]]; then
  echo "scan-security preflight: FAIL"
  exit 1
fi
echo "scan-security preflight: PASS"
