# ASCP adaptation grants

## Contract

An authoritative persisted policy `DENY` may produce one platform-signed,
single-use `ASCP_GRANT_V1` artifact. `PER_ACTION_LIMIT_EXCEEDED`,
`TASK_BUDGET_EXCEEDED`, and `DAILY_BUDGET_EXCEEDED` map to `too_expensive`;
`RECIPIENT_NOT_ALLOWED` maps to `wrong_seller`. Blocked categories and every
other denial are ineligible and create no grant.

The grant binds its ID, original operation, organization, agent, task,
lowercase category, positive maximum atomic amount, sorted seller-ID set, one
remaining attempt, issuance time, and a maximum 30-minute expiry. The maximum
is the minimum of the original quoted amount, per-action limit, and currently
remaining task and daily budgets. A wrong-seller grant derives seller IDs from
active, non-revoked evidence at the current configured directory head whose
payout address is allowed by the immutable policy version. The caller cannot
supply these terms.

The agent create schema accepts only `adaptationGrantId`. The server performs
an exact `{organization, agent, grantId}` durable lookup; a caller-supplied
signed artifact is an unknown field. Intake independently verifies the
signature, time, task/category/amount/seller scope and then inserts the new
intent and changes the grant from `ISSUED/1` to `CONSUMED/0` in one serializable
PostgreSQL transaction. Exact idempotent replay remains valid after expiry and
does not consume again. A different intent cannot use the grant.

If the adapted intent is rejected again for an amount, budget, or recipient
reason, policy orchestration creates an approval instead of a second grant.
A blocked category remains non-escalatable.

## Signer and authorization boundary

FlowOps does not load an adaptation private key. The control-plane process
connects to a dedicated local `ASCP_RING6_COMPONENT_V1` HSM socket using a
platform adaptation key that is distinct from every customer spend-authorizer
key. The grant digest is the HSM idempotency key. Returned digest, operation
handle, canonical low-s signature, and recovered signer address are checked
before persistence. Exact issuance retry reads the existing scoped grant and
does not call the HSM again.

The grant ID and signed timestamps come from the authoritative persisted
decision inputs. Concurrent first-issuance retries therefore produce one
identical digest and one HSM idempotency operation before either caller can
observe the database winner.

All adaptation signing variables are an all-or-nothing startup tuple. If the
tuple is absent, ordinary intake and non-adaptive decisions remain available,
but an eligible rejection fails closed with retriable
`ADAPTATION_GRANT_UNAVAILABLE`. Missing and malformed grant IDs fail as invalid
agent intake; already-consumed grants return `409 ADAPTATION_GRANT_CONSUMED`.

## Durable invariants and recovery

Migration `0026_ascp_adaptation_grants.sql` enforces same-tenant and same-agent
links among original intent, grant, and consuming intent. JSON payload columns
must agree with relational columns. The table permits only the exact immutable
`ISSUED -> CONSUMED` transition, and its trigger proves that the consumer row
contains the same grant ID and digest. Runtime SQL receives only SELECT,
INSERT, and four column-level UPDATE privileges.

A policy decision may commit before its external HSM response. The evaluation
request is safe to retry: the authoritative decision replays, issuance derives
the same canonical request hash, and any already-stored grant returns without
signing. HSM unavailability never creates an unsigned grant. A database error
rolls back both intent insertion and grant consumption.

## Verification

- Unit and race tests cover reason eligibility, signature and field mutation,
  expiry, cross-scope use, exact issuance replay, one-use concurrency, and API
  error classification.
- Real PostgreSQL tests apply all migrations, reject cross-tenant issuance and
  a direct-SQL unbound consumer, race 24 consumers, and prove one committed
  adapted intent plus exact replay.
- Real PostgreSQL orchestration tests prove automatic `too_expensive` and
  `wrong_seller` grants, no blocked-category grant, no signer call on replay,
  and second-rejection escalation.

These local proofs do not attest managed PostgreSQL, production HSM custody,
socket deployment, backup/restore, alert delivery, or a live testnet flow.
