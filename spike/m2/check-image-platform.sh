#!/usr/bin/env bash
# Build the M1 task image and the generic-openai harness for one OCI platform
# and run a command inside each. A non-contract exit (126, or busybox's 2)
# means the image cannot execute on that platform.
#
#   ./spike/m2/check-image-platform.sh linux/arm64
#   ./spike/m2/check-image-platform.sh linux/amd64
#
# linux/aarch64 and linux/x86_64 are accepted and normalized.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
# Sourced for helpers only. Do not follow: run.sh returns when sourced,
# and shellcheck then treats the rest of this file as unreachable.
# shellcheck disable=SC1091
source "$HERE/run.sh"

raw="${1:-}"
if [[ -z "$raw" ]]; then
  echo "usage: $0 linux/arm64|linux/amd64" >&2
  exit 2
fi
oci="$(normalize_docker_platform "$raw")" || {
  echo "unsupported platform: ${raw}" >&2
  exit 2
}
arch="$(goarch_for_platform "$oci")"
export DOCKER_DEFAULT_PLATFORM="$oci"

task_tag="factory-m1-task:platform-${arch}"
harness_tag="factory-harness:platform-${arch}"
harness_bin="$ROOT/harness/generic-openai/factory-harness"

cleanup() {
  rm -f "$harness_bin"
}
trap cleanup EXIT

echo "building task image for ${oci}"
docker build --platform "$oci" -t "$task_tag" "$ROOT/spike/m1/task-image"
assert_image_platform "$task_tag" "$oci"

uname_file="$(mktemp)"
exec_err="$(mktemp)"
trap 'rm -f "$harness_bin" "$uname_file" "$exec_err"' EXIT
task_code=0
set +e
docker run --rm --platform "$oci" --entrypoint /bin/sh "$task_tag" \
  -c 'uname -m && git --version >/dev/null && go version >/dev/null && command -v gh >/dev/null' \
  >"$uname_file" 2>"$exec_err"
task_code=$?
set -e
got_uname="$(tr -d '[:space:]' <"$uname_file" 2>/dev/null || true)"
want_uname="$(uname_for_platform "$oci")"
if [[ "$task_code" -ne 0 || "$got_uname" != "$want_uname" ]]; then
  echo "task image failed on ${oci} (uname ${got_uname:-empty}, exit ${task_code}, want ${want_uname})" >&2
  if [[ -s "$exec_err" ]]; then
    tail -n 40 "$exec_err" >&2
  fi
  exit 1
fi
echo "task image ${oci} uname ${got_uname}"

echo "building harness for ${oci}"
(cd "$ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH="$arch" \
  go build -o "$harness_bin" ./cmd/factory-harness)
assert_linux_elf "$harness_bin" "$arch"
docker build --platform "$oci" -t "$harness_tag" "$ROOT/harness/generic-openai"
assert_image_platform "$harness_tag" "$oci"

set +e
docker run --rm --platform "$oci" --entrypoint /factory-harness "$harness_tag" \
  >"$uname_file" 2>"$exec_err"
harness_code=$?
set -e
if ! harness_contract_exit "$harness_code"; then
  echo "harness image cannot execute on ${oci} (exit ${harness_code})" >&2
  if [[ -s "$exec_err" ]]; then
    tail -n 40 "$exec_err" >&2
  fi
  exit 1
fi
echo "harness image ${oci} exit ${harness_code}"
