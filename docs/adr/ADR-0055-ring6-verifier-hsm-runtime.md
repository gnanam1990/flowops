# ADR-0055: Ring 6 verifier and HSM runtime boundary

Status: accepted

## Context

The isolated signer called a strict `ring6` Unix dependency, but that
dependency was only a deployment contract. FlowOps had no runnable process that
durably bound one operation/action to one logical signing input, distinguished
permanent evidence refusal from transient failure, or made HSM retries converge
on one provider operation.

## Decision

- Add `cmd/ascp-ring6-runtime` as a Unix-only
  `ASCP_SIGNER_DEPENDENCY_V1` service. It never accepts a private key, raw key
  material, TCP traffic, redirects, or proxy configuration.
- Pin key ID, key epoch, keeper ID, and expected signer address. Validate the
  complete activation input and recompute its domain-separated hash.
- Call independent verifier and HSM components over different private `0600`
  Unix sockets using `ASCP_RING6_COMPONENT_V1`. Require exact health identities
  and distinct paths and device/inode/change-time identities.
- Persist `BOUND`, `HSM_REQUESTED`, `SIGNED`, and `REFUSED` in a process-locked,
  append-only, fsynced, hash-chained `0600` journal. Bind operation plus action
  to the full input hash, digest, key ID/epoch, and deterministic HSM
  idempotency key. Record `HSM_REQUESTED` before crossing the HSM boundary.
- Require the HSM to return the requested digest's signature and a canonical
  operation handle. Recover the signature locally and require the pinned signer
  before recording `SIGNED`.
- Persist `REFUSED` only for exact verifier HTTP `422` with a canonical code.
  Transport errors, `5xx`, malformed responses, HSM ambiguity, binding errors,
  cancellation, and timeouts remain nonterminal. Once `HSM_REQUESTED` is
  durable, no later verifier response can terminalize the action as refused.
- Pin each component socket's startup device/inode/change-time identity for its
  process lifetime with a hidden hard link that prevents immediate inode reuse.
  Validate the source against that pin before a call and after establishing
  every new connection, and dial the immutable pin path itself. This closes
  check-before-connect and rapid source-path ABA replacement races.
  Remove only the pinned inode on graceful shutdown; preserve a stale or
  substituted pin for operator inspection.
- Reapply intake freshness while an action is new or merely `BOUND`. Exact
  `HSM_REQUESTED`/`SIGNED` recovery skips repeated verification and may replay
  the same provider operation until `ValidUntil`.
- Refuse existing listener paths and insecure or symlinked ancestors. Bound all
  JSON to 2 MiB and reject duplicate keys, unknown fields, trailing values,
  excessive nesting, wrong content type, and protocol/health substitution.
  Create the listener at a private same-directory name and atomically hard-link
  it to the final name only after mode and identity validation; this avoids
  platform-specific socket-FD inode assumptions during publication.
  On shutdown, remove only the exact socket identity created by this process;
  preserve a replacement path and return the mismatch as a runtime error.

## Consequences

Ring 6 orchestration, crash recovery, exact action binding, and HSM retry
idempotency are runnable and testable. Production still requires separately
reviewed verifier and HSM provider components. This change does not claim
independent Base RPC reads, hardware key custody, or provider audit evidence.
