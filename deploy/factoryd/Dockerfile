# factoryd control plane.
#
# Task containers are attached to the host Docker bridge, not the compose
# network, so they cannot reach this process by the compose service name.
# FACTORY_TASK_SERVER_URL is the base URL those containers use for event
# push. It defaults to http://172.17.0.1:8080, the Linux bridge gateway,
# with factoryd's port published on the host. Override it where that
# gateway differs (Docker Desktop uses http://host.docker.internal:8080).
#
# Build from the repo root:
#
#   docker build -t factoryd:dev -f deploy/factoryd/Dockerfile .
#
# The node stage installs from the lockfile before the UI source is copied,
# so a source-only change does not re-run npm ci. The digest is the
# multi-arch index (linux/amd64 and linux/arm64):
#   amd64 sha256:2c752226d477b4a886378baa95b9af252be59301b725fdb0b7e15208131505a8
#   arm64 sha256:85cdd100016a2e09927c0776bb7ebebcfefce5cd0af49893ada91cbc4d78ee22

FROM node:22-alpine@sha256:0a7108bf6c7bf5de370ffb1a3ed6be93d405b43ff159f681a8d18c0e2bc2e402 AS console

WORKDIR /src/web/console

COPY web/console/package.json web/console/package-lock.json ./
RUN npm ci --no-audit --no-fund && npm cache clean --force

COPY web/console/index.html web/console/vite.config.ts web/console/tsconfig.json ./
COPY web/console/src ./src
COPY web/console/scripts ./scripts
RUN npm run build

FROM golang:1.26-alpine@sha256:9ce231b28998b32e116927c61aa5c406183061d4f3f2d61b292c88e7f6bdf7ca AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
COPY --from=console /src/internal/console/dist ./internal/console/dist

ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS="${TARGETOS}" GOARCH="${TARGETARCH}" \
    go build -trimpath -tags console -o /factoryd ./cmd/factoryd

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab

COPY --from=build /factoryd /factoryd

USER 65532:65532

ENTRYPOINT ["/factoryd"]
