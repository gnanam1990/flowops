# ASCP seller egress and delivery capture

## Why

Turn one already-authorized, finalized escrow lock into the exact bound seller
HTTP call without exposing payment headers to redirects or private networks,
blindly retrying a different payment, using local time as escrow truth, or
losing a successful response during a worker restart.

## Entry and user flow

The orchestration boundary enqueues one job only after quote intake, execution
authorization, customer signing and independently reconciled
`LOCKED_FINALIZED` payment state. The worker claims it with `DispatchOne`. A
successful HTTP response first becomes `RESPONSE_STORED`; a separate
`FinalizeOne` pass attaches confirmed Base time and exposes `CAPTURED` delivery
to the verifier. Users see queued, retrying, captured, missing or quarantined
status and the attempt/reason history; they never supply a replacement URL or
payment header during retry.

## Inputs

The immutable job binds job/call ID, payment operation, organization, Base
chain, leadership epoch, `deliverBy`, exact method, canonical HTTPS URL,
headers, body, canonical PurchaseSpec, x402 offer and payment, escrow contract,
commitment and resource-request digest. `ValidatedChainTime` records the
confirmed chain time at enqueue validation; it is not reused to authorize a
later send.

## Internal behavior

1. PostgreSQL claims one short fenced lease with `FOR UPDATE SKIP LOCKED`.
2. Immediately before egress, the worker requires a healthy event-chain
   recovery gate, then rechecks finalized leadership and the
   reconciliation-owned payment operation. Only `LOCKED_FINALIZED` with the
   exact organization, chain, call, commitment, escrow, asset, payee, amount
   and usable settle window proceeds.
3. A fresh corroborated Base timestamp is checked against `deliverBy`.
   Wall-clock time only controls queue scheduling and lease expiry.
4. `escrowcall.PrepareRequest` reconstructs and revalidates the exact stored
   request and sole generated `PAYMENT-SIGNATURE` immediately before send.
5. `SENDING` and its attempt/evidence are committed before network I/O. The
   restricted transport permits canonical HTTPS port 443 only, disables
   proxies/compression/cookies/redirects, rejects Host overrides and private,
   loopback, link-local, documentation, reserved or mixed DNS answers, and
   re-resolves immediately before the numeric-IP connection.
6. Response bytes, HTTP status, media type, PAYMENT-RESPONSE and SHA-256 digest
   are captured atomically in an append-only row. A valid 2xx response must
   carry the exact escrow-call response/call ID and the same body digest.
7. A separate confirmed chain observation moves `RESPONSE_STORED` to
   `CAPTURED`. The adapter then supplies exact bytes/status/type/digest and
   chain capture time to `ascpverifier`; it never signs a verdict.

## Outputs and module interfaces

`Store` owns enqueue, exclusive leases, attempts, response capture and
finalization. `IntegrityGate`, `LeadershipGate`, `OperationGate` and
`ChainClock` are independent
read boundaries. `Recorder` receives redaction-safe job, operation,
organization, state, attempt and reason codes. `CapturedDelivery` is the only
verifier adapter. This module cannot sign, broadcast, settle, release, refund,
alter accounting or declare output quality.

## Failure and recovery

- Event-integrity/leadership/operation/chain reads fail closed. Transient read failures wait;
  changed leadership or non-executable payment state is quarantined.
- Confirmed chain time at or after `deliverBy` becomes `MISSING` without a
  network call and persists the deadline evidence digest.
- A timeout, connection loss or partial body read is ambiguous. At most three
  attempts replay the exact same call ID, request bytes and payment proof;
  there is no new operation or alternate endpoint. Every abandoned attempt is
  recorded `AMBIGUOUS`.
- A 429 or 5xx response is durably captured and may receive the same exact
  retry. Terminal 3xx/4xx becomes `MISSING`. Oversized, encoded, malformed,
  unbound or digest-mismatched success is `DEAD_LETTER`.
- A crash after response storage cannot contact the seller again. If confirmed
  chain time is unavailable, `RESPONSE_STORED` remains recoverable until the
  finalizer succeeds.
- No HTTP status or response mutates payment, settlement or ledger truth.

## Security and authorization boundaries

The restricted transport has an unexported marker, so external callers cannot
construct the service with a generic `http.Transport`. PostgreSQL also rejects
seller jobs that do not reference and exactly bind a current finalized payment
operation, freezes all request fields, forbids terminal-state reopening, and
makes captured responses append-only. The runtime role can enqueue/read jobs;
the separate rails role has only the reviewed reads, inserts and column-level
updates. Neither role receives wallet or signer authority.

The compatible seller must verify finalized escrow state and store one
idempotent `{callId -> result}` through at least `settleBy + 400 days`. Buyer
exact replay depends on that protocol contract; general HTTP POST retry is not
claimed safe.

## Observability and operations

Alert on lease age, retries, ambiguous attempts, dead letters, deadline misses,
operation-gate rejection, chain-time unavailability, response size/encoding,
invalid PAYMENT-RESPONSE and response-finalization lag. Dashboards separate
seller HTTP latency, chain-time freshness and verifier outcome. Retain the
append-only response and attempt evidence for the escrow dispute window and
exercise restart at every persisted boundary. Production uses
`configure-rails-role.sql` and the same restricted transport; unrestricted
fallback is unsupported.

## Acceptance criteria

- Exact enqueue replay returns one job; any immutable substitution fails.
- Concurrent PostgreSQL claims have exactly one winner and stale lease tokens
  cannot update state.
- Verified event recovery, current leadership, current `LOCKED_FINALIZED` operation bindings and fresh
  confirmed chain time are rechecked before every seller send.
- HTTP/private-IP/alternate-port/mixed-DNS/rebinding/Host/redirect paths make
  zero seller connections; external code cannot inject a generic transport.
- Timeout/restart replays exactly the same URL, body, call and payment proof no
  more than three times; the original ambiguous attempt remains auditable.
- A crash after `RESPONSE_STORED` and chain-observer failure never re-egresses;
  later finalization returns the original exact delivery.
- A success with wrong call, payment response, body digest, content encoding or
  size never reaches `CAPTURED`.
- PostgreSQL rejects payment-operation substitution, request mutation,
  response mutation and terminal-state reopening.
- Focused race/vet, real PostgreSQL concurrency/restart tests, migration-role
  readiness, repository-wide checks and adversarial PR review pass before
  merge.
