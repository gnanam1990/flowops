# Managed PostgreSQL readiness evidence

Date: 2026-08-15

Decision: **BLOCKED — implementation verified, live provider proof not run**

## Repository evidence

- The runtime-role SQL grant contract is explicit and denies broad privileges.
- The runtime verifier checks the current TLS backend, exact role escalation
  flags and reachable memberships, schema DDL denial, embedded migration
  checksums, and the complete table privilege matrix.
- Provider backup, PITR, encryption-at-rest, and monitoring controls use a
  separate short-lived signed evidence record because SQL cannot prove them.
- Mutation tests reject disabled or missing controls, stale evidence,
  credential-bearing evidence URLs, and any signed-field tampering.

`make smoke-postgres-readiness` passes the local contract and reports both live
checks as `NOT RUN` when no managed runtime URL or provider evidence has been
supplied.

## Live evidence still required

- selected managed PostgreSQL provider and region;
- `sslmode=verify-full` runtime connection passing the SQL report;
- signed, unexpired provider-control evidence passing the verifier;
- isolated restore drill evidence; and
- runtime credential-rotation drill evidence.

No database, backup, PITR, encryption, monitoring, restore, or rotation claim
is inferred from repository tests.
