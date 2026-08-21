# ADR-0045: Durable restricted seller egress

## Status

Accepted.

## Context

`pkg/escrowcall` binds the x402 v2 wire bytes but intentionally has no network
or durable state. A worker can crash before send, after the seller acts, while
reading the body, after storing the response, or before obtaining confirmed
chain time. Treating all of those points as an ordinary POST retry risks a
second seller execution; treating a local timestamp as delivery evidence risks
an invalid release decision. A caller-supplied generic transport can also turn
the payment worker into an SSRF or payment-header redirect primitive.

## Decision

Persist exact immutable egress inputs and fenced attempts in PostgreSQL. Reuse
the escrow call ID and payment proof for a maximum of three ambiguous retries,
relying only on the protocol-required seller `{callId -> result}` store. Commit
the exact HTTP response in an append-only row before obtaining final chain time.
Recovery from `RESPONSE_STORED` is finalization-only and cannot invoke HTTP.

Require current leadership, current reconciliation-owned
`LOCKED_FINALIZED` operation state and confirmed Base time immediately before
each send. The database independently binds the job to organization, chain,
call, commitment, escrow, asset, payee and amount. Use an internally marked
HTTPS-only transport with connect-time DNS revalidation and no proxy,
compression, cookies or redirects. Use a separate least-privilege rails role.

## Consequences

Response loss is explicit and bounded rather than claimed exactly once. A
compatible seller-side idempotency/result store is a production prerequisite;
this decision does not make arbitrary HTTP endpoints retry-safe. Response
bytes occupy durable PostgreSQL storage and require retention/capacity
monitoring. Chain-observer degradation may leave work in `RESPONSE_STORED`, but
cannot lose the delivery or trigger another payment. The worker still cannot
sign, settle, release, refund or update accounting.
