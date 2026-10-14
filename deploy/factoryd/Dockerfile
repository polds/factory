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
FROM golang:1.26-alpine@sha256:3082400e369fa24d5fc60bca20edab3f6d604e0c5a690ec66b295eff4dd87ade AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /factoryd ./cmd/factoryd

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab

COPY --from=build /factoryd /factoryd

USER 65532:65532

ENTRYPOINT ["/factoryd"]
