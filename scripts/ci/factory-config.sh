#!/usr/bin/env bash
# Pre-merge factory/config check (docs/03 layer 3).
#
# Builds factoryctl and runs `factoryctl validate` on the definition
# trees this workflow guards:
#
#   examples/acme-factory     required
#   deploy/m2-factory         required
#   deploy/m3-factory         when that directory is present
#   deploy/m4-factory         required
#   deploy/m5-factory         required
#   deploy/m6-factory         required
#
# factoryctl prints "file:line: message" and exits 1 when any diagnostic
# is an error. Warnings stay on stdout and do not fail the run. With
# set -e, a rejected tree fails this script and therefore the Actions job.
#
# --dry-run copies examples/acme-factory, appends an unknown factory.yaml
# key, and validates that copy instead of the real trees. validate must
# reject it; the script then exits with validate's non-zero status, which
# is the proof that an invalid tree fails the job. The workflow does not
# pass --dry-run.
#
# --is-config-path <path> exits 0 when <path> is a factory/config input
# and 1 when it is not. It does not build or validate. The deploy rule
# is only a top-level deploy/<name>-factory/... path. A case pattern
# deploy/*-factory/* also matches deploy/nested/<name>-factory/...
# because * matches slashes.
#
# Portable to macOS bash 3.2 and Linux bash: no GNU-only flags, no
# mapfile, no associative arrays. The factoryctl binary is built for the
# host (it is not copied into a container).
set -euo pipefail

# Match the path list in .github/workflows/factory-config.yml.
# examples/* and schemas/* are prefix rules; * may cross slashes there.
is_config_path() {
  case "$1" in
    examples/* | schemas/*) return 0 ;;
    .github/workflows/factory-config.yml) return 0 ;;
    scripts/ci/factory-config.sh) return 0 ;;
  esac
  case "$1" in
    deploy/*/*) ;;
    *) return 1 ;;
  esac
  local rest name
  rest="${1#deploy/}"
  name="${rest%%/*}"
  case "$name" in
    *-factory) return 0 ;;
    *) return 1 ;;
  esac
}

if [[ "${1:-}" == "--is-config-path" ]]; then
  if [[ $# -ne 2 || -z "${2}" ]]; then
    echo "usage: scripts/ci/factory-config.sh --is-config-path <path>" >&2
    exit 2
  fi
  if is_config_path "${2}"; then
    exit 0
  fi
  exit 1
fi

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${root}"

dry_run=0
if [[ $# -gt 1 ]]; then
  echo "usage: scripts/ci/factory-config.sh [--dry-run]" >&2
  exit 2
fi
if [[ $# -eq 1 ]]; then
  case "${1}" in
    --dry-run)
      dry_run=1
      ;;
    *)
      echo "usage: scripts/ci/factory-config.sh [--dry-run]" >&2
      exit 2
      ;;
  esac
fi

# BSD mktemp (macOS) requires a template; GNU accepts the same form.
work="$(mktemp -d "${TMPDIR:-/tmp}/factory-config.XXXXXX")"
# shellcheck disable=SC2329 # invoked by the EXIT trap
cleanup() {
  rm -rf "${work}"
}
trap cleanup EXIT

factoryctl="${work}/factoryctl"
echo "building factoryctl"
go build -o "${factoryctl}" ./cmd/factoryctl

validate_tree() {
  local label="$1"
  local path="$2"
  echo "validate ${label} (${path})"
  "${factoryctl}" validate "${path}"
}

if [[ "${dry_run}" -eq 1 ]]; then
  planted="${work}/planted-factory"
  cp -R "${root}/examples/acme-factory" "${planted}"
  printf '\nnotAValidFactoryKey: planted\n' >>"${planted}/factory.yaml"
  echo "dry-run: planted unknown key notAValidFactoryKey in ${planted}/factory.yaml"
  validate_tree "planted-invalid" "${planted}"
  echo "factory/config: dry-run expected factoryctl validate to reject the planted tree" >&2
  exit 1
fi

if [[ ! -f examples/acme-factory/factory.yaml ]]; then
  echo "factory/config: examples/acme-factory/factory.yaml is missing" >&2
  exit 1
fi
validate_tree "acme-factory" "examples/acme-factory"

if [[ ! -f deploy/m2-factory/factory.yaml ]]; then
  echo "factory/config: deploy/m2-factory/factory.yaml is missing" >&2
  exit 1
fi
validate_tree "m2-factory" "deploy/m2-factory"

if [[ -d deploy/m3-factory ]]; then
  validate_tree "m3-factory" "deploy/m3-factory"
else
  echo "deploy/m3-factory is not present; skipped"
fi

if [[ ! -f deploy/m4-factory/factory.yaml ]]; then
  echo "factory/config: deploy/m4-factory/factory.yaml is missing" >&2
  exit 1
fi
validate_tree "m4-factory" "deploy/m4-factory"

if [[ ! -f deploy/m5-factory/factory.yaml ]]; then
  echo "factory/config: deploy/m5-factory/factory.yaml is missing" >&2
  exit 1
fi
validate_tree "m5-factory" "deploy/m5-factory"

if [[ ! -f deploy/m6-factory/factory.yaml ]]; then
  echo "factory/config: deploy/m6-factory/factory.yaml is missing" >&2
  exit 1
fi
validate_tree "m6-factory" "deploy/m6-factory"
