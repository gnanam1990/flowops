# Authenticated control-plane API module

Status: local implementation complete; deployment identity and PostgreSQL
operations remain gated

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
2. FlowOps hashes the token and loads its organization-bound principal.
3. The route checks human role or agent scope. Approval and pause also require
   an unexpired server-side step-up claim.
4. The API creates or retrieves a durable organization-scoped command.
5. New commands invoke the existing lifecycle, policy, freeze, and chain gates.
6. The exact result or typed failure is recorded before it is returned.

The client never supplies an authoritative organization header. An agent
credential can act only for its bound agent, and an intent's customer identity
must equal that agent's registered customer signer. Cross-tenant or
cross-customer identifiers are reported as not found.

## Endpoints

| Method | Route | Permission | Result |
|---|---|---|---|
| `GET` | `/health` | public, non-sensitive | Control-plane and Base authorization state |
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
checksums are immutable. Domain event replay refuses malformed, reordered,
substituted, or externally advanced event streams.

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

## Verification

```sh
go test -race ./internal/controlapi ./internal/controlplane ./cmd/control-plane-api
go vet ./internal/controlapi ./internal/controlplane ./cmd/control-plane-api
make check
```

The tests cover authentication, unknown-field rejection, scope escalation,
tenant isolation, organization-scoped idempotency, exact approval digest,
step-up enforcement, pause propagation, Base halt rejection, durable failure
replay, PostgreSQL tenant predicates, audited pause transactions, JSON command
results, and concurrent-writer journal refusal.

## Remaining live gates

- Provision a managed PostgreSQL instance with encryption, backups, point-in-
  time recovery, monitoring, connection TLS, and a least-privilege database
  role.
- Implement the owner/bootstrap and credential-rotation workflow; do not seed a
  production tenant by manual database editing.
- Connect the independent Base observer service and complete the recovery
  stability window before issuing any authorization.
- Store the FlowOps envelope key in an approved secret manager. This is a
  FlowOps capability-signing key, never a customer wallet key.
- Add the server-side Sites identity-to-membership session exchange before the
  dashboard can leave preview mode.

## Runtime configuration

The executable requires `FLOWOPS_DATABASE_URL`, `FLOWOPS_ENVELOPE_KEY_ID`,
`FLOWOPS_ENVELOPE_PRIVATE_KEY_B64`, and
`FLOWOPS_RECONCILIATION_JOURNAL`. `FLOWOPS_CONTROL_ADDR` is optional and
defaults to loopback. Policy limits, Base chain/USDC rules, rails, recipients,
and versions are loaded from the active PostgreSQL policy row for the governed
agent; they are not shared process-wide environment variables.
