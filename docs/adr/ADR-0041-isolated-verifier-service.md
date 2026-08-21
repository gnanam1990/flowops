# ADR-0041: Isolated evidence verifier and verdict signer

- Status: accepted
- Date: 2026-08-21

## Context

Escrow settlement requires an independently checked, spec-bound verdict. A verifier that trusts a supplied content digest, accepts an ambiguous spec, signs after key revocation, or can broadcast its own transaction collapses the separation between evidence, judgment, signing, and settlement.

## Decision

Implement `internal/ascpverifier` as a transaction-incapable verification and attestation boundary. It strictly decodes a closed v1 `VerificationSpec`, rejects unknown/trailing JSON, canonicalizes the set-like checks and predicates, recomputes its Keccak-256 hash, and requires equality with the full `ExecutionCommitment` before any format or semantic check runs. It independently hashes the captured content, enforces freshness and delivery time, runs a class-specific engine under the spec timeout, and maps only PASS to release and definitive FAIL to early refund. PASS_WITH_NOTES requires a separately injected step-up decision bound to the call, commitment, spec, delivery, evidence, and engine result.

The service prepares the exact ASCP v4 `VerdictAttestation`, rechecks finalized verifier-key activity immediately before signature publication, normalizes and recovers the ECDSA signature, and exposes no RPC writer or transaction broadcaster. The escrow contract remains the execution-time authority for verifier epoch, revocation, nonce, deadline, call state, and recipient.

## Consequences

- A spec substitution cannot run checks or reach the signer.
- Captured bytes, claimed digest, HTTP metadata, capture time, verifier software, commitment, and approval decision are cryptographically separated and bound. A claimed/recomputed digest disagreement is evidence corruption and produces no verdict bearer.
- Exact same-process retries return one signature; conflicting evidence for the same call is rejected; expired or revoked cached bearers are not returned.
- Structured JSON has a built-in exact predicate engine. Document, computation, and media classes require an explicitly configured engine and otherwise fail closed.
- Production deployment still requires an exclusively owned durable nonce allocator, a durable verdict journal/idempotency projection, a finalized verifier-governance reader, an HSM or isolated signer adapter, and authenticated captured-evidence intake. The included memory nonce and cache are test/demonstration components, not multi-instance production state.
- This module does not constitute an independent security audit or production deployment approval.
