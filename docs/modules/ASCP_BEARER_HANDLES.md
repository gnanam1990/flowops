# ASCP two-phase signer activation and bearer registry

Signature publication follows `prepare → activate → release` without placing
signature bytes in the control-plane database or an agent-facing response.

## Trust boundary

- The control plane writes `SIGN_REQUESTED` and its prepare outbox while the
  reservation remains `RESERVED`.
- The isolated signer receives the exact canonical payload and evidence bytes,
  independently revalidates them, creates the signature, AES-GCM encrypts it,
  fsyncs a process-locked 0600 hash-chained ledger in an owner-writable-only
  directory, and returns only an opaque handle.
- One serializable control-plane transaction moves the reservation to
  `AUTHORIZATION_LIVE`, appends the permanent bearer registry entry, records
  the opaque handle, and writes the primary-mirror outbox event.
- The exact registry object must be written create-if-absent to the primary
  WORM mirror. Its domain-separated digest is stored before activation is
  acknowledged to the signer. The secondary mirror remains asynchronous and
  repairable from the signer ledger.
- Only a verified activation proof carrying that primary-mirror digest makes
  the signer handle `ACTIVE`. Only the bound keeper identity can release the
  decrypted bytes. Identical release retries return the same stored signature.

The legacy `encrypted_artifact` control-plane column is constrained to `NULL`
by migration `0010`; the runtime role cannot bypass the database constraint.
Ciphertext belongs only to the customer signer ledger and is retained through
the permanent replay horizon. Normal finalization does not erase it.
Runtime SQL grants limit updates to reviewed lifecycle columns; immutable
payload, evidence, signer binding, registry identity, and outbox payload fields
are not updateable through the runtime role.

This module supplies the durable protocol, signer adapter, and recovery state
machine. It is not yet wired to a production signer RPC, real Ring 6 signing
engine, WORM provider, or public control-plane/MCP route; those adapters remain
explicit integration gates and this document does not claim a live signer.

## Recovery and expiry

`Coordinator.Advance` crosses exactly one external/durable boundary per call.
After any crash it resumes from `SIGN_REQUESTED`, `PREPARED`,
`ACTIVE_PENDING_MIRROR`, or `ACTIVE_MIRRORED`. A mirror or acknowledgment
failure leaves the reservation `AUTHORIZATION_LIVE`; it is never TTL-released.
Exact prepare retries use the full signer-request hash to return the original
durable handle without asking the signing engine or HSM to sign again.

On restart, every ciphertext is AEAD-authenticated against the full immutable
handle. Active/released/terminal records revalidate their activation proof and
expired records re-prove non-activation through the signer-bound verifier. The
signer fails closed if this authoritative verification is unavailable.

A `PREPARED` signer record may expire only after its authorization validity
window and an authoritative proof that the control plane never activated it.
An active bearer can leave the budget only through nonce consumption, a
finalized unused-expiry proof, or finalized on-chain nonce invalidation; pause
alone is not release evidence.

## Verification

```sh
go test -race ./internal/ascpbearer ./internal/controlapi
go vet ./internal/ascpbearer ./internal/controlapi
FLOWOPS_TEST_DATABASE_URL=... go test -race ./internal/controlapi \
  -run TestASCPTwoPhaseSignerActivationNeverStoresArtifactAndMakesReservationLiveAtomically -count=1
```

The tests cover exact-byte independent-verification gating, opaque-handle
non-disclosure, wrong-keeper refusal, exact signature replay, encrypted-ledger
restart, nondeterministic-resign avoidance, hash-chain and ciphertext/state
tamper failures, file and parent-directory permission failures, exact
runtime-column grants, authoritative prepared expiry, wrong WORM digest,
outbox creation, and the
atomic `RESERVED → AUTHORIZATION_LIVE` transition.
