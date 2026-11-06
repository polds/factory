# Security deployment (G14 / G18 foundations)

This document covers **pilot controls and Helm additions** from M10-S. It does not certify the live acceptance backend. `paidProbeAllowed` remains false until M10-G attests the actual Docker host or cluster.

## Secret resolver (G18)

`internal/secrets` implements `Resolver.Resolve(ctx, name, injectionID)` with:

- Name allowlist (fail closed with `secret_ref_rejected`)
- Mounted-file lookup on **every** injection (no in-process cache)
- `FACTORY_SECRET_*` env compatibility via `cmd/factoryd` (`secretsFrom`)
- Rejection of missing refs, path escape, symlinks, and unsafe permissions

Kubernetes opt-in: enable `controlPlane.secretProjection` in the factory Helm chart for projected secret files at `FACTORY_SECRET_MOUNT_DIR`. Propagation delay applies; **subPath is not rotation**. Running task processes keep credentials they already hold until a new injection.

Production wiring at assignment time is **M9-H** (`assign.go` is not modified in M10-S).

## Redaction

`secrets.Redactor` retains **both** previous and current values during rotation and applies the same matchers as harness output paths (fmt/JSON/slog/MCP-shaped events). Synthetic canaries: `CANARY-OLD`, `CANARY-NEW`, `CANARY-SYNTHETIC` (names only in docs; tests use sanitized values).

## Network enforcement (G14 synthetic)

`deploy/scan-security/**` provides disposable Docker preflight and Kubernetes policy probes:

```bash
./scripts/ci/scan-security-check.sh
FACTORY_EGRESS_KIND_SKIP=1 ./scripts/ci/egress-kind-smoke.sh
```

Default posture in `deploy/scan-security/matrices/g14-default.env`: deny by default; narrow gateway/forge/control-plane routes; deny direct-IP bypass, DNS/CONNECT tricks, metadata (`169.254.169.254`), and cross-engagement traffic. Denials attribute to trusted task identity in probe logs (never secret values).

Helm opt-in: `scanSecurity.enabled`, `scanSecurity.egress.networkPolicy`, and `scanSecurity.egress.requireCNI`. The policy lives in the task namespace and selects `factory.dev/scan-task` (stamped by the worker `extra_labels`). Egress is kube-dns plus the egress-proxy pods in the release namespace on the configured proxy port. Named forge and control-plane hosts are listed on the route ConfigMap (`api.github.com`, control plane). There is no `0.0.0.0/0` rule. These renders are **not** host-firewall or live-cluster CNI attestation, and the smoke script does not run a probe pod.

## Residual allowlist exfiltration risk

Even with deny-by-default egress, an allowlisted HTTP route can carry data if the route is too broad. Restrict gateway and forge targets to required endpoints. Restricted credentials: use read-only forge tokens scoped to the pinned repository; never mount all factory secrets into scanner tasks.

## Gateway allocation (INV-GATEWAY)

`secrets.FailClosedAllocator` is a testable stub. Per-engagement gateway caps are **GAP** until M10-G proves allocation on the real gateway. The stub does not set `guaranteesCeiling: true`.

## INV-GATEWAY / paid probe gate

```text
paidProbeAllowed = false   # until M10-G on the actual backend
```

Helm render, kind smoke, and synthetic PASS lines in CI are development signals only—not approval to run paid scanner probes.
