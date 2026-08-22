# ADR-0057: Privileged ASCP changes use durable dual-control workflows

## Status

Accepted for the pre-alpha control-plane implementation.

## Decision

Privileged payout, signer-cap, verifier, production-gate, break-glass, role,
module-governance, and directory-cancellation changes are represented by a
24-hour proposal whose only mutable reference is a canonical 32-byte payload
hash. The proposal and approval must be made by different human principals.
Both requests require an authentication ceremony recorded no more than five
minutes earlier and a credential that is still valid.

Kind-specific roles are enforced in the service; agents, read-only Sites
sessions, and unrelated human roles cannot use the workflow endpoints. The
database enforces tenant scope, immutable proposal identity, legal transitions,
and append-only action, event, and outbox records. Idempotency is scoped to the
organization, actor, action, and key.

`PRODUCTION_GATE` and `ROLE_ADMIN` become effective after the second approval.
All chain-backed kinds stop at `APPROVED_PENDING_CHAIN`. They become `APPROVED`
only through the non-HTTP completion method after an independent verifier binds
a finalized receipt to the exact workflow and payload hash. The public API has
no endpoint that can assert completion.

## Consequences

- Existing credentials do not gain workflow authority automatically. A
  workflow-capable credential needs an explicit governance role plus
  `step_up_at` and `step_up_until` values from a real step-up ceremony.
- Runtime PostgreSQL privileges allow only the reviewed transition columns and
  append-only inserts.
- The API runtime currently supplies no chain-completion verifier, so chain
  workflows fail closed in `APPROVED_PENDING_CHAIN` until the independent
  observer runtime is configured in a later slice.
- Approval is not proof that an external chain mutation happened.
