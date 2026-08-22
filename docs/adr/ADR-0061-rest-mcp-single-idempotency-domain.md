# ADR-0061: REST and MCP share one ASCP idempotency domain

Status: Accepted
Date: 2026-08-22

## Context

ASCP exposes durable operations through both REST and MCP. Separate transport
idempotency state, regenerating an operation after a lost response, or reading
current mutable evidence before checking an exact replay could consume a quote
nonce twice or create duplicate decisions, reservations, authorizations and
downstream effects.

## Decision

MCP remains a stateless authenticated adapter over the REST application
boundary. It forwards the exact body and idempotency key. The storage identity
is transport-independent and scoped to organization, authenticated agent,
logical endpoint and key.

Every replay check precedes new mutable-evidence work. Intake atomically binds
one operation and SellerQuote nonce. Orchestration stores one immutable policy
decision per operation. Authorization atomically revalidates current state,
reserves budget and writes one execution authorization. Signer activation is
unique per authorization and emits one prepare outbox event. Responses are
delivery artifacts, not commit acknowledgements required for correctness.
Serializable intake conflicts are retried within a fixed bound; a quote-nonce
unique error is classified as replay only after a fresh scoped idempotency read
proves the same canonical input hash.

## Consequences

- Concurrent REST and MCP submissions converge on the same identifiers.
- Losing any response is safe; the client retries the same key and body.
- Restart reconstruction uses durable records and does not generate a new
  operation, quote claim, decision, reservation, authorization, sign request,
  or prepare outbox event.
- A changed request under the same key conflicts, and a new key cannot reuse a
  consumed quote nonce.
- Transport-specific caches or retry IDs must never become economic identity.

## Verification

- `TestASCPCreateIsOneOperationAcrossConcurrentRESTMCPResponseLossAndRestart`
- `TestASCPDecisionReservationAuthorizationAreOneAcrossRESTMCPAndRestart`
- `TestASCPRealPostgresCrossSurfaceDurableUniqueness`
