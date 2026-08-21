# ASCP event chain and checkpoint module

## Why

This module makes ASCP decision history tamper-evident through a signed external
checkpoint without confusing that proof with chain settlement truth. It closes
the event-integrity and restore-verification contracts in ASCP v3.2 sections
23.2 and 23.4. It does not decide policy, sign spending authorizations,
broadcast transactions, or claim that an off-chain event proves an on-chain
payment.

## Entry

- A trusted domain module calls `PostgresStore.Append` in response to a durable
  business transition.
- The isolated scheduled checkpointer calls `Publisher.Publish` after receiving
  the independently computed classified journal trial-balance hash.
- Every service startup and every restored backup calls `VerifyRecovery` before
  issuance, relay, seller egress, or checkpoint writes are enabled.

## Inputs

An event binds `eventId`, organization, microsecond UTC timestamp, event type,
actor, optional causation ID, mandatory correlation ID, entity references,
canonical payload, and an optional correction target. IDs and strings are
bounded and control characters are refused. Payloads are capped by the database
at 1 MiB and use the restricted RFC 8785 profile: objects, arrays, strings,
booleans, null, and non-negative JavaScript-safe integers. Monetary values and
large chain quantities are strings.

A checkpoint binds the current sequence/hash and a 32-byte hexadecimal journal
trial-balance hash. Writer MAC keys are exactly 32 bytes; checkpoint keys are
Ed25519 private keys. Writer and signing key IDs identify rotation epochs and
are part of the authenticated documents.

## Internal behavior

1. Validate and canonicalize the complete input before opening a transaction.
2. Acquire the global event-stream advisory lock in a serializable transaction.
3. Return the original event for an exact event-ID replay, even after writer-key
   rotation; reject any semantic substitution with `ErrEventConflict`.
4. Read the current head, allocate exactly the next sequence, hash the complete
   event with domain `ASCP_EVENT_V1`, and authenticate the hash with the current
   HMAC writer epoch.
5. Insert once. PostgreSQL update/delete triggers preserve append-only history.
6. To checkpoint, read the head, sign the canonical checkpoint document, put
   exact bytes to the immutable store, monotonically advance the remote head,
   and insert the local checkpoint. Every external step is exact-replay safe.
7. To recover, replay all events and keys, validate the latest checkpoint and
   immutable object, and prove the remote head hash at its exact local sequence.

## Outputs and interfaces

- `Event` is the immutable sequence/hash/MAC record returned on first append and
  exact replay.
- `Head` is the last verified sequence and event hash.
- `Checkpoint` contains the canonical signed document, signature, immutable
  object reference, signing epoch, and local creation timestamp.
- `WORMStore` must reject replacement at an existing reference.
- `RemoteHead` must accept identical replay and reject lower or same-sequence
  conflicting heads.
- `CheckpointStore` is the narrow persistence/recovery contract.

## Authorization and deployment

The control-plane runtime role may `SELECT, INSERT` events and only `SELECT`
checkpoints. The separate checkpointer role may only `SELECT` events and
`SELECT, INSERT` checkpoints. Neither role may update/delete the two tables or
create schema objects. HMAC keys, checkpoint private keys, and immutable-store
credentials are distinct secrets and must not enter logs, events, database
payloads, or the application repository.

The deployment must fence event append and checkpoint publication on the
current leadership epoch, publish at least every five minutes or 10,000 events,
and alert on verification failure, checkpoint age, remote-head lag, WORM
failure, disk/WAL pressure, and key expiry. Correlation/causation identifiers
must propagate through downstream logs, metrics, signer, keeper, seller calls,
receipts, and corrections.

## UI

Auditor and incident views show the last verified local head, last signed
checkpoint, WORM verification time, remote committed head, uncheckpointed tail
length/age, signing and writer key epochs, and the exact failing layer. The UI
must say `tail not externally checkpointed` rather than `audit history verified`
when the local head is ahead of the remote head.

## Failure and recovery

- Unknown writer key, invalid MAC/hash/sequence, or malformed canonical payload:
  freeze controlled effects and page; never skip the event.
- Event-ID input substitution: reject without appending.
- WORM or remote-head failure: do not record local checkpoint completion; retry
  the same signed head idempotently.
- Local store behind/conflicting with remote head: refuse startup and restore
  from a verified backup; never advance the remote head backwards.
- Local tail ahead of the remote head: report the bounded post-checkpoint tail,
  verify its HMAC chain, and checkpoint before declaring the restore fully
  protected.
- Corrections are appended with `supersedesEventId`; no mutation or deletion is
  a recovery mechanism.

## Acceptance criteria

- Exact retry returns one event; changed payload, org, actor, timestamp, refs,
  correlation, causation, or supersedes target rejects.
- Twenty concurrent PostgreSQL writers produce each sequence exactly once and
  the full chain verifies after restart.
- Writer-key rotation preserves old verification and does not mutate replayed
  events; missing or wrong key material fails closed.
- Hash, MAC, previous hash, signature, checkpoint document, WORM bytes, and
  remote-head substitutions are all detected.
- Truncation before or at a remote committed head refuses startup. A later local
  tail is explicitly reported as bounded rather than externally checkpointed.
- Database update/delete attempts against events and checkpoints fail.
- Focused race tests, a real PostgreSQL migration/concurrency test,
  least-privilege readiness checks, and repository-wide checks pass.
- AC-10/AC-44 are covered locally. AC-32 remains incomplete until a real
  encrypted backup, WORM provider, remote-head service, and timed restore drill
  prove RPO/RTO and trial-balance equality.
