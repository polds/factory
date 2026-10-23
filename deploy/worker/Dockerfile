# Image of the unmodified upstream oz-agent-worker. The commit is ARG WORKER_REF.
#
# The worker's Docker backend talks to the daemon with the Engine API
# (github.com/moby/moby/client, client.New(client.FromEnv) in
# internal/worker/docker.go). It does not exec the docker CLI, so this
# image does not install one. Mount /var/run/docker.sock at run time.
# The process stays root so it can open a typical root:docker socket.
#
# Build from the repo root:
#
#   docker build -t factory-upstream-worker:dev deploy/worker
FROM golang:1.26-alpine@sha256:9ce231b28998b32e116927c61aa5c406183061d4f3f2d61b292c88e7f6bdf7ca AS build

# git is required to clone the pinned revision and to stamp vcs metadata.
RUN apk add --no-cache git=2.54.0-r0

WORKDIR /src

# Newest oz-agent-worker commit whose go.mod builds with Go 1.26; upstream main requires go 1.27.3.
ARG WORKER_REF=3ba509a2f4bc2243cbff0a7869c2303c9dcad4e7
# Full clone, then the pinned commit, then a clean-tree check. go build
# records vcs.revision / vcs.modified from this checkout. The binary is
# written outside the tree so the build itself cannot dirty it.
RUN git clone https://github.com/warpdotdev/oz-agent-worker.git . \
    && git checkout "${WORKER_REF}" \
    && git status --porcelain \
    && test -z "$(git status --porcelain)" \
    && CGO_ENABLED=0 go build -buildvcs=true -o /oz-agent-worker .

# ca-certificates for outbound TLS. openssl is pinned to the alpine 3.22
# package that includes the 3.5.8 fixes upstream calls out (CVE-2026-63073,
# CVE-2026-75803). No docker CLI: see the header.
FROM alpine:3.22@sha256:5291449c3df73caf6ed85e649dec1b9e818b39a5d8c871e97afc13e9cd5e8fa8

RUN apk add --no-cache \
    ca-certificates=20261020-r0 \
    openssl=3.5.9-r0

COPY --from=build /oz-agent-worker /oz-agent-worker

ENTRYPOINT ["/oz-agent-worker"]
