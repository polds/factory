# Public scanner runner. The build context is the repository root.
# It copies the stub plugin only. It does not clone or download a private scanner.
FROM golang:1.26.11 AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY deploy/scanner-runner/stub ./deploy/scanner-runner/stub
COPY deploy/scanner-runner/pin/provenance /out/provenance
RUN go build -trimpath -o /out/scanner-stub ./deploy/scanner-runner/stub

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/scanner-stub /scanner
COPY --from=build /out/provenance /etc/scanner/provenance
USER 65532:65532
ENTRYPOINT ["/scanner"]
