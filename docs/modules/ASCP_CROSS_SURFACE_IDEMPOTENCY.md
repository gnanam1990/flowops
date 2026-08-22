# ASCP cross-surface idempotency and recovery

## Boundary

REST and MCP are two authenticated transports over the same ASCP application
services. They do not own separate idempotency domains. MCP forwards the exact
bearer credential, request body and `Idempotency-Key` to the REST handler; the
durable intake scope remains organization + authenticated agent + endpoint +
idempotency key.

Operation creation and SellerQuote nonce ownership occur in one store
transaction. Policy decisions are unique per operation. Current-state
revalidation, budget reservation and execution-authorization creation occur in
one serializable authorization transaction. Transport delivery does not commit
or roll back any of those records: a lost response is recovered by replaying
the same request.

The Postgres intake store retries bounded serialization/deadlock failures. If a
concurrent insert reports the quote-nonce constraint before the idempotency
constraint, it resolves the durable organization + agent + endpoint + key row
on a fresh transaction: an exact hash replays, a changed hash conflicts, and a
different key remains `QUOTE_NONCE_CONSUMED`.

## Recovery behavior

An exact replay returns the already stored operation before reading mutable
directory state. A replay after process replacement therefore keeps the same
operation ID and quote-nonce owner even if the directory is temporarily
unavailable or has advanced. A changed input under the same key conflicts; the
same quote nonce under a different key cannot create a second operation.

Evaluation and authorization also read their immutable result first. Concurrent
REST and MCP callers converge on one decision, reservation and authorization.
The two-phase signer boundary uses the authorization as its permanent economic
identity, so both transports also converge on one sign request and one prepare
outbox event. After a process restart, either surface returns those same
identifiers and does not repeat a reservation or signer-dispatch transaction.

## Executable evidence

`internal/controlapi/ascp_cross_surface_test.go` contains three layers:

1. a listener-free 32-way REST/MCP intake race with discarded responses,
   service reconstruction, directory failure during replay and a second-effect
   nonce attack;
2. a listener-free 24-way decision and authorization race through the real
   orchestration service with one durable test store and one counted
   reservation transaction;
3. a real-Postgres adapter test, enabled by `FLOWOPS_TEST_DATABASE_URL`, which
   runs the same cross-surface sequence against production intake,
   orchestration, authorization, activation and signer-outbox stores and asserts
   exactly one row in
   `ascp_intents`, `ascp_policy_decisions`, `ascp_budget_reservations` and
   `ascp_execution_authorizations`, plus one `ascp_sign_requests` row and one
   `SIGN_PREPARE_REQUESTED` outbox event. It then deactivates the current policy,
   reconstructs the services, replays every stage through both transports, and
   rechecks the counts.

The local stores in layers 1 and 2 are test doubles for deterministic race and
restart coverage; they are not production durability evidence. Layer 3 is the
production-adapter proof and must pass in database CI before AC-88 can advance
beyond local evidence. The outbox assertion proves one durable request to the
economic signer boundary; finalized on-chain uniqueness remains part of the
production-equivalent AC suite, not a claim made by this local module.

Run the listener-free suite with:

```sh
go test -race ./internal/controlapi -run 'TestASCP(CreateIsOneOperationAcrossConcurrentRESTMCPResponseLossAndRestart|DecisionReservationAuthorizationAreOneAcrossRESTMCPAndRestart)' -count=50
```

Run the production adapter with:

```sh
FLOWOPS_TEST_DATABASE_URL=postgres://... go test -race ./internal/controlapi -run TestASCPRealPostgresCrossSurfaceDurableUniqueness -count=10
```
