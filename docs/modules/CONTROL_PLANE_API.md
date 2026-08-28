# Authenticated control-plane API module

Status: production packaging and audited owner bootstrap implemented; live
infrastructure verification remains gated

Packages: `internal/controlapi`, `internal/controlplane`
Executable: `cmd/control-plane-api`

Privileged ASCP governance changes use the durable dual-control endpoints under
`/v1/workflows`; see [ASCP_PROPOSAL_WORKFLOWS.md](ASCP_PROPOSAL_WORKFLOWS.md).
These routes accept human governance credentials only, require a fresh recorded
step-up for mutations, and expose no chain-completion endpoint.

## Purpose

This module is the authenticated write boundary between agents, human
operators, and FlowOps' canonical Go domain modules. It makes policy decisions,
exact approvals, authorization issuance, pause state, and chain degradation
available through durable commands without giving the dashboard or an agent a
signing key.

## Entry flow

1. A client sends a bearer credential and, for writes, an `Idempotency-Key`.
2. FlowOps hashes the token and loads its organization-bound principal. Agent
   credentials stop authenticating when their agent is revoked or archived.
3. The route checks human role or agent scope. Approval and pause also require
   an unexpired server-side step-up claim.
4. The API creates or retrieves a durable organization-scoped command.
5. New commands invoke the existing lifecycle, policy, freeze, and chain gates.
6. The exact result or typed failure is recorded with a bounded server-owned
   completion context before it is returned, even if the client disconnects.

Sites dashboard reads use a separate entry flow. `POST /v1/sites/session`
matches a project credential and exact provisioned identity to an ACTIVE
organization membership, then issues a two-minute signed session. Each read
revalidates that membership. The session is marked read-only, has no step-up
claim, and cannot reach any economic write route.

The client never supplies an authoritative organization header. An agent
credential can act only for its bound agent, and an intent's customer identity
must equal that agent's registered customer signer. Cross-tenant or
cross-customer identifiers are reported as not found.

Customer signer broadcast intake is a third, non-browser entry flow. It uses a
customer-key-signed receipt rather than a bearer credential. The server scopes
the public key to organization, customer, and key ID, then derives all economic
facts from the already-issued authorization before creating reconciliation
state. It never accepts a raw transaction or wallet key.

Escrow registration is a fourth entry flow. A still-valid issued escrow
authorization already contains the exact signed call terms. The registration
route accepts only its authorization ID and derives the durable intent from the
control-plane journal. Transition registration accepts action-specific dynamic
fields and an already-broadcast transaction hash from a step-up-authenticated
Owner or Admin; it cannot sign or submit a transaction. Tenant and economic
fields are never accepted as caller authority. The exact reviewed deployment
contract, asset, and immutable release window are runtime admission settings.

## Endpoints

| Method | Route | Permission | Result |
|---|---|---|---|
| `GET` | `/health` | public, non-sensitive | Control-plane and Base authorization state |
| `POST` | `/v1/sites/session` | Sites server exchange credential plus exact membership | Short-lived organization-bound read session |
| `GET` | `/v1/session` | authenticated bearer credential | Safe principal, role, read-only, and step-up-expiry claims |
| `POST` | `/v1/intents` | agent scope `intents:create` or authorized human | Durable command plus policy record |
| `POST` | `/v1/intents/{requestID}/authorization` | agent scope `authorizations:issue` or authorized human | Exact signed FlowOps authorization envelope |
| `GET` | `/v1/approvals` | human organization member | Tenant-filtered pending approvals |
| `POST` | `/v1/approvals/{requestID}/decision` | Approver, Finance, Admin, or Owner plus step-up | Exact-digest approval decision |
| `POST` | `/v1/agents/{agentID}/pause` | Admin or Owner plus step-up | Durable pause and audit record |
| `POST` | `/v1/organization/pause` | Admin or Owner plus step-up | Persistent organization authorization stop plus command and audit IDs |
| `GET` | `/v1/commands/{commandID}` | organization member; agents see only their commands | Authoritative command outcome |
| `GET` | `/v1/dashboard/snapshot` | human organization member | Live tenant-scoped agents, approvals, chain state, reconciliation exceptions, progress, and proved-asset aggregates |
| `POST` | `/v1/signer/broadcasts` | customer signer receipt signature | Authorization-bound expected execution awaiting Base reconciliation |
| `POST` | `/v1/signer/escrow-broadcasts` | customer signer receipt signature | Attested exact escrow FUND awaiting Base reconciliation |
| `POST` | `/v1/x402/authorizations/{authorizationID}/settlements` | own-agent `x402:settlements` scope or authorized human | Facilitator result registered as an untrusted transaction candidate awaiting canonical Base proof |
| `POST` | `/v1/escrow/intents/{authorizationID}` | own-agent `escrow:register` scope or authorized human | Authorization-derived durable escrow intent before broadcast |
| `POST` | `/v1/escrow/calls/{callID}/transitions` | Owner/Admin with active step-up | Durable non-FUND candidate for one already-broadcast escrow transition |
| `GET` | `/v1/escrow/calls/{callID}` | organization read permission; agents only their own call | Tenant-scoped canonical escrow timeline |
| `POST` | `/v1/operator/chain/halt` | dedicated operator-control key | Durable manual Base halt |
| `POST` | `/v1/operator/chain/resume` | dedicated operator-control key plus recovery readiness | Durable manual autonomous-execution release |
| `GET` | `/v1/operator/reconciliation?organizationId=...` | dedicated operator-control key | Tenant-selected reconciliation read model for incident response |
| `POST` | `/v1/operator/executions/{executionID}/quarantine` | dedicated operator-control key, exact organization, named operator, unproven disposition | Durable containment without asserting drop/replacement or moving funds |

Signer broadcast intake has rail-specific endpoints for `direct_usdc` and
escrow FUND. An x402 facilitator result uses its authenticated, tenant-scoped
endpoint instead of masquerading as a direct transfer. The result must carry a
tenant-scoped customer-signer Ed25519 receipt and match the exact issued x402
authorization, canonical payer, Base network, transaction hash, and amount, but
remains only a candidate until independent receipt observers prove the exact
native-USDC transfer. Escrow uses its separate strict event decoder and
transition worker; none of these registries broadcast.

## Persistence

Migration `0001_control_plane.sql` creates organizations, governed agents,
per-agent versioned policies, hashed credentials, durable commands, append-only
audit events, and the hash-chained control event stream. Migrations and control
events each use a PostgreSQL advisory transaction lock. Applied migration
checksums are immutable. Hash-chained payloads are stored as `bytea`, because
the chain commits to exact JSON bytes and `jsonb` normalization would change
them during replay. Domain event replay refuses malformed, reordered,
substituted, or externally advanced event streams.
Canonical direct/x402 outcomes remain owned by the rollback-compatible
reconciliation journal. Budget evaluation reads only executions with a durable
reorg-lookback finality checkpoint: `SETTLED` moves the amount from reservation
to spent accounting and `REVERTED` releases it. Missing or malformed projection
state, missing durable signer evidence, or any organization/task/chain/asset/
recipient/amount mismatch remains reserved. No new control event is written, so rolling back the API
image preserves a conservative reservation instead of encountering an unknown
control-journal event.

Migration `0002_sites_memberships.sql` adds project-specific exchange-token
digests and Sites identity memberships. It stores only a site-bound user hash
and normalized email digest. Session authentication compares every signed claim
to the current ACTIVE row so revocation takes effect immediately.

Migration `0004_organization_authorization_pause.sql` adds the persistent
organization authorization gate. Authorization issuance holds a shared lock on
the organization row before it locks the agent row; organization pause takes an
exclusive organization lock. A pause therefore waits for already-running
issuance to finish and every later issuance fails closed before its agent lock.

Authorization issuance holds the governed agent row lock from the final ACTIVE
check through the durable authorization append. A concurrent pause therefore
orders before issuance and blocks it, or orders after the append; it cannot
commit between the check and issuance. Pause is permitted only from ACTIVE (or
as an idempotent PAUSED result), never from DRAFT, QUARANTINED, REVOKED, or
ARCHIVED.

Expired approvals are swept at startup, every 30 seconds, and before relevant
API reads and new intent reservations. A sweep failure terminates the service
or fails the request closed instead of leaving stale budget reservations in
the live view.

## Failure states

- Missing, expired, or revoked credential: generic `UNAUTHENTICATED`.
- Missing scope or role: `FORBIDDEN` or `SCOPE_REQUIRED`.
- Foreign tenant identifier: `NOT_FOUND`, regardless of existence.
- Reused key with different canonical input: `IDEMPOTENCY_CONFLICT`.
- Wrong or expired approval digest: durable conflict or expiry result.
- Paused agent: `AGENT_FROZEN`; no authorization is issued.
- Halted, recovering, or stale Base observers: `CHAIN_UNAVAILABLE`; the command
  remains durable and no automatic rebroadcast occurs.
- Domain write confirmed but command result unavailable: unresolved command;
  the caller polls by command ID rather than resubmitting payment work.
- Another control-plane writer advanced the event stream: process faults closed
  with `ErrJournalStale` and must replay from PostgreSQL before serving writes.
- Sites project, user, email, exchange credential, membership, role, or session
  substitution: generic `UNAUTHENTICATED`; no organization existence is leaked.
- Unknown signer receipt key or bad signature: `INVALID_SIGNER_RECEIPT`.
- Receipt/authorization, tenant, customer, time, execution, or transaction-hash
  substitution: `BROADCAST_BINDING_REJECTED`; no reconciliation state changes.
- No configured customer signer public keys:
  `SIGNER_BROADCASTS_UNAVAILABLE`; the endpoint is fail-closed.
- Missing, expired, cross-tenant, non-escrow, or altered escrow authorization:
  `NOT_FOUND` or `STATE_CONFLICT`; no durable call is created.
- Reordered action, reused transaction hash, or altered dynamic transition:
  durable conflict; the worker retains the prior canonical state.

## Verification

```sh
go test -race ./internal/controlapi ./internal/controlplane ./cmd/control-plane-api
go vet ./internal/controlapi ./internal/controlplane ./cmd/control-plane-api
make check
```

The tests cover authentication and revoked-agent credential rejection,
unknown-field rejection, scope escalation,
tenant isolation, organization-scoped idempotency, exact approval digest,
step-up enforcement, expiry sweeping, pause/issuance serialization, invalid
pause transitions, Base halt rejection, client-cancellation-safe command
completion, durable failure replay, PostgreSQL tenant predicates, exact-byte
event replay, audited pause transactions, JSON command results, and
concurrent-writer journal refusal. Sites tests additionally cover project/user/
email substitution, session tampering and expiry, membership revocation,
organization-bound snapshot reads, and absence of step-up authority.
Reconciliation read-model tests additionally cover asset binding, excluded
unclassified postings, exact tenant isolation, exception progress, dedicated
operator authentication, and refusal to represent a dropped or replacement
transaction as proved without canonical evidence.
Signer receipt tests additionally cover every signed-field mutation, key and
tenant scoping, exact authorization derivation, future/expired timestamps,
halted-chain intake, idempotency conflict, and restart replay.
Escrow registration tests additionally cover exact signed call-term binding,
authorization expiry, cross-tenant non-disclosure, transaction-hash
idempotency, strict action order, halt retention, restart replay, canonical
ledger posting, and cascading reorg correction.

## Remaining live gates

- Provision a managed PostgreSQL instance with encryption, backups, point-in-
  time recovery, monitoring, connection TLS, and a least-privilege database
  role.
- Connect the independent Base observer service and complete the recovery
  stability window before issuing any authorization.
- Store the FlowOps envelope key in an approved secret manager. This is a
  FlowOps capability-signing key, never a customer wallet key.
- Run the owner-mediated Sites bootstrap and exchange-token rotation workflow
  against the selected production database; operators must not hand-edit rows.

## Runtime configuration

The executable requires `FLOWOPS_DATABASE_URL`, `FLOWOPS_ENVELOPE_KEY_ID`,
`FLOWOPS_ENVELOPE_PRIVATE_KEY_B64`, `FLOWOPS_SITE_SESSION_KEY_B64`, and
`FLOWOPS_RECONCILIATION_JOURNAL`. `FLOWOPS_CONTROL_ADDR` is optional and
defaults to `127.0.0.1:8080`. An injected `PORT` selects `0.0.0.0:PORT` only
when `FLOWOPS_TRUST_PROXY_HEADERS=true`; protected routes then require an HTTPS
forwarded-protocol claim. This mode is valid only behind the exclusive edge
defined by ADR-0012. Policy
limits, Base chain/USDC rules, rails, recipients,
and versions are loaded from the active PostgreSQL policy row for the governed
agent; they are not shared process-wide environment variables.
`FLOWOPS_SITE_SESSION_KEY_B64` must encode exactly 32 random bytes and must be
stored separately from the FlowOps envelope key.

`FLOWOPS_ASCP_KEEPER_CALLBACK_KEY_B64` is a separate 32-byte credential for
`POST /v1/ascp/settlement-attempts`. The request may bind only an operation,
action, transaction hash, and release-only delivery/evidence hashes. It has no
field or authority for success, finality, release, refund, or ledger state;
those outcomes require independent Base receipt quorum.

`GET /livez`, `GET /readyz`, and `GET /health` have intentionally different
contracts: process liveness, bounded PostgreSQL readiness, and public product /
chain health respectively. `GET /metrics` exists only when a distinct 32-byte
`FLOWOPS_METRICS_KEY_B64` is configured and accepts that credential alone. Base
mainnet startup requires it. Metrics use bounded route/status labels and never
emit tenant, principal, path-value, address, digest, or token labels.

`FLOWOPS_SIGNER_RECEIPT_KEYS_JSON` is optional. When present it is a strict
array of `organizationId`, `customerId`, `keyId`, and `publicKeyB64`; the last
field must contain exactly one 32-byte Ed25519 public key. Private-key-shaped
material, missing/unknown/duplicate fields, and duplicate scoped identities are
rejected at startup. When absent, signer broadcast intake remains unavailable.

`cmd/flowops-admin` provides strict-stdin, transactionally audited owner
bootstrap and exchange-token rotation. The container posture, deployment
variables, enrollment sequence, smoke checks, rotation, and rollback procedure
are defined in `docs/operations/CONTROL_PLANE_DEPLOYMENT.md`.
