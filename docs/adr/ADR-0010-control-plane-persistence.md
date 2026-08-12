# ADR-0010: Control-plane persistence and command boundary

Status: Accepted
Date: 2026-08-12

## Decision

The production control-plane API uses PostgreSQL for organizations, agents,
per-agent versioned policies, hashed credentials, durable command results,
immutable audit records, and the hash-chained authorization event stream. The existing file journals remain
valid deterministic fixtures and local-development stores; they are not the
multi-tenant production source of truth.

The API authenticates a bearer credential by its SHA-256 digest. Plaintext
credentials are never stored. The authenticated credential supplies the
organization, role, agent identity, scopes, and step-up expiry; callers cannot
select a different tenant through a header or body field. Credentials bound to
revoked or archived agents fail authentication immediately.

Every mutating HTTP request first creates an organization-scoped command keyed
by operation and `Idempotency-Key`. Identical retries return that command's
durable result. Different input under the same key fails before reaching a
domain module. If the domain write succeeds but command completion cannot be
confirmed, the response remains unresolved and the command is never executed a
second time automatically. Command completion uses a bounded server-owned
context so client cancellation cannot interrupt the durable result write after
the domain mutation succeeds.

Policy evaluation resolves the one active version for the authenticated
organization and governed agent. Caps, rails, assets, categories, and recipient
allowlists are never loaded from one process-global multi-tenant policy.

The authorization lifecycle receives an `EventJournal` interface. Its
PostgreSQL implementation preserves the existing sequence and hash-chain
validation, uses a serializable transaction plus an advisory transaction lock,
and faults permanently if another writer advances the stream behind the
process. FlowOps therefore fails closed rather than combining independently
evaluated reservations from two control-plane writers. A single active writer
is the Phase 1 deployment posture. Event payloads use `bytea`, not `jsonb`, so
the exact bytes committed by each hash survive database replay unchanged.

The final ACTIVE check for authorization issuance runs under a PostgreSQL row
lock held until the authorization event commits. Pause uses the same row lock,
which makes their ordering explicit and prevents a pause from committing
between check and issuance. Pause transitions are limited to ACTIVE to PAUSED
and idempotent PAUSED handling.

Approval expiry is active maintenance rather than an optional caller action:
startup, a periodic worker, approval/dashboard reads, and new intent creation
all sweep expired reservations. Maintenance failure is fail-closed.

The Go HTTP server binds only to loopback. Production ingress must terminate
TLS in a trusted local reverse proxy; direct non-loopback plaintext bearer
transport is rejected at configuration load.

## Dependency

`github.com/jackc/pgx/v5/stdlib` is the PostgreSQL `database/sql` driver. It is
isolated to the executable boundary; domain packages continue to depend only on
`database/sql` interfaces. `github.com/DATA-DOG/go-sqlmock` is test-only and
verifies transaction, tenant predicate, JSON, and hash-journal behavior without
requiring a database daemon in every unit-test environment.

## Consequences

- Organization-scoped intent idempotency is enforced in both the API command
  table and lifecycle replay map.
- Organization A identifiers return the same not-found surface to Organization
  B and reveal no metadata.
- Approval and emergency pause require a human role and fresh step-up claim.
- Agent pause is persisted with its before/after audit event in one transaction
  and serialized against authorization issuance through `AgentFreezeGate`.
- Base halt or stale-observer state blocks authorization issuance but does not
  erase the intent, approval, command, or last trusted chain state.
- PostgreSQL backup, restore, high availability, credential issuance UI, and
  multi-writer reservation serialization remain deployment work. Mainnet stays
  blocked until those operational gates are proven.
