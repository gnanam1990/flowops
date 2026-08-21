# ADR-0044: ASCP event chain and recovery checkpoints

Status: accepted for the local reference implementation

## Context

The existing component journals protect individual state machines but do not
provide the organization-wide event contract required by ASCP v3.2 sections
23.2 and 23.4. A database row alone cannot prove its position in history, a
plain hash chain cannot authenticate the current writer, and a local chain
cannot detect deletion of its uncheckpointed tail after a restore.

## Decision

Add `internal/ascpevents` and migration `0014`. Every accepted event receives a
single global sequence under a PostgreSQL transaction-scoped advisory lock.
The event commits to its organization, timestamp, type, actor, correlation and
causation identifiers, entity references, canonical payload, correction link,
previous hash, and writer-key identifier. SHA-256 provides the chain hash and a
separate 32-byte HMAC key authenticates the writer. Key rotation is explicit;
verification requires the retained public key registry for every referenced
writer-key epoch.

Payloads use the restricted RFC 8785 profile already used by the payment wire
contracts: I-JSON values and non-negative safe integers only. Financial and
chain quantities remain decimal strings. A repeated event ID returns the
original row only when every semantic input is identical. Rotation of the
current writer key does not rewrite a historical replay.

The checkpointer uses a separate database role and Ed25519 key. It signs the
canonical `{schemaVersion,lastSeq,lastEventHash,journalTrialBalanceHash,
signingKeyId}` document, writes the exact document and signature to an
idempotent immutable object, advances an independent monotonic remote head,
then records the checkpoint locally. Retries reuse the already recorded
checkpoint for the same head. Restore verification replays the complete
hash/MAC chain, verifies the signature and immutable bytes, and proves every
remote committed head exists at the same position in the local chain.

## Consequences

- Mutating or deleting an event/checkpoint is rejected by database triggers.
- Content mutation, field substitution, unknown writer epochs, signature
  substitution, immutable-object replacement, checkpoint mismatch, and local
  truncation behind the remote head fail closed.
- Corrections append a new event with `supersedesEventId`; they never edit
  history.
- A post-checkpoint tail remains only HMAC-, host-, and backup-protected. This
  bounded guarantee is reported honestly; it is not equivalent to a committed
  remote head.
- Production still requires an HSM/KMS Ed25519 adapter, isolated HMAC key
  lifecycle, two-provider immutable-object policy, remote monotonic-head
  service, scheduled verifier, alerting, WAL shipping, and restore drills.
  The local in-memory test adapters do not satisfy those deployment gates.
