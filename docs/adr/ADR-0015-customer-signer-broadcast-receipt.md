# ADR-0015: Customer-Signer Broadcast Receipt

Status: Accepted for capped Base Sepolia pilot
Date: 2026-08-12

## Context

FlowOps issues an exact authorization before the customer signer uses its own
wallet. The reconciliation worker needs a transaction hash, expected payer, and
the authorization-derived transfer facts, but FlowOps must not receive a wallet
private key or invent that a transaction was broadcast. A network timeout can
also occur after a node has accepted the transaction, so retrying the wallet
operation is unsafe.

The existing `RegisterBroadcast` method correctly refuses a new locally
initiated broadcast when Base is unhealthy. It is not correct for a callback
that arrives after the customer may already have submitted: Base can halt
between submission and callback, and rejecting that callback would discard the
only durable handle for reconciliation.

## Decision

The customer signer signs a domain-separated `flowops.broadcast-receipt.v1`
receipt with a dedicated Ed25519 attestation key. The receipt binds:

- organization, customer, and authorization identifiers;
- the digest of the exact issued authorization;
- Base transaction hash and expected sender;
- `SUBMITTED` or `AMBIGUOUS` outcome; and
- the signer's broadcast timestamp and attestation key ID.

The attestation key is not the wallet key. FlowOps configures only the public
key, scoped to the exact organization, customer, and key ID. The receipt never
contains a raw transaction, wallet credential, RPC URL, or arbitrary expected
transfer data.

`POST /v1/signer/broadcasts` uses the signed receipt as its authentication. The
control plane resolves the authorization from its own durable lifecycle,
recomputes its digest, validates that the signer timestamp is inside the
authorization window, and derives organization, agent, task, intent digest,
chain, asset, recipient, and amount from that record. Only the signed
transaction hash and sender come from the customer signer.

One authorization maps to one deterministic execution ID. Registering the
same authorization and hash is idempotent; rebinding the authorization or hash
is a conflict. A valid callback is journaled even if Base is currently stale or
halted. In that case it enters `PENDING_CHAIN_RECOVERY` and no settlement is
recognized until the existing independent receipt quorum confirms it.

The journal preserves the exact authorization, signed receipt, and public key
that verified it. The reconciliation engine recomputes the authorization
digest, re-verifies the receipt proof, checks the authorization window, and
matches its organization, agent, task, chain, asset, recipient, and amount to
the expected execution before appending. This makes the attestation
independently auditable after restart or key rotation and prevents a future
internal caller from bypassing the receipt boundary with substituted economic
fields. The stored public key is historical evidence, not current authority to
accept another callback.

The fields are additive to the existing execution event schema. A rollback
binary can replay them by ignoring the unknown proof, and the newer replay path
restores the proof from the original broadcast event if that legacy binary
later appends a resolution without it. This prevents a rollback-and-roll-forward
cycle from erasing the attestation.

The customer-side transaction executor remains responsible for durably
entering a one-way broadcast state before network I/O. Any result after the RPC
call begins is either submitted or ambiguous and must produce this receipt;
neither outcome authorizes a second broadcast. That executor and concrete
EOA/HSM adapters are a separate module.

## Trust and rotation

- The customer signer attests that its configured wallet adapter produced the
  stated hash and sender. FlowOps independently verifies the eventual Base
  receipt and exact USDC transfer before posting ledger state.
- Public keys are configured through a strict, public-key-only JSON array.
  Removing a key requires a service configuration rollout. Already journaled
  executions remain valid; callbacks not yet delivered under the removed key
  fail closed.
- Multiple key IDs permit planned overlap, but no private attestation or wallet
  key is accepted by the FlowOps configuration grammar.
- A valid receipt does not prove settlement, finality, delivery, or service
  quality. It creates only an expected execution awaiting reconciliation.

## Failure posture

- Unknown key and bad signature are unauthenticated.
- Cross-tenant, cross-customer, authorization-digest, timestamp, execution, or
  transaction-hash substitution is refused before reconciliation state changes.
- Unknown JSON fields, duplicate configuration fields, non-canonical hashes,
  and non-canonical addresses fail closed.
- Journal failure is unresolved and retriable at the callback layer; it never
  instructs the wallet to rebroadcast.
- With no customer public keys configured, the endpoint returns
  `SIGNER_BROADCASTS_UNAVAILABLE` and the no-funds control plane remains safe.

## Acceptance evidence

- signed receipt round-trip plus mutation rejection for every bound field;
- tenant/customer/key scoping and authorization-digest substitution tests;
- authorization-window and future-clock tests;
- deterministic expected-execution derivation from the durable authorization;
- hash/authorization idempotency conflicts;
- halted-chain registration directly into `PENDING_CHAIN_RECOVERY`; and
- restart replay without a second execution or ledger posting.
