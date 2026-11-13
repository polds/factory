#!/usr/bin/env bash
# Secret data keys are valid Kubernetes names, and bootstrap secrets do not
# require a factory id. A slash key is refused.
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd)"
chart="${root}/deploy/helm/factory"
helm="${HELM:-helm}"
work="$(mktemp)"
trap 'rm -f "$work"' EXIT

"$helm" template factory "$chart" --namespace factory \
  --set global.image.tag=test \
  --set controlPlane.bootstrapSecrets.WIDGET_TOKEN=boot-value \
  --set controlPlane.factorySecrets.fac_known.WIDGET_TOKEN=known-value \
  >"$work"

python3 - "$work" <<'PY'
import re, sys
text = open(sys.argv[1]).read()
key = re.compile(r"^[-._a-zA-Z0-9]+$")
in_data = False
found = []
for line in text.splitlines():
    if line.strip() == "stringData:":
        in_data = True
        continue
    if in_data:
        if line.startswith("  ") and ":" in line:
            name = line.strip().split(":", 1)[0]
            found.append(name)
            if not key.fullmatch(name) or "/" in name:
                sys.exit(f"invalid secret key {name}")
        elif line and not line.startswith(" "):
            in_data = False
need = {"bootstrap.WIDGET_TOKEN", "fac_known.WIDGET_TOKEN"}
if not need <= set(found):
    sys.exit(f"missing keys {need - set(found)} in {found}")
if "FACTORY_SECRET_WIDGET_TOKEN" in text:
    sys.exit("unprefixed secret env")
if "FACTORY_BOOTSTRAP_SECRET_WIDGET_TOKEN" not in text:
    sys.exit("bootstrap env missing")
if "FACTORY_SECRET_fac_known__WIDGET_TOKEN" not in text:
    sys.exit("factory env missing")
print("factory secret keys ok")
PY

if "$helm" template factory "$chart" --namespace factory \
  --set global.image.tag=test \
  --set-json 'controlPlane.factorySecrets={"bad/id":{"WIDGET_TOKEN":"x"}}' \
  >/dev/null 2>"$work"; then
  echo "slash key rendered" >&2
  exit 1
fi
if ! grep -q "not a valid Secret key" "$work"; then
  echo "slash key error was not the secret-key guard" >&2
  cat "$work" >&2
  exit 1
fi
echo "slash key refused"
