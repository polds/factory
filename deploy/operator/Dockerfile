# factory-operator.
#
# Build from the repo root:
#
#   docker build -t factory-operator:dev -f deploy/operator/Dockerfile .
#
# Digests match deploy/factoryd/Dockerfile.

FROM golang:1.26-alpine@sha256:9ce231b28998b32e116927c61aa5c406183061d4f3f2d61b292c88e7f6bdf7ca AS build

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
