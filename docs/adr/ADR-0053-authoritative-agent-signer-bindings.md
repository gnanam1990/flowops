# ADR-0053: Authoritative per-agent signer bindings

Status: accepted

## Context

FlowOps requires one customer-controlled signer or reference sandbox signer per
agent. The activation boundary previously accepted signer key, epoch, module,
Safe, and keeper routing from the agent. A dedicated credential scope limited
who could submit those values, but did not make the routing authoritative.

## Decision

- Store one current Base-chain signer binding per organization and agent, with
  an immutable version history.
- Only an Owner or Admin human with fresh step-up authentication may create or
  rotate a binding. Agent credentials cannot read or write the admin endpoint.
- Every write requires an optimistic `expectedVersion` and an idempotency key.
  The binding mutation, immutable history, audit event, and idempotency result
  commit in one serializable PostgreSQL transaction. Serialization, deadlock,
  and concurrent uniqueness retries are bounded to three attempts and then
  resolve against a fresh snapshot.
- A rotation is refused while the agent has a nonterminal signer request or a
  bearer-registry entry whose outcome is `LIVE`. Pause is not treated as proof
  that an issued bearer is unusable.
- A signer-key/epoch tuple is permanently single-use per agent. A routing
  change must introduce a tuple absent from immutable history, preventing
  rollback to an old signer epoch.
- The activation service reads the binding using the authenticated
  organization and agent identity. The agent activation request no longer has
  fields for `signerKeyId`, `keyEpoch`, `moduleAddress`, `safeAddress`, or
  `keeperId`; REST rejects them as unknown and the MCP schema does not publish
  them.
- The binding version and route are locked and revalidated inside the same
  serializable transaction that creates `SIGN_REQUESTED`. A rotation racing
  that transaction either observes the new signer request and refuses, or
  commits first and makes the stale activation fail. Stored exact replays use
  their original binding version after a later safe rotation.
- Runtime database readiness includes the three new tables and exact
  least-privilege grants. History and idempotency rows are append-only.

## Consequences

An agent can request signing work but cannot choose another signer, Safe,
module, key epoch, or keeper. Key rotation is deliberately conservative: it
waits until every prior prepared/live bearer is resolved. This registry does
not claim that the configured hardware key or contracts have been attested;
capability verification and the isolated Ring 6 signer remain separate
production gates.
