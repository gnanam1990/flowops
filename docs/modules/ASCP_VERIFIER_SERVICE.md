# ASCP verifier service

## Why and entry

`internal/ascpverifier` converts a full ASCP v4 execution commitment, the exact raw `VerificationSpec`, and captured delivery bytes into a narrowly scoped release or early-refund attestation. The entry point is `Service.VerifyAndSign`. The package cannot create intents, decide spend policy, move funds, call an RPC writer, or submit an escrow transaction.

## Inputs

- Full `executioncommitment.Commitment`, including escrow, chain, verification-spec hash, delivery deadline, and settlement deadline.
- Raw verification-spec JSON. Unknown fields, trailing values, unsupported versions/classes/checks, duplicate set entries, missing format-floor checks, invalid limits, and a notes policy other than step-up approval fail closed.
- Captured delivery `{reference bytes, content bytes, claimed SHA-256 digest, HTTP status, content type, capturedAt}`.
- Operator-injected class engines, finalized verifier-key gate, verdict nonce source, isolated digest signer, and optional notes authorizer.

## Internal behavior

1. Strictly parse and canonicalize the spec; recompute its Keccak-256 hash and compare it to the commitment before checks.
2. Validate the complete commitment and derive its EIP-712 digest plus `callId`.
3. Recompute SHA-256 over the captured bytes; bind reference, recomputed digest, HTTP metadata, and capture time into `deliveryHash`.
4. Require non-future, fresh capture time. A captured delivery after `deliverBy` is a definitive FAIL; stale or future observations produce no bearer.
5. Treat claimed/recomputed content-digest disagreement as evidence corruption and produce no bearer. Run status, non-empty, type, byte-bound, and optional exact SHA-256 checks; a definitive format failure does not invoke the semantic engine.
6. Run the configured class engine under the spec timeout. The built-in structured-data engine accepts only self-contained `captured-delivery` references, a top-level JSON object, exact zero tolerance, and exact `json-equals` predicates.
7. Map PASS to release and FAIL to early refund. PASS_WITH_NOTES cannot sign until the external step-up authorizer returns an exact release/refund decision.
8. Recheck finalized verifier-key activity, allocate a uint256 nonce, cap the bearer at 15 minutes and `settleBy`, construct the exact contract attestation, sign its ASCP v4 EIP-712 digest, recover the configured signer, and return the evidence record plus signature.

## Outputs and interfaces

`SignedDecision` contains verdict, contract outcome, reason code, notes, optional approval decision ID, canonical spec, spec/commitment/delivery/evidence hashes, exact attestation, attestation digest, recovered signer, and 65-byte Solidity-compatible signature. `vectors/verdict-attestation-v1.json` is shared by Go and Solidity tests.

The signer interface accepts only a 32-byte digest and returns a signature. The verifier-key gate is read-only. The notes authorizer receives immutable review bindings. Class engines receive only the canonical typed spec and captured delivery.

## Failure and recovery

- Spec mismatch: no check, nonce allocation, or signature.
- Digest-integrity conflict: no attestation. Definitive status/content/semantic failure: refund attestation only.
- Missing engine, timeout, malformed engine output, future/stale capture, unavailable key gate, inactive/revoked verifier, bad nonce source, wrong signer recovery, or missing notes approval: no attestation.
- Same-process exact retry: identical cached signature while still valid and key-active.
- Different delivery for an already decided call: conflict; no second signature.
- Expired or revoked cached bearer: withheld. The on-chain escrow independently rechecks every binding and retains `claimExpired` as the non-bypassable recovery path.

## Durable runtime

`cmd/ascp-verifier` now supplies a loopback-only authenticated runtime. It pins
one Base chain and escrow, consumes HMAC-authenticated captured-delivery bodies
with durable replay nonces, allocates PostgreSQL verdict nonces, serializes one
append-only decision per call across replicas, revalidates stored signatures,
and reads only fresh finalized verifier-key observations. Migrations `0020` and
`0021` plus `configure-verifier-role.sql` enforce the least-privilege DB
contract. Verdict
decisions and finalized key observations are permanently append-only; replay
nonces are immutable for 24 hours and then pruned only through one reviewed
routine. The process has no RPC writer or transaction broadcaster.

The included private-file digest signer re-opens and validates its key before
every signature. It is a local/test adapter, not an HSM claim. Production still
requires an idempotent HSM operation handle, the finalized observation writer
and backfill reconciliation, authenticated seller-worker delivery wiring,
metrics/audit export, restore drills, and keeper submission. Moving the service
off-host additionally requires authenticated encrypted transport. No code in
this module broadcasts a transaction.

## Acceptance criteria

- Spec substitution fails before the engine and signer.
- Content-digest mismatch and future/stale capture produce no bearer; late delivery can only produce refund.
- PASS produces release; FAIL produces refund; PASS_WITH_NOTES requires a separately bound decision.
- Revocation and expiry prevent cached bearer publication.
- Concurrent identical calls invoke engine and signer once; conflicting evidence for one call fails.
- All attestation fields and EIP-712 domain mutations change the digest; Go, Solidity, and the published vector agree.
- Strict JSON engine, malformed signer output, unsupported operator, unknown spec fields, weak specs, and retry paths have negative tests.
- Focused race/vet/Forge tests, full repository checks, and adversarial PR review pass before merge.
