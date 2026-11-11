#!/usr/bin/env bash
# Fail when a Dockerfile golang stage is older than the go.mod toolchain.
#
# Official golang images set GOTOOLCHAIN=local, so a build in that image
# ignores go.mod's toolchain line and ships the image's stdlib. govulncheck
# on source can pass while the image is older.
set -euo pipefail

if [[ $# -gt 1 ]]; then
  echo "usage: scripts/ci/go-toolchain-match.sh [root]" >&2
  exit 2
fi

if [[ $# -eq 1 ]]; then
  root="$(cd "$1" && pwd)"
else
  root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
fi

gomod="${root}/go.mod"
if [[ ! -f "${gomod}" ]]; then
  echo "go-toolchain-match: no go.mod at ${gomod}" >&2
  exit 2
fi

# Tag is VERSION[-variant][@digest]. A missing patch (golang:1.26) compares
# as 1.26.0: the floating minor tag does not name the stdlib that
# GOTOOLCHAIN=local will compile with.
version_ok() {
  [[ "$1" =~ ^[0-9]+(\.[0-9]+){0,2}$ ]]
}

parse_version() {
  local tag="${1%%@*}"
  tag="${tag%%-*}"
  if ! version_ok "${tag}"; then
    return 1
  fi
  printf '%s\n' "${tag}"
}

version_less() {
  local a1 a2 a3 b1 b2 b3
  IFS=. read -r a1 a2 a3 <<<"$1"
  IFS=. read -r b1 b2 b3 <<<"$2"
  a1="${a1:-0}"
  a2="${a2:-0}"
  a3="${a3:-0}"
  b1="${b1:-0}"
  b2="${b2:-0}"
  b3="${b3:-0}"
  if ((10#$a1 < 10#$b1)); then
    return 0
  fi
  if ((10#$a1 > 10#$b1)); then
    return 1
  fi
  if ((10#$a2 < 10#$b2)); then
    return 0
  fi
  if ((10#$a2 > 10#$b2)); then
    return 1
  fi
  if ((10#$a3 < 10#$b3)); then
    return 0
  fi
  return 1
}

required_version() {
  local line key value toolchain="" gover=""
  while IFS= read -r line || [[ -n "${line}" ]]; do
    line="${line%$'\r'}"
    line="${line#"${line%%[![:space:]]*}"}"
    line="${line%%#*}"
    line="${line%"${line##*[![:space:]]}"}"
    [[ -z "${line}" ]] && continue
    read -r key value _ <<<"${line}"
    case "${key}" in
      toolchain)
        if [[ -z "${toolchain}" ]]; then
          toolchain="${value#go}"
        fi
        ;;
      go)
        if [[ -z "${gover}" && "${value}" =~ ^[0-9] ]]; then
          gover="${value}"
        fi
        ;;
    esac
  done <"$1"
  if [[ -n "${toolchain}" ]]; then
    printf '%s\n' "${toolchain}"
    return 0
  fi
  if [[ -n "${gover}" ]]; then
    printf '%s\n' "${gover}"
    return 0
  fi
  echo "go-toolchain-match: ${1} has no toolchain or go version" >&2
  return 1
}

if ! required="$(required_version "${gomod}")"; then
  exit 2
fi
if ! version_ok "${required}"; then
  echo "go-toolchain-match: unparseable go.mod version ${required}" >&2
  exit 2
fi

echo "go-toolchain-match: require >= ${required}"

fail=0
stages=0

check_file() {
  local file="$1"
  local rel n=0 line trimmed head rest image image_lc ver
  rel="${file#"${root}/"}"
  while IFS= read -r line || [[ -n "${line}" ]]; do
    n=$((n + 1))
    line="${line%$'\r'}"
    trimmed="${line#"${line%%[![:space:]]*}"}"
    [[ -z "${trimmed}" || "${trimmed}" == \#* ]] && continue
    head="${trimmed%%[[:space:]]*}"
    head="$(printf '%s' "${head}" | tr '[:upper:]' '[:lower:]')"
    [[ "${head}" == "from" ]] || continue
    rest="${trimmed#[Ff][Rr][Oo][Mm]}"
    rest="${rest#"${rest%%[![:space:]]*}"}"
    while [[ "${rest}" == --* ]]; do
      if [[ "${rest}" != *[[:space:]]* ]]; then
        rest=""
        break
      fi
      rest="${rest#*[[:space:]]}"
      rest="${rest#"${rest%%[![:space:]]*}"}"
    done
    [[ -z "${rest}" ]] && continue
    image="${rest%%[[:space:]]*}"
    image_lc="$(printf '%s' "${image}" | tr '[:upper:]' '[:lower:]')"
    [[ "${image_lc}" == golang:* ]] || continue
    stages=$((stages + 1))
    # latest, a build-arg, or any non-numeric tag does not name the stdlib
    # GOTOOLCHAIN=local will compile with.
    if ! ver="$(parse_version "${image_lc#golang:}")"; then
      echo "go-toolchain-match: FAIL ${rel}:${n}: unparseable golang image ${image}" >&2
      fail=1
      continue
    fi
    if version_less "${ver}" "${required}"; then
      echo "go-toolchain-match: FAIL ${rel}:${n}: ${image} (${ver}) is older than ${required}" >&2
      fail=1
      continue
    fi
    echo "go-toolchain-match: ${rel}:${n}: ${image} (${ver}) >= ${required}"
  done <"${file}"
}

list="$(mktemp)"
trap 'rm -f "${list}"' EXIT
if ! (
  cd "${root}"
  find . \( -path './.git' -o -path '*/.work' \) -prune \
    -o \( -name 'Dockerfile' -o -name '*.Dockerfile' \) -print0 >"${list}"
); then
  echo "go-toolchain-match: find failed under ${root}" >&2
  exit 2
fi

while IFS= read -r -d '' file; do
  check_file "${root}/${file#./}"
done <"${list}"

if [[ "${stages}" -eq 0 ]]; then
  echo "go-toolchain-match: no FROM golang stages"
fi
if [[ "${fail}" -ne 0 ]]; then
  exit 1
fi
echo "go-toolchain-match: PASS"
