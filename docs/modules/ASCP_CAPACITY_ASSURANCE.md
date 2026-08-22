# ASCP capacity admission and AC-34 evidence

AC-34 has two independent controls. The execution-authorization transaction
must first acquire a durable global active-operation permit, and an external
production-equivalent load driver must produce immutable evidence that the
declared peak profile satisfies the PRD SLOs through retries and process
restarts.

## Hard admission boundary

Migration `0030_ascp_capacity_admission.sql` creates one locked global counter
and one admission per operation/reservation. The capacity function runs in the
same serializable transaction as budget reservation and authorization. When
the configured limit is full, the whole transaction rolls back: no reservation
or authorization leaks. A reservation transition to a terminal state releases
exactly one admission through a database trigger. Missing admission, duplicate
release, and counter underflow fail closed.

The runtime role can read capacity state and execute only the acquisition
function. It cannot directly insert, update, delete, or truncate admissions.
The API value and database value must match; mismatch closes admission instead
of silently choosing either value.

After applying migration `0030`, configure the database limit with the
migration-owner credential:

```sh
psql "$MIGRATION_OWNER_DATABASE_URL" \
  --set=max_active_operations="$FLOWOPS_ASCP_MAX_ACTIVE_OPERATIONS" \
  --file=deploy/control-plane/configure-capacity.sql
```

Set the identical canonical integer, from 1 through 100000, on the API as
`FLOWOPS_ASCP_MAX_ACTIVE_OPERATIONS`. Reducing the database maximum below the
current active count is rejected and requires an orderly drain first.

## Peak profile and evidence

The PRD defines latency and outcome SLOs but no normative peak request rate.
FlowOps therefore does not invent one. Before a run, the operator supplies a
strict versioned JSON profile that declares:

- the target requests per second and minimum sustained duration;
- the minimum retry-injection ratio;
- each process and required restart count; and
- every observed queue and its hard maximum depth.

The duration must be at least ten minutes. The external driver records primary
and replay attempts, stage timings, restart instance identities, queue points,
and authoritative final operation state. The profile SHA-256 digest is bound
into the evidence. `/flowops/ascp-capacity-audit` rejects unknown JSON fields,
trailing JSON, profile substitution, incomplete operation coverage, changed or
duplicate economic-effect identities, leaked reservations/authorizations,
unbounded or undrained queues, missing restarts, and SLO breaches.

The fixed PRD thresholds are:

| Measure | Required bound |
| --- | --- |
| Decision latency | p95 at or below 300 ms |
| Signer latency | p95 at or below 2 s |
| Broadcast to mined | p95 at or below 60 s |
| `claimExpired` broadcast | p99 at or below 10 minutes |
| Accepted intent terminal/actionable rate | at least 99.9% |
| Reservation, authorization, duplicate effect leaks | zero |

Run the auditor after the external driver has stopped and every declared queue
has emitted a zero-depth point at the evidence completion timestamp:

```sh
/flowops/ascp-capacity-audit \
  -profile /evidence/operator-peak-profile.json \
  -evidence /evidence/capacity-run.json
```

Exit status `0` is a pass, `1` is malformed input, and `2` is a valid evidence
envelope that failed acceptance. The auditor sends no traffic, restarts no
process, and does not claim that a production-equivalent run occurred merely
because unit tests or the binary itself execute successfully.
