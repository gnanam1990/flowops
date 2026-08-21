# ASCP settlement and accounting module

## Why

Convert an activated spend authorization into durable, independently observed
Base payment state without allowing a keeper, signer, API caller, or database
writer to declare its own financial outcome.

## Entry points

- Signer activation creates `ascp_payment_operations` in `AUTH_SIGNED`.
- `POST /v1/ascp/settlement-attempts` registers a keeper transaction identity
  under the dedicated callback credential.
- The runtime settlement worker polls pending receipts and finalized canonical
  blocks using the configured independent RPC observer set.

## Inputs

The durable operation supplies organization, agent, authorization, reservation,
bearer, commitment, call ID, chain, escrow, asset, buyer, payee, amount and
settlement deadline. A keeper supplies only action and transaction hash;
`RELEASE` additionally requires delivery and evidence hashes.

## Internal behavior

1. Serialize attempt registration and enforce one lock plus one mutually
   exclusive terminal action.
2. Decode the exact ASCPCallEscrow lifecycle event and exact USDC Transfer from
   every provider receipt.
3. Require distinct-provider agreement and safe/finalized confirmation depth.
4. Atomically persist observation, attempt, operation, bearer, reservation and
   balanced ledger changes.
5. Check finalized block canonicality; append exact reversals and conservative
   recovery states when a reorg is proved.

## Outputs and interfaces

The callback returns `SUBMITTED` plus replay status. Runtime logs expose cycle
counts for pending/applied/deferred/canonical/reorg work. PostgreSQL exposes the
classified operation, attempt, append-only observation and append-only ledger
tables to readiness-checked runtime privileges.

## Failure states

- Missing quorum, provider disagreement or insufficient depth: deferred with no
  state or accounting mutation.
- Reverted receipt: `PENDING_CHAIN_RECOVERY`; reservation is not released.
- Terminal reorg: terminal posting reversed, reservation returns to
  `COMMITTED_FINALIZED`, automatic retry blocked.
- Lock reorg: every classified posting for the operation is reversed and the
  reservation becomes `REORGED_BACK`.
- Binding mismatch, duplicate conflicting attempt or finality regression:
  transaction fails closed.

## Acceptance criteria

- Contract ABI topic vectors and exact lock/release/refund receipt fixtures pass.
- Substitution, duplicate, removed-log, wrong-order and keeper outcome-injection
  tests fail closed.
- Real PostgreSQL proves safe-to-final transitions, balanced postings, exact
  reversals, idempotent replay and concurrent release/refund exclusion.
- Signer activation and payment-operation creation commit atomically.
- Runtime role has only required table/column privileges; financial history is
  append-only.
- Focused tests, race tests, full repository checks and adversarial PR review
  pass before merge.
