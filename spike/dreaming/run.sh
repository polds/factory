#!/usr/bin/env bash
# Driver for the Dreaming #454 run.
#   ./spike/dreaming/run.sh preflight|live
#   ./spike/dreaming/run.sh scan <evidence-dir>
# MODE selects preflight or live when no argument is given.
# Live and preflight are for the orchestrator. This script does not
# dispatch the workflow.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${root}"

scan() {
  local dest="$1"
  if [[ ! -d "${dest}" ]]; then
    echo "evidence dir missing" >&2
    exit 1
  fi
  if [[ -n "${RUNNER_TEMP:-}" && "${dest}" == "${RUNNER_TEMP}/dreaming-src"* ]]; then
    echo "refusing to scan the role checkout as evidence" >&2
    exit 1
  fi
  local hit
  hit="$(grep -R -l -E -e 'ghs_' -e 'ghp_' -e 'github_pat_' -e '(^|[^A-Za-z0-9])sk-' -e '-----BEGIN [A-Z ]*PRIVATE KEY-----' -e 'synthetic-seat-marker' "${dest}" || true)"
  if [[ -n "${hit}" ]]; then
    echo "evidence scan hit" >&2
    printf '%s\n' "${hit}" >&2
    exit 1
  fi
  printf 'secrets-scan hits 0\n' | tee "${dest}/secrets-scan.txt" >/dev/null
}

if [[ "${1:-}" == "scan" ]]; then
  if [[ $# -ne 2 ]]; then
    echo "usage: spike/dreaming/run.sh scan <evidence-dir>" >&2
    exit 2
  fi
  scan "$2"
  exit 0
fi

mode="${1:-${MODE:-}}"
case "${mode}" in
  preflight | live) ;;
  *)
    echo "usage: spike/dreaming/run.sh preflight|live" >&2
    exit 2
    ;;
esac

if [[ "${CONFIRM:-}" != "dreaming-live-454" ]]; then
  echo "guard refused" >&2
  exit 1
fi

export FACTORY_PUBLISH_DRAFT_REPO="${FACTORY_PUBLISH_DRAFT_REPO:-northstar-ai-labs/dreaming}"
export FACTORY_ROLE_SOURCE="${FACTORY_ROLE_SOURCE:-${RUNNER_TEMP:-/tmp}/dreaming-src}"

exec "${root}/spike/dreaming/preflight.sh" "${mode}"
