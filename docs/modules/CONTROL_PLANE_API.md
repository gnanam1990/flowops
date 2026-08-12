# Authenticated control-plane API module

Status: production packaging and audited owner bootstrap implemented; live
infrastructure verification remains gated

Packages: `internal/controlapi`, `internal/controlplane`
Executable: `cmd/control-plane-api`

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

## Endpoints

| Method | Route | Permission | Result |
|---|---|---|---|
| `GET` | `/health` | public, non-sensitive | Control-plane and Base authorization state |
| `POST` | `/v1/sites/session` | Sites server exchange credential plus exact membership | Short-lived organization-bound read session |
| `POST` | `/v1/intents` | agent scope `intents:create` or authorized human | Durable command plus policy record |
| `POST` | `/v1/intents/{requestID}/authorization` | agent scope `authorizations:issue` or authorized human | Exact signed FlowOps authorization envelope |
| `GET` | `/v1/approvals` | human organization member | Tenant-filtered pending approvals |
| `POST` | `/v1/approvals/{requestID}/decision` | Approver, Finance, Admin, or Owner plus step-up | Exact-digest approval decision |
| `POST` | `/v1/agents/{agentID}/pause` | Admin or Owner plus step-up | Durable pause and audit record |
| `GET` | `/v1/commands/{commandID}` | organization member; agents see only their commands | Authoritative command outcome |
| `GET` | `/v1/dashboard/snapshot` | human organization member | Live tenant-scoped agents, approvals, and chain state |

## Persistence

Migration `0001_control_plane.sql` creates organizations, governed agents,
per-agent versioned policies, hashed credentials, durable commands, append-only
audit events, and the hash-chained control event stream. Migrations and control
events each use a PostgreSQL advisory transaction lock. Applied migration
checksums are immutable. Hash-chained payloads are stored as `bytea`, because
the chain commits to exact JSON bytes and `jsonb` normalization would change
them during replay. Domain event replay refuses malformed, reordered,
substituted, or externally advanced event streams.

Migration `0002_sites_memberships.sql` adds project-specific exchange-token
digests and Sites identity memberships. It stores only a site-bound user hash
and normalized email digest. Session authentication compares every signed claim
to the current ACTIVE row so revocation takes effect immediately.

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

`cmd/flowops-admin` provides strict-stdin, transactionally audited owner
bootstrap and exchange-token rotation. The container posture, deployment
variables, enrollment sequence, smoke checks, rotation, and rollback procedure
are defined in `docs/operations/CONTROL_PLANE_DEPLOYMENT.md`.
