#!/usr/bin/env bash
# Lint languages this repository is going to contain, once those trees exist.
#
# Each check passes when its tree is absent. When the tree is present the
# check fails closed: missing toolchain, missing npm scripts, or a lint
# finding all fail the job. Versions are pinned so a tool release cannot
# change the result without a review.
#
#   web/package.json                 React/TypeScript console (docs/09)
#   *.py                             not a control-plane language; still linted
#   *.sql                            Postgres migrations (M2)
#   deploy/helm/factory/Chart.yaml   Helm chart (docs/10)
#   *.schema.json                    published JSON Schemas (M3)
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${root}"

export PATH="${HOME}/.local/bin:${PATH}"

RUFF_VERSION="0.17.0"
SQLFLUFF_VERSION="4.4.0"
CHECK_JSONSCHEMA_VERSION="0.38.2"
HELM_VERSION="3.22.0"

found_any() {
  local name="$1"
  find . \
    \( -path './.git' -o -path '*/.work' -o -path '*/node_modules' \) -prune \
    -o -name "${name}" -print -quit | grep -q .
}

echo "typescript"
if [[ -f web/package.json ]]; then
  node -e '
    const pkg = require("./web/package.json");
    const scripts = pkg.scripts || {};
    const required = ["lint", "format:check", "test", "build"];
    const missing = required.filter((name) => !scripts[name]);
    if (missing.length) {
      console.error("web/package.json is missing scripts: " + missing.join(", "));
      process.exit(1);
    }
  '
  npm ci --prefix web
  npm run lint --prefix web
  npm run format:check --prefix web
  npm test --prefix web
  npm run build --prefix web
else
  echo "  no web/package.json; console lint runs once the SPA lands"
fi

echo "python"
if found_any '*.py'; then
  python3 -m pip install --user "ruff==${RUFF_VERSION}"
  mapfile -d '' py_files < <(find . \
    \( -path './.git' -o -path '*/.work' -o -path '*/node_modules' \) -prune \
    -o -name '*.py' -print0)
  ruff check "${py_files[@]}"
  ruff format --check "${py_files[@]}"
else
  echo "  no Python sources"
fi

echo "sql"
if found_any '*.sql'; then
  python3 -m pip install --user "sqlfluff==${SQLFLUFF_VERSION}"
  mapfile -d '' sql_files < <(find . \
    \( -path './.git' -o -path '*/.work' -o -path '*/node_modules' \) -prune \
    -o -name '*.sql' -print0)
  sqlfluff lint --dialect postgres "${sql_files[@]}"
else
  echo "  no SQL sources"
fi

echo "helm"
if [[ -f deploy/helm/factory/Chart.yaml ]]; then
  if ! command -v helm >/dev/null 2>&1 || [[ "$(helm version --template '{{.Version}}')" != "v${HELM_VERSION}" ]]; then
    case "$(uname -m)" in
      x86_64) helm_arch="amd64" ;;
      aarch64 | arm64) helm_arch="arm64" ;;
      *)
        echo "unsupported architecture for helm: $(uname -m)"
        exit 1
        ;;
    esac
    archive="helm-v${HELM_VERSION}-linux-${helm_arch}.tar.gz"
    curl -fsSL -o "/tmp/${archive}" "https://get.helm.sh/${archive}"
    tar -C /tmp -xzf "/tmp/${archive}" "linux-${helm_arch}/helm"
    mkdir -p "${HOME}/.local/bin"
    install -m 0755 "/tmp/linux-${helm_arch}/helm" "${HOME}/.local/bin/helm"
  fi
  helm lint deploy/helm/factory
  helm template factory deploy/helm/factory >/dev/null
else
  echo "  no deploy/helm/factory/Chart.yaml"
fi

echo "jsonschema"
if found_any '*.schema.json'; then
  python3 -m pip install --user "check-jsonschema==${CHECK_JSONSCHEMA_VERSION}"
  mapfile -d '' schema_files < <(find . \
    \( -path './.git' -o -path '*/.work' -o -path '*/node_modules' \) -prune \
    -o -name '*.schema.json' -print0)
  check-jsonschema --check-metaschema "${schema_files[@]}"
else
  echo "  no *.schema.json files"
fi
