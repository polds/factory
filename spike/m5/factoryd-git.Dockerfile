# factoryd plus git, for the read-only definition factory.
# factoryd:dev has no git binary. This image copies that binary onto a
# pinned Alpine that has git, and leaves the checkout directory writable
# by uid 65532 so a fresh volume inherits that owner.
FROM alpine:3.22@sha256:5291449c3df73caf6ed85e649dec1b9e818b39a5d8c871e97afc13e9cd5e8fa8

RUN apk add --no-cache \
    ca-certificates=20260909-r0 \
    git=2.54.0-r0

COPY --from=factoryd:dev /factoryd /factoryd

RUN mkdir -p /var/lib/factory/definition \
    && chown -R 65532:65532 /var/lib/factory

USER 65532:65532
ENTRYPOINT ["/factoryd"]
