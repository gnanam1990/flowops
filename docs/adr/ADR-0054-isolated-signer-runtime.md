# ADR-0054: Isolated signer runtime and artifact release boundary

Status: accepted

## Context

The bearer worker already used strict Unix clients and the signer package had
a crash-safe encrypted ledger, but no production command served the signer or
keeper artifact protocols. A prepared handle also omitted the control-plane
request identity, the negative proof omitted operation identity, and the replay
index used action ID alone. Those omissions weakened exact activation binding
and allowed unrelated operations with the same action label to collide.

## Decision

- Add `cmd/ascp-signer-runtime` with no TCP listener and no private signing key.
- Serve `ASCP_BEARER_RUNTIME_V1` and the keeper `artifact` subset of
  `ASCP_KEEPER_BOUNDARY_V1` on distinct new `0600` Unix sockets.
- Refuse existing socket paths, any symlinked path component, parents that are
  not owner-only (`0700`) or unowned, shared paths, wrong protocol identities,
  oversized or non-strict JSON, and
  dependency redirects.
- Retain signature ciphertext only in the existing process-locked, fsynced,
  hash-chained signer ledger. Load its AES-GCM key only from a private regular
  file; never from an API request or command argument.
- Pin signer key ID, key epoch, and keeper ID before calling the Ring 6/HSM
  dependency. Recover every returned signature over the requested digest and
  require the configured module-registered signer address.
- Use distinct `ring6` and `activation` Unix dependencies. The former performs
  the independent evidence checks and hardware-backed digest signature. The
  latter independently verifies the exact control-plane activation and primary
  mirror or proves non-activation.
- Bind request, authorization, reservation, operation, and action identity in
  every prepared record and AEAD AAD. Index replay by operation plus action.
  Bind operation ID into the domain-separated unactivated proof. Version this
  expanded ledger record and AAD as format 2; version 1 fails closed and needs
  an explicit drain/migration ceremony.
- Return signature bytes only from the keeper artifact route after durable
  activation, constant-time verification of a separate 32-byte keeper Bearer
  capability, and exact keeper-ledger binding. `keeperId` is not authentication.
  Identical authorized release retries return the same encrypted stored artifact.
- Treat only the exact Ring 6 `SIGNER_REFUSED` code as a permanent
  pre-signature refusal. Atomically mark the request refused, release its
  still-reserved budget, cancel the prepare outbox event, and clear the worker
  lease. Every ambiguous, binding, state, or transport failure remains
  fail-closed and cannot release budget.

## Consequences

The worker-to-signer and keeper-to-artifact protocols are now runnable and
restart-safe without moving a private signing key into FlowOps. Deployment is
still fail-closed until separately reviewed Ring 6/HSM and activation-authority
services are present. This runtime does not claim HSM custody, independent Base
RPC operation, primary WORM retention, or production deployment evidence.
