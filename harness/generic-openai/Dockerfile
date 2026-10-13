# Sidecar image for the generic-openai harness.
#
# The worker exports this image's entire root filesystem and extracts it with
# `tar`, using this image with the entrypoint overridden. The image therefore
# has to contain `tar`. busybox provides it. The harness binary is static
# (CGO_ENABLED=0) so it runs in the task image, including Alpine.
#
# Build from the repo root:
#
#   CGO_ENABLED=0 go build -o harness/generic-openai/factory-harness ./cmd/factory-harness
#   docker build -t factory-harness:dev harness/generic-openai
FROM busybox:1.36

COPY factory-harness /factory-harness
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh /factory-harness
ENTRYPOINT ["/entrypoint.sh"]
