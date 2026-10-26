# Task image for the M1 real-agent run.
#
# The unmodified worker starts this image as the task container. The agent
# clones a repository, edits Go, runs tests, and opens a pull request from
# inside it, so the image provides git, Go 1.26, and the GitHub CLI.
#
# The worker may start the container as an arbitrary uid. Writable state lives
# under /tmp so git and go work without a home directory owned by that uid.
# /workspace is the task cwd; mode 1777 lets that uid clone a repo into it.
#
# Build:
#
#   docker build -t factory-m1-task:dev spike/m1/task-image
FROM golang:1.26-alpine@sha256:9ce231b28998b32e116927c61aa5c406183061d4f3f2d61b292c88e7f6bdf7ca

RUN apk add --no-cache \
    bash=5.3.9-r1 \
    ca-certificates=20261020-r0 \
    curl=8.23.0-r0 \
    git=2.54.0-r0 \
    github-cli=2.97.0-r2 \
    && mkdir -p /workspace \
    && chmod 1777 /workspace

ENV HOME=/tmp \
    GOCACHE=/tmp/gocache \
    GOPATH=/tmp/go \
    GIT_CONFIG_GLOBAL=/tmp/.gitconfig

WORKDIR /workspace
