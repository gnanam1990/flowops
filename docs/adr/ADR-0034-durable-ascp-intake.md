# ADR-0034: Durable ASCP operation intake

Status: Accepted
Date: 2026-08-20

## Decision

ASCP seller-quote acceptance is a new, separate operation-intake path. A
serializable PostgreSQL transaction inserts one immutable `ascp_intents` row
and claims its globally unique `quote_nonce`. Idempotency is scoped by
`{organization, actor, endpoint, key}`; an exact canonical-input hash replays
the stored operation while a changed input returns a conflict.

`operationId` is generated inside the service rather than accepted from an
agent. The canonical input excludes that generated value, so retries cannot
mint another economic operation. The table persists the quote binding,
directory version/contract, signer, quote audit snapshot, canonical
PurchaseSpec bytes plus a JSONB query projection, and exact request body before
any policy, reservation, signer, network, or fund action occurs.

## Production boundary

At the time of this decision the module was not mounted on REST/MCP and its
internal caller had to provide already verified finalized ServiceDirectory
evidence. ADR-0036 now mounts a separate agent adapter which derives identity,
builds the PurchaseSpec, and resolves current materialized directory evidence
before calling this unchanged internal service. The legacy `PaymentIntent`
endpoint remains a different pre-ASCP flow and does not create rows in
`ascp_intents`.

The database migration is immutable and runtime API credentials must retain
only the DML permissions needed for this table. SQL serialization failures are
returned to the caller for bounded application retry; they never cause a fresh
nonce or untracked side effect.
