# ADR-0043: Durable ASCP keeper relay

- Status: accepted
- Date: 2026-08-21

## Context

The signer activation protocol withholds bearer bytes until the permanent
registry is active, while settlement accepts only independently verified chain
receipts. A relay is still needed between those boundaries. Treating an RPC
send as a stateless HTTP call would allow duplicate nonces after crashes,
blind retries after lost acknowledgements, signer-channel substitution, and
unreconciled gas spending.

## Decision

Implement `internal/ascpkeeper` as a separately deployed service using its own
capped EOA and least-privilege database role. A durable job binds organization,
operation, action, Base chain, keeper identity, gas payer, target, value,
canonical payload Keccak-256 hash, activation handle, signer and authorization digest.
Only `claimExpired` omits the activated bearer and leadership epoch.

Before wallet use, the service checks the current issuance leadership epoch,
retrieves the artifact through the keeper-bound signer channel, observes the
pending EOA nonce from a fail-closed quorum source, assembles the transaction,
and has an independent action-specific verifier decode and reprove every job
binding. The wallet receives only that verified transaction.

Nonce reservation is serializable and durable before signing. The exact signed
raw transaction is encrypted and stored before `BROADCASTING`. A restart from
`PREPARED` or `BROADCASTING` opens and rebroadcasts the same bytes. Unknown RPC
outcomes become `AMBIGUOUS` and are not automatically claimable. Deterministic
underpricing becomes `TIMED_OUT`; a new transaction can use only the same nonce,
a strictly higher fee, an independently proved replacement-safe chain view,
and at most three bumps. Each attempt records the keeper EOA as `gasPayer`.

The keeper records submission evidence, not settlement truth. An outcome
adapter over the existing settlement/reconciliation quorum advances keeper
attempts through confirmed, finalized, reverted, reorged or timed-out state and
persists its evidence digest. The same observer supplies fresh confirmed chain
time and an evidence digest before the expiry scanner creates deterministic
`claimExpired(bytes32)` jobs.

## Consequences

- Signer artifact retrieval, keeper EOA signing, transaction construction,
  nonce observation, replacement safety, encryption and RPC broadcast remain
  separately injectable trust boundaries with fail-closed interfaces.
- Database crashes cannot cause a second nonce allocation for a reserved job.
- Ambiguous broadcasts never enter the relay scan. The observation worker may
  recover them to `SUBMITTED` only when independent evidence finds the exact
  transaction hash; otherwise an IncidentResponder decision is required.
- Deployment must provision a KMS/HSM-backed keeper wallet, ciphertext sealer,
  independent Base RPC quorum, gas-floor monitor and alerting. These adapters
  cannot be replaced by the in-memory test fixtures.
