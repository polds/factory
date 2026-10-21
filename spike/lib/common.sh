# shellcheck shell=bash
# Shared helpers for spike/m2, spike/m3, spike/m4, and spike/m5.
# This file has no top-level side effects. Callers keep their own options,
# traps, and variables. Bash resolves these names in the caller at call time:
#   die                bridge_gateway (m2, m3, m4; m5 keeps its own copy)
#   on_err             run_capture restores this ERR trap
#   compose            save_compose_logs
#   COMPOSE_STARTED    save_compose_logs
#   WORK               save_compose_logs
#   SECRETS            redact_if_live appends a replaced value

# docker info prints aarch64/x86_64. Images and GOARCH use arm64/amd64.
normalize_docker_platform() {
  case "$1" in
    linux/aarch64 | linux/arm64) printf '%s\n' linux/arm64 ;;
    linux/x86_64 | linux/amd64) printf '%s\n' linux/amd64 ;;
    *) return 1 ;;
  esac
}

goarch_for_platform() {
  case "$1" in
    linux/arm64) printf '%s\n' arm64 ;;
    linux/amd64) printf '%s\n' amd64 ;;
    *) return 1 ;;
  esac
}

uname_for_platform() {
  case "$1" in
    linux/arm64) printf '%s\n' aarch64 ;;
    linux/amd64) printf '%s\n' x86_64 ;;
    *) return 1 ;;
  esac
}

# load_env_file reads KEY=VALUE lines. It does not expand or execute values.
# Empty values are ignored. A variable that is already non-empty is kept.
load_env_file() {
  local file="$1" line="" n=0 key="" val=""
  local -a keys=() vals=()
  [[ -f "$file" ]] || return 1
  while IFS= read -r line || [[ -n "$line" ]]; do
    n=$((n + 1))
    line="${line%$'\r'}"
    line="${line#"${line%%[![:space:]]*}"}"
    [[ -z "$line" || "$line" == \#* ]] && continue
    if [[ "$line" == export\ * ]]; then
      line="${line#export }"
      line="${line#"${line%%[![:space:]]*}"}"
    fi
    if [[ ! "$line" =~ ^([A-Za-z_][A-Za-z0-9_]*)=(.*)$ ]]; then
      echo "deploy/.env:${n}: invalid line" >&2
      return 1
    fi
    key="${BASH_REMATCH[1]}"
    val="${BASH_REMATCH[2]}"
    if [[ ${#val} -ge 2 && "$val" == \"*\" && "$val" == *\" ]]; then
      val="${val:1:${#val}-2}"
    elif [[ ${#val} -ge 2 && "$val" == \'*\' && "$val" == *\' ]]; then
      val="${val:1:${#val}-2}"
    fi
    if [[ -z "$val" || -n "${!key:-}" ]]; then
      continue
    fi
    keys+=("$key")
    vals+=("$val")
  done <"$file"
  local i
  for i in "${!keys[@]}"; do
    printf -v "${keys[$i]}" '%s' "${vals[$i]}"
    export "${keys[$i]}"
  done
}

# run_capture stores a command's status in a variable and returns 0.
# Turning errexit back on before `return <nonzero>` aborts the whole script
# (bash exits on that return), so the caller never reaches fail.
run_capture() {
  local __var="$1"
  shift
  local __errexit=0
  case $- in
    *e*) __errexit=1 ;;
  esac
  trap - ERR
  set +e
  "$@"
  local __code=$?
  if [[ "$__errexit" -eq 1 ]]; then
    set -e
  fi
  trap 'on_err $? $LINENO' ERR
  printf -v "$__var" '%s' "$__code"
}

# harness_contract_exit is true for the codes the harness returns on purpose.
# 126, and busybox's 2, mean the file was not an executable for this platform.
harness_contract_exit() {
  case "$1" in
    0 | 10 | 20 | 21 | 30) return 0 ;;
    *) return 1 ;;
  esac
}

copy_if_empty() {
  local dest="$1" src="$2"
  if [[ -z "${!dest:-}" && -n "${!src:-}" ]]; then
    printf -v "$dest" '%s' "${!src}"
    export "${dest?}"
  fi
}

# Hostname and port only. Userinfo in the URL is a secret and is not printed.
url_log_host() {
  RAW="$1" python3 - <<'PY'
import os
from urllib.parse import urlparse
parts = urlparse(os.environ.get("RAW", ""))
host = parts.hostname or ""
if parts.port:
    host = f"{host}:{parts.port}"
print(host)
PY
}

# assert_linux_elf fails unless the file is a 64-bit little-endian Linux ELF
# for arch (arm64 or amd64). A darwin binary is not an ELF.
assert_linux_elf() {
  local bin="$1" arch="$2"
  python3 - "$bin" "$arch" <<'PY'
import struct
import sys

path, arch = sys.argv[1], sys.argv[2]
want = {"amd64": 62, "arm64": 183}
data = open(path, "rb").read(20)
if len(data) < 20 or data[:4] != b"\x7fELF":
    sys.stderr.write("%s: not a linux ELF binary\n" % path)
    sys.exit(1)
if data[4] != 2 or data[5] != 1:
    sys.stderr.write("%s: want a 64-bit little-endian ELF\n" % path)
    sys.exit(1)
machine = struct.unpack_from("<H", data, 18)[0]
if arch not in want or machine != want[arch]:
    sys.stderr.write("%s: ELF machine %s, want %s for %s\n" % (path, machine, want.get(arch), arch))
    sys.exit(1)
PY
}

assert_image_platform() {
  local image="$1" want="$2" got=""
  got="$(docker image inspect --format '{{.Os}}/{{.Architecture}}' "$image" 2>/dev/null || true)"
  if [[ "$got" != "$want" ]]; then
    echo "image ${image} platform is ${got:-unknown}, want ${want}" >&2
    return 1
  fi
}

save_compose_logs() {
  if [[ "${COMPOSE_STARTED}" -ne 1 ]]; then
    return 0
  fi
  mkdir -p "$WORK"
  compose logs --no-color >"$WORK/compose.log" 2>&1 || true
}

bridge_gateway() {
  local gw
  gw="$(docker network inspect bridge --format '{{(index .IPAM.Config 0).Gateway}}')"
  if [[ ! "$gw" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    die "could not determine an IPv4 gateway for the docker bridge (got ${gw:-empty})"
  fi
  printf '%s\n' "$gw"
}

accept_mode() {
  case "${1:-}" in
    offline | live | live-ci) return 0 ;;
    *) return 1 ;;
  esac
}

read_model() {
  awk '/^model: / { print $2; exit }' "$1"
}

require_id() {
  local value="$1" prefix="$2"
  if [[ ! "$value" =~ ^${prefix}[0-9A-Z]{26}$ ]]; then
    echo "bad ${prefix} id" >&2
    return 1
  fi
}

write_final_script() {
  local dest="$1" text="$2"
  python3 - "$dest" "$text" <<'PY'
import json
import sys

path, text = sys.argv[1], sys.argv[2]
with open(path, "w", encoding="utf-8") as fh:
    json.dump([{"final": text}], fh)
    fh.write("\n")
PY
}

# redact_if_live keeps a replaced secret on the redact list. The replacement
# and an empty value are not added.
redact_if_live() {
  local value="$1" replacement="$2"
  if [[ -n "$value" && "$value" != "$replacement" ]]; then
    SECRETS+=("$value")
  fi
}
