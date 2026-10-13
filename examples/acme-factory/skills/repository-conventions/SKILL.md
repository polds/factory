---
name: repository-conventions
description: How payments-service expects changes to be made, tested, and described. Use before finishing any change.
---

# Repository conventions

## Before you finish
1. `pnpm lint` and `pnpm test --filter <changed-package>`. Paste the real output.
2. Any user-visible change needs a changeset: `pnpm changeset`.
3. PR title follows Conventional Commits (`fix(payments): …`).
4. Migrations need a down path. No exceptions.

## Layout
- `services/payments/` — the service. HTTP handlers in `http/`, domain logic in
  `domain/`, persistence in `store/`.
- Domain logic must not import from `http/`. If you need a type in both, it
  belongs in `domain/`.

## Recurring failure modes in this repository
These are the problems review finds most often. Check for them before you open
a PR.

- **Mocking the HTTP layer instead of the client.** Hides serialization bugs,
  which is exactly the class of bug this service produces. Test against the
  client with a fake transport.
- **Migrations without a down path.** Blocks rollback during an incident.
- **Money as floating point.** Amounts are integer minor units, always. A `float`
  in a monetary path is a bug even when the test passes.
- **Retries without idempotency keys.** Payment endpoints must be safe to retry;
  a retry without a key is a double charge waiting to happen.
- **Swallowed errors in `store/`.** Return the error. A logged-and-ignored
  database error becomes a silent data-loss bug.

## Logging
Structured, `slog`, no PII, no card data, no tokens. Amounts are fine; account
identifiers are not.
