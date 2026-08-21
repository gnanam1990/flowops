# ADR-0052: Authenticated ASCP activation intake boundary

Status: accepted

## Context

The bearer activation store and worker existed, but only direct Go callers
could create `SIGN_REQUESTED`. Exposing the storage input directly would let a
caller select authorization and reservation identifiers, and returning the
storage record would disclose canonical evidence and the prepared signer
handle. REST and MCP also need identical authorization semantics.

## Decision

Add one application service between authenticated callers and
`ascpbearer.ActivationStore`.

- The agent identity and operation path are passed to the existing scoped
  authorization reader.
- Creation requires the separate `activations:create` credential scope;
  permission to issue an execution authorization alone cannot trigger signing.
- The service accepts signer material only. It derives operation,
  authorization, and reservation identifiers from durable state and generates
  a cryptographically random request identifier.
- The store retains authorization ID as the permanent idempotency key. Exact
  concurrent calls converge to one request; changed material conflicts.
- Only serializable/deadlock database errors receive three bounded retries.
- REST and MCP delegate to the same service. Their response is a redacted
  status projection with no payload, evidence, prepared handle, ciphertext, or
  signature.
- The production MCP request limit and activation REST limit are 2 MiB to
  carry the existing bounded binary fields after JSON base64 expansion.

## Consequences

An agent can ask the isolated signer pipeline to evaluate only its own live
execution authorization. Successful intake proves neither signer acceptance
nor activation: the bearer worker and independent signer still revalidate the
complete bytes. A missing signer-side RPC server, Ring 6 engine, or WORM
provider remains a deployment blocker rather than being hidden by API success.
