# ASCP leadership drain fence

## Why

Prevent two issuance hosts, seller-egress workers, or other controlled-effect
owners from treating different epochs as current during a cutover. A cached
epoch or ordinary read cannot prove that leadership remained unchanged while
an irreversible external effect was in flight.

## Entry and operator flow

An isolated leadership controller bootstraps organization epoch 1 once through
the shipped `/flowops/ascp-leadership` command. For a cutover, an authorized
operator calls `BeginDrain` with the exact current epoch, waits for the call to
return, drains old-host work, and calls `Advance` with that same expected epoch.
Controlled-effect services call `Current` for early rejection and `Fence`
immediately around their bounded effect.

## Inputs

Every mutation binds an organization, exact expected epoch, constrained actor,
lower-case SHA-256 evidence digest, and controller time. `Fence` binds the
organization, expected epoch, and one synchronous callback. Organization and
actor values reject whitespace/control or out-of-contract characters.

## Internal behavior

PostgreSQL stores one `ACTIVE` or `DRAINING` row per organization plus an
append-only event history. Bootstrap is idempotent only for an exact replay.
The only legal updates are `ACTIVE(N) -> DRAINING(N)` and
`DRAINING(N) -> ACTIVE(N+1)`, and those two updates must commit in separate
transactions. Database checks and triggers independently reject
skipped epochs, illegal transitions, malformed identities, deletes, event
mutation, duplicate state events, and events that do not match current state.

`Fence`, `BeginDrain`, and `Advance` acquire the same transaction-scoped
advisory lock derived from the full organization identifier. A drain therefore
waits for an already-entered effect, and once draining is committed no new
effect can enter. Hash collisions can serialize unrelated organizations but
cannot permit concurrent leadership. The callback must be bounded; the lock
and one database connection remain held until it returns.

## Outputs and interfaces

`Postgres` provides `Bootstrap`, `BeginDrain`, `Advance`, `Get`, `Current`, and
`Fence`. It directly implements `ascprails.LeadershipGate` and returns shared
`ErrEpochChanged` identity for stale or draining egress. Records expose the
durable epoch, state, actor, evidence digest, and update time. There is no UI
mutation surface; `ascp-leadership status` is the operator read path and
dashboards may display read-only state and event evidence. The adapter requires
an explicit validated schema, so temporary objects cannot shadow its tables.

## Authorization and security boundaries

Only the dedicated role from `configure-leadership-role.sql` receives
column-scoped insert and update rights. API and rails roles receive epoch `SELECT`
only. The controller role has no table-wide update, delete, truncate, trigger,
reference, schema-create, superuser, role-create, database-create, replication,
or RLS-bypass authority, inheritance, inbound/outbound role memberships, or object ownership.
The configuration script refuses pre-existing membership or ownership instead
of relying on revocation to neutralize it. This database role is the authorization boundary;
production routing must not expose controller methods to ordinary tenant/API
credentials.

## Failure and recovery

Missing, draining, stale, malformed, or unreadable leadership fails closed and
does not invoke the effect. Callback failure rolls back the fence transaction
and releases the lock. A database/commit error after callback execution is an
ambiguous external-effect outcome, not proof the effect did not happen; each
effect protocol must retain its own idempotency key and durable outcome fence.
Controller retry is exact-CAS: a stale epoch or wrong state cannot advance.

## Observability and production operations

Alert on prolonged `DRAINING`, fence wait/hold time, stale-epoch rejection,
controller mutation failure, pool exhaustion, missing event/state pairs, and
unexpected controller-role use. Set statement and transaction limits above the
bounded effect deadline but below operational drain SLOs. Exercise controller
credential rotation, process death during drain, database failover, concurrent
drain attempts, and recovery from an ambiguous effect before production.

## Acceptance criteria

- Exact bootstrap replay returns the same epoch without a second event;
  substitution conflicts.
- A drain cannot complete while an old-epoch fenced effect is running.
- After drain commits, old and same-epoch effects invoke no callback.
- Advance succeeds only from exact `DRAINING(N)` to `ACTIVE(N+1)`.
- Concurrent or stale mutation cannot skip, reuse, delete, or rewrite epochs or
  audit events, including through direct SQL.
- Runtime and rails roles cannot mutate leadership; the controller cannot use
  broad or destructive table privileges.
- Focused race/vet, real PostgreSQL concurrency, migration/readiness, full
  repository checks, and adversarial PR review pass before merge.
