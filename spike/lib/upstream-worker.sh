# shellcheck shell=bash
# The oz-agent-worker pin is the single ARG WORKER_REF line in
# deploy/worker/Dockerfile. WORKER_REF in the environment overrides it.

upstream_worker_pin() {
  local root="$1"
  local file="$root/deploy/worker/Dockerfile"
  local ref=""
  ref="$(grep -E '^ARG WORKER_REF=[0-9a-f]{40}$' "$file" || true)"
  ref="${ref#ARG WORKER_REF=}"
  if [[ ! "$ref" =~ ^[0-9a-f]{40}$ ]]; then
    printf 'deploy/worker/Dockerfile must contain one ARG WORKER_REF=<40-hex sha>\n' >&2
    return 1
  fi
  printf '%s\n' "$ref"
}

upstream_worker_ref() {
  local root="$1"
  if [[ -n "${WORKER_REF:-}" ]]; then
    printf '%s\n' "$WORKER_REF"
    return 0
  fi
  upstream_worker_pin "$root"
}
