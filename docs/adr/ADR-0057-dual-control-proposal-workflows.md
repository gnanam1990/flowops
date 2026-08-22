# ADR-0057: Privileged ASCP changes use durable dual-control workflows

## Status

Accepted for the pre-alpha control-plane implementation.

## Decision

Privileged payout, signer-cap, verifier, production-gate, break-glass, role,
module-governance, and directory-cancellation changes are represented by a
24-hour immutable proposal. Local-only kinds bind a canonical 32-byte payload
hash; chain-backed kinds persist one closed typed action plus its server-derived
payload hash and calldata. The proposal and approval must be made by different human principals.
Both requests require an authentication ceremony recorded no more than five
minutes earlier and a credential that is still valid.

Kind-specific roles are enforced in the service; agents, read-only Sites
sessions, and unrelated human roles cannot use the workflow endpoints. The
database enforces tenant scope, immutable proposal identity, legal transitions,
and append-only action, event, and outbox records. Idempotency is scoped to the
organization, actor, action, and key.

Chain-backed create requests carry a caller-generated nonzero 32-byte
`workflowId` and one closed typed governance action. The service recomputes the
contract-compatible payload hash and exact calldata with that ID; callers may
not supply either value. Before persistence, the configured governance target
gate authorizes the exact Base chain, reviewed contract, workflow kind, and
function selector. An absent gate or arbitrary target fails before any proposal
or outbox row exists. Approval repeats the target gate and cryptographically
rebinds the persisted JSONB action to the exact payload and calldata before the
execution command may be written.
`PRODUCTION_GATE` and `ROLE_ADMIN` are local dual-control workflows and reach
`FINALIZED` atomically with the second approval. All chain-backed kinds stop at
`APPROVED_PENDING_CHAIN`; the same approval transaction writes the immutable,
versioned `ascp.governance.execute` outbox command containing the target,
value-zero call, selector, calldata, action, payload hash, approval identity,
and an `executeAfter` chain-time floor one second after approval.
Internal relayer and reconciler
boundaries advance `SUBMITTED` and `CONFIRMED`. Only an independent canonical
receipt quorum reaches `FINALIZED`. Explicit `REVERTED`, `REORGED`, `TIMED_OUT`,
and `REQUIRES_REAPPROVAL` side states use closed reason enums. The public API
has no endpoint that can assert any chain outcome.

## Consequences

- Existing credentials do not gain workflow authority automatically. A
  workflow-capable credential needs an explicit governance role plus
  `step_up_at` and `step_up_until` values from a real step-up ceremony.
- Runtime PostgreSQL privileges allow only the reviewed transition columns and
  append-only inserts.
- Exact idempotent replays return the recorded outcome after the original
  step-up window expires; fresh mutations still require a live step-up.
- An absent or incomplete observer configuration rejects creation of new
  chain-backed workflows; it is not merely a completion-time failure. Recovery
  may reconstruct missing submitted and confirmed transitions from one
  finalized canonical receipt, in the same database transaction.
- Migration 0029 cannot safely reconstruct typed action bytes for pre-upgrade
  live rows. It expires unapproved legacy rows and moves approved rows to
  `REQUIRES_REAPPROVAL`, with immutable migration action/event/outbox evidence.
- The PRD's broader affected-policy/directory authorship deny-overrides rule
  still requires an authoritative authorship registry. This slice enforces
  proposer/approver separation but does not claim that wider gate complete.
- Approval is not proof that an external chain mutation happened.
