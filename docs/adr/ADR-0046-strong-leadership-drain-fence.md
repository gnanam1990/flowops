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
`ACTIVE` and `DRAINING` states. Serialize durable effect admission, drain, and
exact-CAS advance with the same transaction advisory lock. Permit only
`ACTIVE(N) -> DRAINING(N) -> ACTIVE(N+1)`, make the evidence log append-only,
require drain and advance to commit in separate transactions, and enforce
those rules with both application checks and database triggers.
Persist each admitted effect as `IN_FLIGHT` before its callback starts. Drain
enters `DRAINING` and waits for those records to resolve; advance is rejected
while one remains. A database connection loss therefore leaves a durable
fail-closed record instead of silently releasing the safety boundary.
Run mutations through a separate least-privilege controller role; effect roles
receive read-only epoch access plus scoped durable admission/completion rights.

The effect callback uses its service transaction after admission commits; no
leadership transaction or connection remains held. It must be bounded and
independently idempotent because an external outcome can
still be ambiguous. Cutover never advances until `BeginDrain` returns and
old-host work has been stopped. A stuck effect can be abandoned only during
`DRAINING`, with explicit operator identity and evidence after the old host has
been proved dead.

## Consequences

Leadership safety is shared across replicas and survives process restart.
Drain waits on durable admitted effects and blocks later effect entry. A hash
collision may reduce availability by serializing two organizations but does
not weaken safety. Effect admission/completion, timeout, wait, and drain
telemetry are production requirements. PostgreSQL
availability is now required for leadership-controlled effects, which fail
closed when it is unavailable.
