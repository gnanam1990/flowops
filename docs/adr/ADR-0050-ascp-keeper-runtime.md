# ADR-0050: Isolated ASCP keeper runtime

- Status: accepted
- Date: 2026-08-22

## Context

ADR-0043 established the durable keeper state machine, but the repository did
not expose a runnable process that connected it to PostgreSQL, leadership
state, signing, sealing, broadcast, and independent chain evidence. Embedding
those capabilities in the control-plane API would combine transaction-writing,
authorization, and evidence-reading authority. An unconstrained adapter could
also leak leases or signer handles, claim work for another EOA or chain, or
turn a lost RPC acknowledgement into an unsafe retry.

## Decision

Run `cmd/ascp-keeper` as a separately supervised, non-listening process pinned
to one keeper ID, one canonical lowercase gas-payer EOA, and either Base
mainnet or Base Sepolia. It claims and observes jobs only when keeper ID, gas
payer, and chain ID all match. PostgreSQL access uses a dedicated LOGIN role
with no memberships or owned objects, read-only leadership access, and only
the reviewed keeper-table mutations.

Seven distinct Unix-domain sockets isolate activated-artifact release,
transaction assembly, independent binding verification, wallet/HSM signing,
KMS sealing, write-only broadcast, and read-only chain evidence. Startup and
every request recheck that each socket is a non-symlink Unix socket owned by
the runtime user or root in a non-world-writable immediate directory. Exact
health identities prevent cross-wiring. Requests use
`ASCP_KEEPER_BOUNDARY_V1`; responses are size bounded, reject unknown and
duplicate JSON fields, and require one JSON value. Jobs sent to sidecars omit
signer handles, lease ownership and tokens, lease expiry, and internal errors.

The chain adapter cannot broadcast and the broadcaster cannot supply nonce,
replacement, outcome, or expiry evidence. Only explicit trusted-sidecar
`REJECTED` and `UNDERPRICED` responses are deterministic; all other failures
become durable ambiguity. Each cycle observes existing broadcast outcomes
before expiry discovery and new relay work. Durable ambiguous, timed-out, and
dead-letter results are counted and do not restart the worker, while an
uncontained database or proof-boundary failure stops the process for supervisor
recovery.

## Consequences

- A worker cannot be poisoned by same-keeper jobs for another EOA or Base
  chain, and runtime claim indexes support those exact filters.
- The process holds no private key, raw-transaction environment variable,
  credential-bearing RPC URL, or public listener. Raw signed bytes cross only
  the wallet, sealer, and broadcaster boundaries and are cleared from local
  buffers after use.
- Deployment requires seven separately reviewed sidecars, secure socket
  ownership, an external supervisor, managed-PostgreSQL role/readiness proof,
  provider-independence evidence, HSM/KMS ceremonies, funded gas drills,
  metrics and alerting. The repository runtime and fixture tests do not prove
  those production controls exist.
