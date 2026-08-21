# ASCP execution authorization

`ascpexecauth` converts one exact, unexpired `APPROVED` review into either a
permanent `INVALIDATED` result or `VALIDATED_AND_RESERVED`. The approval remains
immutable. A unique authorization per intent prevents an approved snapshot
from producing more than one execution path.

## Atomic boundary

`PostgresStore.ValidateAndReserve` uses a serializable transaction to:

1. replay an existing terminal evaluation without rerunning checks;
2. lock the immutable intent and exact organization-scoped approval;
3. verify approval state, review-snapshot equality, and approval expiry;
4. rerun authoritative local identity, organization-pause, active-policy,
   exact canonical-PurchaseSpec, current finalized-directory, seller/key,
   quote, and execution-snapshot checks;
5. derive all five required budget dimensions from the stored organization,
   agent, task, category, UTC day, and active policy; reject caller-invented
   dimensions or limits; lock only matching indexed economically live rows;
   and create a maximum-15-minute pre-signature reservation; and
6. insert the execution authorization and commit both rows together.

PostgreSQL serialization failures are retried at most three times with the
same immutable IDs and input. No external effect occurs in this transaction.
Infrastructure/read failures roll back and remain retryable. A demonstrated
business mismatch commits `INVALIDATED`, creates no reservation, and requires
a fresh intent. Budget exhaustion also commits `INVALIDATED` so later budget
release cannot silently revive an old approval.

Successful release consumes every dimension. A finalized refund restores only
dimensions marked refundable; agent-lifetime usage remains consumed. The
normalized dimension index is inserted in the reservation transaction and is
backfilled fail-closed for pre-migration reservations.

Canonical PurchaseSpec bytes are stored separately as `bytea`; JSONB remains
an audit/query projection only. Legacy JSONB-only intents are not assigned
invented bytes and invalidate with `PURCHASE_SPEC_BYTES_UNAVAILABLE`.

## Finalized directory materialization

`directoryreader.PostgresStore` accepts only an integrity-sealed quorum result
returned by `directoryreader.Reader`. It appends the finalized snapshot and
quote evidence, then advances the chain/contract head monotonically. A
conflicting digest at an already-recorded finalized height fails closed.
The quorum-completion time is sealed into the result; recording cannot refresh
a held result, and a result delayed by more than one minute must be reread.
Execution authorization reads only the current durable head and rejects stale
observations, version changes, seller suspension, key revocation, payout or
acknowledgement changes, and quote-term changes. The configured observation
freshness window is capped at five minutes.

## Outputs

- `VALIDATED_AND_RESERVED` plus one `RESERVED` budget row; or
- `INVALIDATED` plus a stable redaction-safe reason and no budget row; or
- a retryable infrastructure/serialization-exhaustion error with no committed
  authorization or reservation.

This module does not issue or release a signature. The authenticated activation
intake reconstructs these authorization and reservation identifiers and creates
one durable `SIGN_REQUESTED` row. The separately supervised bearer worker then
atomically moves the reservation to `AUTHORIZATION_LIVE`, persists the
bearer-registry row and activation outbox, and only then acknowledges activation
of the opaque prepared signer handle.

## Verification

```sh
go test -race ./internal/ascpexecauth ./internal/directoryreader
go vet ./internal/ascpexecauth ./internal/directoryreader
FLOWOPS_TEST_DATABASE_URL=... go test -race ./internal/controlapi \
  -run TestASCPExecutionAuthorizationRealPostgresBudgetRace -count=1
```

The real-PostgreSQL test applies every migration in a fresh schema and races
two approvals for one remaining budget slot. Exactly one reservation and one
validated authorization commit; the loser is durably invalidated, approvals
remain `APPROVED`, and replay cannot create another reservation.
