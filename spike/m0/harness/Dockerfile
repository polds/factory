# Stub harness sidecar for the M0 spike.
#
# The worker materializes a sidecar by exporting this image's root filesystem
# into a volume and mounting it at /agent in the task container. So this image's
# `/` becomes `/agent`, and `/entrypoint.sh` becomes `/agent/entrypoint.sh` —
# which is the path the Docker backend execs.
#
# Two non-obvious constraints, both observed in the worker's source:
#   1. The image must contain `tar`: the worker extracts the exported
#      filesystem *using this same image* with its entrypoint overridden.
#   2. Only linux/amd64 and linux/arm64 are supported.
#
# A real harness ships a binary here instead of a shell script, and must be
# glibc-linked. A shell script has no such constraint, which is why the stub
# can sit on busybox.
FROM busybox:1.36

COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh
