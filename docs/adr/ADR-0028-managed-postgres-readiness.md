# ADR-0028: Managed PostgreSQL readiness is two independent proofs

Status: Accepted

Date: 2026-08-15

## Context

The control plane already persisted authorization state, commands, audit
events, memberships, and the control-event hash chain in PostgreSQL. A working
connection did not prove that the selected runtime credential was least
privileged, that the exact embedded migrations were present, or that the
managed provider had backups, PITR, encryption at rest, and monitoring enabled.
Those last controls are provider-plane facts and are not truthfully observable
through ordinary PostgreSQL SQL.

## Decision

- Keep the migration/admin credential separate from the long-lived runtime
  credential. Runtime startup keeps migrations disabled.
- Apply one reviewed, exact runtime grant matrix. The role gets only schema
  usage and the table operations exercised by the API; it gets no schema DDL,
  deletes, truncation, trigger, reference, role, database, replication,
  superuser, or RLS-bypass capability.
- Inspect the exact current backend rather than trusting a URL string: require
  a negotiated `pg_stat_ssl` session, `sslmode=verify-full` in deployment
  configuration, safe role flags and role memberships, the public runtime
  schema, exact embedded migration checksums with no unknown migration, and no
  missing or surplus table privilege.
- Model backups, PITR, encryption at rest, and monitoring as a separate,
  short-lived operator-signed evidence record. Reject missing, disabled,
  duplicated, stale, unsigned, tampered, credential-bearing, or unknown
  controls.
- Do not describe the operator signature as provider attestation. Require an
  isolated restore drill and runtime-credential rotation drill before pilot
  admission.

## Consequences

The repository now defines and can automatically verify the database contract
without logging a database URL. A provider selection still requires live
credentials and console evidence, so the current repository-only result is
correctly `NOT RUN`, not ready. Future migrations must be followed by an
explicit review of the runtime grant matrix; otherwise the exact verifier fails
closed.

## Verification

- SQL-mock tests exercise the exact ready contract and non-TLS rejection;
- URL tests reject `disable` and encryption-only `require` modes;
- provider evidence tests mutate control enablement, membership, freshness,
  URL safety, and signed fields;
- `deploy/control-plane/test-postgres-readiness.sh` checks the grant contract
  and conditionally runs both live proofs without embedding secrets.
