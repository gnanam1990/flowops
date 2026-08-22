# ASCP leadership drain fence

## Why

Prevent two issuance hosts, seller-egress workers, or other controlled-effect
owners from treating different epochs as current during a cutover. A cached
epoch or ordinary read cannot prove that leadership remained unchanged while
an irreversible external effect was in flight.

## Entry and operator flow

An isolated leadership controller bootstraps organization epoch 1 once through
the shipped `/flowops/ascp-leadership` command. A promotion is a mandatory
`drain -> ready -> advance -> complete` ceremony. `drain` freezes issuance,
waits for already-admitted effects, and records the finality margin. `ready`
requires every live lock authorization to have a registry-proven terminal
outcome and every verifier attestation to be past its bounded validity plus
that margin. `advance` performs the CAS cutover. `complete` then requires
post-cutover stale-epoch rejection evidence from all six controlled sinks.

## Inputs

Bootstrap binds an organization, constrained actor, lower-case SHA-256 evidence
digest, and controller time. Drain also binds a one-second through one-hour
finality margin. Ready, advance, completion, and abandonment bind the exact
source epoch and evidence digest; abandonment also binds the durable effect ID.
`FenceSink` binds the organization, presented epoch, exact sink name, and one
synchronous callback.

## Internal behavior

PostgreSQL stores one `ACTIVE` or `DRAINING` row per organization, an
append-only epoch event history, and durable effect-admission records.
Bootstrap is idempotent only for an exact replay.
The only legal updates are `ACTIVE(N) -> DRAINING(N)` and
`DRAINING(N) -> ACTIVE(N+1)`, and those two updates must commit in separate
transactions. Database checks and triggers independently reject
skipped epochs, illegal transitions, malformed identities, deletes, event
mutation, duplicate state events, and events that do not match current state.

`FenceSink` commits a sink-named `IN_FLIGHT` record under the same organization advisory lock
used by `BeginDrain` and `Advance`, then invokes the callback with no leadership
transaction held. A drain can commit `DRAINING` while that callback runs, but
does not return until every admitted effect is `COMPLETED` or explicitly
`ABANDONED`; the database independently rejects advance while one remains.
Connection loss after admission leaves the durable record in place, so it
cannot release the cutover boundary. A stale or draining request never invokes
the callback and appends a rejection containing sink, presented epoch, observed
epoch/state, and observation time. Hash collisions can serialize admission
but cannot permit concurrent leadership. Callback database writes use their
service transaction and are not rolled back by `Fence`; the controller
credential remains isolated from the worker.

## Outputs and interfaces

`Postgres` provides `Bootstrap`, `BeginPromotion`, `MarkPromotionReady`,
`Advance`, `CompletePromotion`, `Promotion`, `AbandonEffect`, `Get`, `Current`,
`Fence`, and `FenceSink`. It directly implements the production sink gates and returns shared
`ErrEpochChanged` identity for stale or draining egress. Records expose the
durable epoch, state, actor, evidence digest, and update time. There is no UI
mutation surface; `ascp-leadership status` is the operator read path and
dashboards may display read-only state and event evidence. The adapter requires
an explicit validated schema, so temporary objects cannot shadow its tables.

## Authorization and security boundaries

Only the dedicated role from `configure-leadership-role.sql` receives epoch and
promotion mutation and evidence-bound effect-abandonment rights. Seller,
keeper, bearer, verifier, and checkpointer roles receive only the column-scoped
effect/rejection writes required by `FenceSink`;
API/runtime reads are read-only. The controller role has no table-wide update, delete, truncate, trigger,
reference, schema-create, superuser, role-create, database-create, replication,
or RLS-bypass authority, inheritance, inbound/outbound role memberships, or object ownership.
The configuration script refuses pre-existing membership or ownership instead
of relying on revocation to neutralize it. This database role is the authorization boundary;
production routing must not expose controller methods to ordinary tenant/API
credentials.

Each worker receives only its sink-specific updatable effect and rejection
views; it cannot name another sink or fabricate that sink's cutover evidence.
Bearer and verifier deployments are single-organization leadership shards:
their pinned organization is matched to the claimed intent/commitment before
the sink callback can run. The controller sees only the operation-to-tenant,
bearer outcome, call-to-tenant, and attestation-validity columns needed to
evaluate the drain.

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

## Promotion commands

Each command accepts one strict JSON object on stdin. Evidence digests below
are placeholders for independently retained operator evidence:

```sh
printf '%s\n' '{"organizationId":"org_acme","expectedEpoch":7,"actor":"operator_a","evidenceDigest":"0x...","finalityMarginSeconds":120}' | /flowops/ascp-leadership drain
printf '%s\n' '{"organizationId":"org_acme","expectedEpoch":7,"evidenceDigest":"0x..."}' | /flowops/ascp-leadership ready
printf '%s\n' '{"organizationId":"org_acme","expectedEpoch":7,"actor":"operator_a","evidenceDigest":"0x..."}' | /flowops/ascp-leadership advance
printf '%s\n' '{"organizationId":"org_acme","expectedEpoch":7,"evidenceDigest":"0x..."}' | /flowops/ascp-leadership complete
```

After `advance`, continue injecting the old epoch through signer issuance,
verifier attestation, keeper relay, seller-proxy egress, outbox dispatch, and
checkpoint write. `complete` remains fail-closed until all six rejection rows
were observed at target epoch after the exact cutover timestamp.

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
- Advance succeeds only from exact `DRAINING(N)` to `ACTIVE(N+1)` after the
  authoritative bearer/attestation drain is ready.
- Promotion completion requires named post-cutover rejection evidence for all
  six controlled sinks; a count, caller boolean, or pre-cutover probe is not
  sufficient.
- Concurrent or stale mutation cannot skip, reuse, delete, or rewrite epochs or
  audit events, including through direct SQL.
- Runtime and rails roles cannot mutate leadership; the controller cannot use
  broad or destructive table privileges.
- Focused race/vet, real PostgreSQL concurrency, migration/readiness, full
  repository checks, and adversarial PR review pass before merge.
