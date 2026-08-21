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

Bootstrap binds an organization, constrained actor, lower-case SHA-256 evidence
digest, and controller time. Drain, advance, and abandonment also bind the exact
expected epoch; abandonment binds the durable effect ID. `Fence` binds the
organization, expected epoch, and one synchronous callback. Organization and
actor values reject whitespace/control or out-of-contract characters.

## Internal behavior

PostgreSQL stores one `ACTIVE` or `DRAINING` row per organization, an
append-only epoch event history, and durable effect-admission records.
Bootstrap is idempotent only for an exact replay.
The only legal updates are `ACTIVE(N) -> DRAINING(N)` and
`DRAINING(N) -> ACTIVE(N+1)`, and those two updates must commit in separate
transactions. Database checks and triggers independently reject
skipped epochs, illegal transitions, malformed identities, deletes, event
mutation, duplicate state events, and events that do not match current state.

`Fence` commits an `IN_FLIGHT` record under the same organization advisory lock
used by `BeginDrain` and `Advance`, then invokes the callback with no leadership
transaction held. A drain can commit `DRAINING` while that callback runs, but
does not return until every admitted effect is `COMPLETED` or explicitly
`ABANDONED`; the database independently rejects advance while one remains.
Connection loss after admission leaves the durable record in place, so it
cannot release the cutover boundary. Hash collisions can serialize admission
but cannot permit concurrent leadership. Callback database writes use their
service transaction and are not rolled back by `Fence`; the controller
credential remains isolated from the worker.

## Outputs and interfaces

`Postgres` provides `Bootstrap`, `BeginDrain`, `Advance`, `AbandonEffect`, `Get`,
`Current`, and `Fence`. It directly implements `ascprails.LeadershipGate` and returns shared
`ErrEpochChanged` identity for stale or draining egress. Records expose the
durable epoch, state, actor, evidence digest, and update time. There is no UI
mutation surface; `ascp-leadership status` is the operator read path and
dashboards may display read-only state and event evidence. The adapter requires
an explicit validated schema, so temporary objects cannot shadow its tables.

## Authorization and security boundaries

Only the dedicated role from `configure-leadership-role.sql` receives epoch
mutation and evidence-bound effect-abandonment rights. The rails role receives
only the column-scoped effect admission/completion rights required by `Fence`;
API/runtime reads are read-only. The controller role has no table-wide update, delete, truncate, trigger,
reference, schema-create, superuser, role-create, database-create, replication,
or RLS-bypass authority, inheritance, inbound/outbound role memberships, or object ownership.
The configuration script refuses pre-existing membership or ownership instead
of relying on revocation to neutralize it. This database role is the authorization boundary;
production routing must not expose controller methods to ordinary tenant/API
credentials.

## Failure and recovery

Missing, draining, stale, malformed, or unreadable leadership fails closed and
does not invoke the effect. Callback failure still resolves its admission, but
the effect protocol must retain its own idempotency key and durable outcome
fence for ambiguous external results. If completion persistence fails, the
admission remains `IN_FLIGHT`, drain remains blocked, and advance fails at the
database boundary. Only after reconciling the effect-specific outcome and
proving the old host dead may an operator use `abandon-effect` with an exact
epoch, effect ID, actor, and evidence digest.
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
- A drain cannot complete while an old-epoch durable effect is in flight, even
  after the admitting database connection or process pool is lost.
- After drain commits, old and same-epoch effects invoke no callback.
- Advance succeeds only from exact `DRAINING(N)` to `ACTIVE(N+1)`.
- Concurrent or stale mutation cannot skip, reuse, delete, or rewrite epochs or
  audit events, including through direct SQL.
- Runtime and rails roles cannot mutate leadership; the controller cannot use
  broad or destructive table privileges.
- Focused race/vet, real PostgreSQL concurrency, migration/readiness, full
  repository checks, and adversarial PR review pass before merge.
