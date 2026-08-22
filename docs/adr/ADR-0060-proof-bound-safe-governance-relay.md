# ADR-0060: Governance retries bind the unchanged Safe transaction

## Status

Accepted for the pre-alpha governance relay implementation.

## Context

A governance workflow approval binds an inner contract call, but it does not
prove a current Safe nonce, current owner threshold, collected owner
signatures, or an outer gas-payer transaction. Treating a timeout or reorg as
permission to construct a new action can execute stale authority or duplicate
an economic effect.

## Decision

Build the deterministic Safe transaction only from the approved command and a
fresh independent Safe snapshot. Verify the exact EIP-712 threshold signatures
and Safe exec calldata in-process, seal the executable bytes behind an
authenticated vault boundary, and durably record the outer transaction before
broadcast.

Expose a read-only signing-request command before signature intake. It returns
the exact EIP-712 Safe transaction hash, transaction fields, current sorted
owner set and threshold, payload binding, and quorum evidence without loading
the vault capability or connecting to the broadcaster. Signature intake
re-observes the same bindings and fails if they drifted.

Automatic retry is allowed only for a dropped or reorged non-canonical outer
transaction when immediately refreshed quorum evidence proves that the Safe nonce, approved
precondition payload hash, Safe transaction hash, and exec-calldata hash are
unchanged. Persist an append-only proof and require the workflow database
trigger to join it to the durable relay job before returning a side-state
workflow to `SUBMITTED`.

## Consequences

- Control-plane approval and Safe owner signatures remain distinct authority
  ceremonies.
- The relayer admits only the PRD's exact three-owner, threshold-two governance
  Safe topology; structurally valid weaker or alternate topologies fail closed.
- A gas payer may re-wrap the same signed Safe transaction only through the
  exact proof-bound path.
- Safe nonce, owner set/threshold, or precondition drift requires a new
  workflow and owner signatures.
- The durable relay job permits at most ten submitted outer attempts for one
  owner-signed Safe transaction. Retry exhaustion transitions to reapproval
  without preparing attempt eleven. This cap is enforced both in the worker
  and the database constraint so a crash or worker replacement cannot reset it.
- Mined revert cannot be treated as a transient RPC error.
- Ten submitted outer attempts exhaust the signed artifact and require a new
  approval and owner ceremony.
- The relayer's finalized observation is not product finality; the independent
  receipt observer retains that authority.
- A late finalized action event from any recorded Safe attempt can recover a
  timed-out/reorged workflow; an unrecorded transaction cannot claim a side state.
  The workflow's primary transaction hash converges to the canonical winning
  receipt even when a later replacement was already submitted or confirmed.
- Only canonical EOA EIP-712 owner signatures are admitted in this version.
  Other Safe signature modes remain unsupported.
