#!/usr/bin/env bash
# Pin and clone checks for the unmodified oz-agent-worker. No network.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=spike/lib/upstream-worker.sh
. "$ROOT/spike/lib/upstream-worker.sh"

fail() {
  printf 'FAIL %s\n' "$*" >&2
  exit 1
}

pin="$(upstream_worker_pin "$ROOT")"
[[ "$pin" =~ ^[0-9a-f]{40}$ ]] || fail "pin is not a full sha"

unset WORKER_REF
[[ "$(upstream_worker_ref "$ROOT")" == "$pin" ]] || fail "default ref is not the pin"
[[ "$(WORKER_REF=main upstream_worker_ref "$ROOT")" == "main" ]] ||
  fail "WORKER_REF did not override"

# Spike scripts and workflows must not default the worker to branch main.
# upstream_worker_test.sh itself sets WORKER_REF=main to prove the override.
while IFS= read -r -d '' file; do
  found="$(grep -n -E \
    -e 'WORKER_REF:-["'\'']?main["'\'']?' \
    -e 'WORKER_REF:[[:space:]]*["'\'']?main["'\'']?[[:space:]]*$' \
    -e 'git clone.*oz-agent-worker' \
    "$file" || true)"
  if grep -q -F 'oz-agent-worker.git' "$file" && grep -q -E 'git clone' "$file"; then
    found="${found}"$'\n'"clones oz-agent-worker.git"
  fi
  if [[ -n "$found" ]]; then
    printf '%s\n' "$found" >&2
    fail "floating worker ref in ${file#"$ROOT"/}"
  fi
done < <(find "$ROOT/spike" "$ROOT/.github/workflows" \
  \( -path '*/.work' -o -name 'upstream_worker_test.sh' \) -prune -o \
  \( -name '*.sh' -o -name '*.yml' -o -name '*.yaml' \) -print0)
for script in "$ROOT/spike/m0/run.sh" "$ROOT/spike/m1/run.sh"; do
  grep -F 'spike/lib/clone-upstream-worker.sh' "$script" >/dev/null ||
    fail "$script does not call the shared clone script"
done

count="$(grep -c -E "^ARG WORKER_REF=${pin}$" "$ROOT/deploy/worker/Dockerfile" || true)"
[[ "$count" == "1" ]] || fail "Dockerfile pin count is ${count}"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
git -C "$tmp" init -q -b main repo
git -C "$tmp/repo" config user.email t@example.com
git -C "$tmp/repo" config user.name t
printf 'one\n' >"$tmp/repo/f"
git -C "$tmp/repo" add f
git -C "$tmp/repo" commit -q -m one
old="$(git -C "$tmp/repo" rev-parse HEAD)"
printf 'two\n' >"$tmp/repo/f"
git -C "$tmp/repo" commit -q -am two
tip="$(git -C "$tmp/repo" rev-parse HEAD)"
[[ "$old" != "$tip" ]] || fail "fixture did not advance"

WORKER_REPO="$tmp/repo" WORKER_REF="$old" \
  bash "$ROOT/spike/lib/clone-upstream-worker.sh" "$tmp/pinned"
got="$(git -C "$tmp/pinned" rev-parse HEAD)"
[[ "$got" == "$old" ]] || fail "clone fetched ${got}, want ${old}"
[[ -z "$(git -C "$tmp/pinned" status --porcelain)" ]] || fail "cloned tree is dirty"

WORKER_REPO="$tmp/repo" WORKER_REF="main" \
  bash "$ROOT/spike/lib/clone-upstream-worker.sh" "$tmp/branch"
got="$(git -C "$tmp/branch" rev-parse HEAD)"
[[ "$got" == "$tip" ]] || fail "branch override fetched ${got}, want ${tip}"

printf 'dirty\n' >"$tmp/pinned/f"
WORKER_REPO="$tmp/repo" WORKER_REF="$tip" \
  bash "$ROOT/spike/lib/clone-upstream-worker.sh" "$tmp/pinned"
[[ "$(git -C "$tmp/pinned" rev-parse HEAD)" == "$old" ]] || fail "existing checkout moved"
[[ -n "$(git -C "$tmp/pinned" status --porcelain)" ]] || fail "existing checkout was recloned"

printf 'ok upstream worker pin %s\n' "$pin"
