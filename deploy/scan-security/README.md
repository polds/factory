# Synthetic G14/G18 enforcement matrices (disposable Docker + policy K8s)

These assets support **offline** CI and verifier runs. They do not attest the live acceptance backend.

## Docker host

Set optional env vars when running `preflight.sh`:

- `SCAN_SECURITY_MATRIX` — path to a `key=value` matrix file (see `matrices/g14-default.env`)
- `SCAN_SECURITY_SAMPLE_TASK` — pod/job manifest checked for forbidden mounts

Forbidden in sample scanner tasks: host Docker socket, writable foreign checkouts, broad credential mounts.

## Kubernetes

Use `scripts/ci/egress-kind-smoke.sh` with `scanSecurity.enabled=true` to render NetworkPolicy and verify a probe pod cannot reach `169.254.169.254`.

Helm render and kind success **do not** replace Docker-host firewall attestation (M10-G).

## Residual risk

Name allowlists can still exfiltrate via an allowed gateway route if policy is too broad. Keep routes narrow; `paidProbeAllowed` stays false until M10-G.
