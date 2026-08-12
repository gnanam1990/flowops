# ADR-0014: Continuous Receipt and Finality Reconciliation

Status: Accepted for capped Base Sepolia pilot
Date: 2026-08-12

## Context

The reconciliation engine could validate receipt and reorg evidence, but no
production process continuously applied those primitives to durable broadcast
records. A generic retry loop would be unsafe: an absent receipt cannot prove a
failure, an RPC success cannot prove canonical settlement, and a crash must not
create a second payment or ledger posting.

## Decision

The control-plane process runs one sequential reconciliation worker against the
same independently configured Base observer set and hash-chained journal as the
chain-health supervisor.

For each journaled `BROADCAST` or `PENDING_CHAIN_RECOVERY` execution, the worker:

1. obtains provider-specific receipt evidence under a bounded timeout;
2. leaves the execution unresolved on missing, partial, stale, conflicting, or
   invalid evidence;
3. derives a deterministic direct-USDC settlement identifier from the execution
   and canonical block hash;
4. asks the engine to validate the full quorum and atomically persist the
   execution plus balanced ledger transaction; and
5. records a canonical revert without a ledger transaction.

The worker never signs, broadcasts, replaces, or retries an onchain transaction.
Only the cryptographically bound customer-signer receipt registrar may add
already-submitted expected transaction data. It uses the halt-safe attested
registration path defined by ADR-0015; the worker itself still cannot create an
execution.

For each settled execution, the worker waits until the last trusted checkpoint
is at least the configured reorg-lookback depth beyond the settlement. It then
queries the canonical block hash at the original height. Quorum agreement on the
original hash is journaled as a positive finality check and ends routine polling.
Quorum agreement on a different hash atomically appends an exact ledger reversal,
reopens the execution, and moves chain state to `RECOVERING`.

The positive checkpoint uses the existing `execution_resolved` journal event
envelope with additive execution fields. This is deliberate rollback
compatibility: the prior binary ignores those fields but can still replay and
preserve the settled execution instead of refusing the journal.

## Failure posture

- Provider absence or disagreement is deferred, not converted to a payment
  failure.
- After a canonical reorg reversal, receipt evidence at the exact removed block
  number and hash is rejected even if providers repeat it; only a fresh receipt
  outcome can settle the reopened execution.
- A stale or halted chain prevents finalization.
- Per-execution RPC work is bounded and cycles never overlap.
- Journal, idempotency, or ledger invariant failures stop the worker and fail the
  service rather than silently skipping economic state.
- Detailed provider errors and credential-bearing URLs are not logged by the
  worker.
- Reorganizations deeper than the configured positive-finality checkpoint are a
  residual pilot risk and require an operator incident workflow.

## Consequences

The capped pilot now has an end-to-end durable reconciliation coordinator for
already registered direct-USDC broadcasts. It now accepts signer-attested
transaction handles, but it does not yet have the customer-side one-way wallet
executor, escrow event decoding, transaction replacement/dropped handling, or
dashboard exception controls. Those are separate modules and must not be
inferred from an idle no-funds worker deployment.

## Acceptance evidence

- canonical receipt settlement posts exactly once;
- canonical reverts post no ledger state;
- missing, timed-out, or disputed evidence remains unresolved;
- positive finality survives restart and is not polled again;
- a canonical reorg appends an exact reversal and reopens the execution;
- a halted/stale chain performs no receipt or finality queries; and
- the full race-enabled reconciliation and control-plane suites pass.
