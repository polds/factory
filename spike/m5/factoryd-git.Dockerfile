# factoryd plus git, for the read-only definition factory.
# factoryd:dev has no git binary. This image copies that binary onto the
# same Alpine the worker build uses, which is the one that ships
# git=2.54.0-r0 and ca-certificates=20260909-r0. The checkout directory
# is writable by uid 65532 so a fresh volume inherits that owner.
FROM golang:1.26-alpine@sha256:3082400e369fa24d5fc60bca20edab3f6d604e0c5a690ec66b295eff4dd87ade

RUN apk add --no-cache \
    ca-certificates=20260909-r0 \
    git=2.54.0-r0

COPY --from=factoryd:dev /factoryd /factoryd

RUN mkdir -p /var/lib/factory/definition \
    && chown -R 65532:65532 /var/lib/factory

USER 65532:65532
ENTRYPOINT ["/factoryd"]
