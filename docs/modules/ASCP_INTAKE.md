# FlowOps durable ASCP intake

## Why and entry

`internal/ascpintake.Service.Create` accepts a validated SellerQuote before
policy or any money-moving action. It mints a random operation ID and delegates
one atomic durable claim to its `Store`.

## Inputs, outputs, and failure contract

The trusted caller supplies authenticated organization/actor identity, a scoped
idempotency key, ServiceDirectory contract, quote/signature, expected purchase
terms, verified directory evidence, canonical PurchaseSpec JSON, and its exact
outbound body bytes. Intake verifies those bytes and their Keccak hash before
requiring equality with the quote's `purchaseSpecHash`. Success returns an
immutable operation with quote hash and nonce ownership. Exact retries replay;
changed input under the same scope conflicts; a nonce owned by another
operation conflicts.

Malformed identity, quote, expiry, key/term/revocation evidence, signature,
or store failure makes no operation. No route here dispatches HTTP, reserves a
budget, issues a signature, calls a contract, or recognizes payment.

## Production contract

Use `PostgresStore`; `MemoryStore` is only for tests. The Store transaction
inserts the operation and unique quote nonce together. A future REST/MCP
adapter must derive organization and actor only from credentials and persist
the normalized PurchaseSpec/body before calling this service.
