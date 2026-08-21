# ADR-0046: Strong leadership drain fence

## Status

Accepted.

## Context

Seller egress and other irreversible effects need more than a current-epoch
read. A cutover can otherwise commit between that read and the effect, allowing
old and new owners to act concurrently. Process-local locks do not coordinate
replicas, and a blind epoch increment cannot prove that in-flight work drained.

## Decision

Keep the authoritative per-organization epoch in PostgreSQL with explicit
`ACTIVE` and `DRAINING` states. Serialize effect entry, drain, and exact-CAS
advance with the same transaction advisory lock. Permit only
`ACTIVE(N) -> DRAINING(N) -> ACTIVE(N+1)`, make the evidence log append-only,
require drain and advance to commit in separate transactions, and enforce
those rules with both application checks and database triggers.
Run mutations through a separate least-privilege controller role; effect roles
receive read-only epoch access.

The effect callback runs while its fence transaction and connection are held.
It must be bounded and independently idempotent because a connection or commit
failure after the callback is inherently ambiguous. Cutover never advances
until `BeginDrain` returns and old-host work has been stopped.

## Consequences

Leadership safety is shared across replicas and survives process restart.
Drain blocks behind admitted effects and blocks later effect entry. A hash
collision may reduce availability by serializing two organizations but does
not weaken safety. Long external effects consume a pool connection, so pool,
timeout, wait, and drain telemetry are production requirements. PostgreSQL
availability is now required for leadership-controlled effects, which fail
closed when it is unavailable.
