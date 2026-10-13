#!/usr/bin/env bash
# Prove lint-upcoming.sh fails closed once a language tree exists.
# Each case uses a throwaway root and, where a tool would otherwise be
# downloaded, a stub on PATH that exits non-zero.
set -euo pipefail

script="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lint-upcoming.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "${tmp}"' EXIT

fail() {
  echo "lint-upcoming fixture failed: $1" >&2
  exit 1
}

expect_fail() {
  local name="$1"
  shift
  if "$@"; then
    fail "${name} exited 0"
  fi
}

stub_bin="$(mktemp -d)"
cat >"${stub_bin}/ruff" <<'EOF'
#!/bin/sh
exit 1
EOF
cat >"${stub_bin}/sqlfluff" <<'EOF'
#!/bin/sh
exit 1
EOF
cat >"${stub_bin}/helm" <<'EOF'
#!/bin/sh
exit 1
EOF
cat >"${stub_bin}/check-jsonschema" <<'EOF'
#!/bin/sh
exit 1
EOF
chmod 0755 "${stub_bin}/"*

empty="${tmp}/empty"
mkdir -p "${empty}"
LINT_UPCOMING_ROOT="${empty}" "${script}" >/dev/null

ts="${tmp}/ts"
mkdir -p "${ts}/web"
printf '%s\n' '{"name":"console","scripts":{}}' >"${ts}/web/package.json"
expect_fail typescript env LINT_UPCOMING_ROOT="${ts}" "${script}"

py="${tmp}/py"
mkdir -p "${py}"
printf '%s\n' 'print("hi")' >"${py}/sample.py"
expect_fail python env LINT_UPCOMING_OFFLINE=1 LINT_UPCOMING_ROOT="${py}" PATH="${stub_bin}:${PATH}" "${script}"

sql="${tmp}/sql"
mkdir -p "${sql}"
printf '%s\n' 'select 1;' >"${sql}/001.sql"
expect_fail sql env LINT_UPCOMING_OFFLINE=1 LINT_UPCOMING_ROOT="${sql}" PATH="${stub_bin}:${PATH}" "${script}"

helm="${tmp}/helm"
mkdir -p "${helm}/deploy/helm/factory"
printf '%s\n' 'apiVersion: v2' 'name: factory' >"${helm}/deploy/helm/factory/Chart.yaml"
expect_fail helm env LINT_UPCOMING_OFFLINE=1 LINT_UPCOMING_ROOT="${helm}" PATH="${stub_bin}:${PATH}" "${script}"

schema="${tmp}/schema"
mkdir -p "${schema}"
printf '%s\n' '{}' >"${schema}/factory.schema.json"
expect_fail jsonschema env LINT_UPCOMING_OFFLINE=1 LINT_UPCOMING_ROOT="${schema}" PATH="${stub_bin}:${PATH}" "${script}"

echo "lint-upcoming fixtures passed"
