# ADR-0065: Nonce-bound direct-payment recovery

Status: Accepted

## Decision

FlowOps recovers missing direct-USDC transactions only after two independent
Base providers bind the customer sender, EOA nonce, zero native value, native
USDC contract, and exact `transfer(recipient, amount)` calldata at the same
historical checkpoint. The checkpoint must already be at least the configured
reorg lookback behind the trusted observer head.

After identity binding, the observers may classify the original hash as still
pending, canonically displaced without a governed-asset transfer, replaced by
the exact expected transfer, or replaced by an unknown governed-asset transfer.

Every terminal classification first enters durable quarantine. An operator may
run only the closed `PROBE` and `FINALIZE_PROVEN` actions. The former requests
independent observer evidence; the latter releases a proved drop or reconciles
an exact replacement receipt. Neither action can settle an unknown transfer,
accept operator-supplied chain evidence, select an arbitrary transaction, or
request a rebroadcast. Repeating the same action after response loss is
idempotent.

This recovery applies only to customer-signer-attested direct-USDC executions.
x402 facilitator transactions have a different sender and calldata model and
remain on their protocol-aware receipt path.

## Evidence and failure behavior

The journal retains the initial nonce/content identity, scan start, terminal
quorum digest, canonical through-block, account nonce, replacement identity,
and operator actor. Provider disagreement, a stale pending object whose nonce
is already consumed, missing initial identity, an excessive scan window,
non-canonical transfer logs, insufficient reorg depth, or an unknown transfer
leaves funds reserved and the outcome unresolved or quarantined.

The worker never submits a transaction. It records probe, identity-bind, and
automatic-quarantine counts in each cycle. The operator API uses the dedicated
operator key and tenant-binds organization and execution under one engine lock.

## Consequences

- A dropped expected payment can release budget without pretending a reverted
  receipt exists.
- An exact fee-bumped replacement can settle under its actual transaction hash.
- Unknown governed-asset transfers remain visible and cannot be relabelled as
  intended spend.
- Old broadcasts whose nonce/content identity was never observed remain
  unproven; an operator cannot manufacture the missing evidence.
- Recovery scans are bounded to 50,000 blocks and production RPC admission must
  support historical transaction, account-nonce, and log reads.
