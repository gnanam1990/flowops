# ADR-0049: Durable isolated verifier runtime

- Status: accepted
- Date: 2026-08-21

## Context

The verifier core could evaluate and sign exact ASCP verdicts, but its nonce and
decision cache were process memory. Restart or replica races could evaluate the
same call twice, and there was no authenticated delivery-intake runtime or
finalized verifier-governance adapter. A transaction-capable control-plane
process must not absorb this key boundary.

## Decision

Run `cmd/ascp-verifier` as a separate loopback-only process. It pins one Base
chain, one escrow contract, one verifier address and one verifier epoch. Intake
uses a 32-byte HMAC key epoch, a canonical Unix timestamp, a unique nonce and a
SHA-256 body digest under `ASCP_VERIFIER_INTAKE_V1`. The nonce is durably
inserted before JSON processing; duplicate keys, trailing JSON, unknown fields,
stale requests, invalid MACs, alternate chains and alternate escrows fail
closed.

PostgreSQL allocates verdict nonces from a non-rollback sequence and stores one
append-only signed decision per call. A call-scoped advisory transaction spans
the bounded engine and signer call, so replicas cannot concurrently publish
different decisions. Replays revalidate the stored attestation digest and
signature before return. The finalized key gate reads the latest append-only
observer record and requires an active, non-future, fresh observation both
before evaluation and immediately before signing.

The runtime has no RPC writer, keeper key or transaction broadcaster. Its role
can insert verdicts and replay nonces, read finalized key observations, and use
the nonce sequence; it cannot update/delete evidence or read unrelated tables.

## Consequences

- Process restart and replica retry return the same durable signature; input
  substitution for an existing call is a conflict.
- A crash after an external signer produces bytes but before PostgreSQL commit
  can ask the signer again. Production HSM integration must therefore expose an
  idempotent operation handle bound to the attestation digest. The included
  private-file adapter re-reads and verifies the key on every signature and is
  for local/test deployment only.
- The finalized observation writer, HSM custody, mTLS/service-mesh transport if
  the verifier is moved off-host, restore drills, metrics export and keeper
  submission remain explicit production gates.
