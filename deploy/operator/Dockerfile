# factory-operator.
#
# Build from the repo root:
#
#   docker build -t factory-operator:dev -f deploy/operator/Dockerfile .
#
# Digests match deploy/factoryd/Dockerfile.

FROM golang:1.26.11-alpine@sha256:ae52b5bc6ed045a9ed26ab03dc19327a802a9cc9aa8b27ed57f5c6b60579b0bb AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS="${TARGETOS}" GOARCH="${TARGETARCH}" \
    go build -trimpath -o /factory-operator ./cmd/factory-operator

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab

COPY --from=build /factory-operator /factory-operator

USER 65532:65532

ENTRYPOINT ["/factory-operator"]
