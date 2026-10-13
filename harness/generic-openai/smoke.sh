#!/bin/sh
# Build the sidecar image and run one skip-initial-turn invocation.
# Exits 0 only when the container writes the contract's terminal event.
set -eu
cd "$(dirname "$0")/../.."
CGO_ENABLED=0 go build -o harness/generic-openai/factory-harness ./cmd/factory-harness
docker build -q -t factory-harness:dev harness/generic-openai >/dev/null
ws=$(mktemp -d)
rt=$(mktemp -d)
trap 'rm -rf "$ws" "$rt"' EXIT
docker run --rm --user "$(id -u):$(id -g)" \
  -v "$ws:/workspace" \
  -v "$rt:/var/run/factory" \
  -w /workspace \
  -e FACTORY_RUNTIME_DIR=/var/run/factory \
  -e OPENAI_BASE_URL=http://127.0.0.1:9 \
  factory-harness:dev \
  agent run --task-id image-smoke --model m --skip-initial-turn --idle-on-complete 0s
grep -F '"summary":"skipped initial turn"' "$rt/events.ndjson" >/dev/null
echo "image smoke: skipped initial turn"
