#!/bin/sh
set -eu
cd "$(dirname "$0")"
docker build -q -t factory-m1-task:dev . >/dev/null
docker run --rm --user "$(id -u):$(id -g)" factory-m1-task:dev sh -c \
  'git --version && go version && gh --version | head -1 && bash --version | head -1 && test "$(pwd)" = /workspace'
echo "task image smoke: ok"
