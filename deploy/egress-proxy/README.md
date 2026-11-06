# Optional egress proxy sidecar pattern for narrow allowlist routing (synthetic).

When enabled in acceptance configs, tasks talk only to this proxy on `:8443`.
The proxy enforces route tables checked by `deploy/scan-security/probes/egress-matrix.sh`.

This directory does not ship a production proxy image; M10-A wires enforcement scripts.
