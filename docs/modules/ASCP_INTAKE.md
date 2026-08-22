# FlowOps durable ASCP intake

## Why and entry

`internal/ascpintake.Service.Create` accepts a validated SellerQuote before
policy or any money-moving action. It mints a random operation ID and delegates
one atomic durable claim to its `Store`.

## Inputs, outputs, and failure contract

The internal trusted caller supplies authenticated organization/actor identity,
a scoped idempotency key, ServiceDirectory contract, quote/signature, expected
purchase terms, verified directory evidence, canonical PurchaseSpec JSON, and
its exact outbound body bytes. Intake verifies those bytes and their Keccak hash before
requiring equality with the quote's `purchaseSpecHash`. Success returns an
immutable operation with quote hash and nonce ownership. Exact retries replay;
changed input under the same scope conflicts; a nonce owned by another
operation conflicts.

Malformed identity, quote, expiry, key/term/revocation evidence, signature,
or store failure makes no operation. No route here dispatches HTTP, reserves a
budget, issues a signature, calls a contract, or recognizes payment.

## Production contract

Use `PostgresStore`; `MemoryStore` is only for tests. The Store transaction
inserts the operation and unique quote nonce together. The runtime adapter
is mounted at `POST /agent/v1/intents` and through
`ascp.operation.create`. It derives organization and agent only from the bearer
credential, builds the PurchaseSpec itself, constrains chain/asset/scheme from
runtime configuration, and reads fresh evidence from the configured
ServiceDirectory's current quorum-materialized PostgreSQL head. Caller-supplied
identity, directory, policy, approval, signer, or chain evidence is not in the
request schema and unknown fields are rejected.

`GET /agent/v1/intents/{operationId}` and `ascp.operation.get` read only the
immutable redacted projection under an exact `{organization, agent,
operationId}` database predicate. Missing and cross-tenant records share the
same not-found response. Execution authorization never reconstructs canonical
bytes from JSONB normalization.

The route fails closed with `503 ASCP_INTAKE_UNAVAILABLE` when
`FLOWOPS_ASCP_DIRECTORY_CONTRACT` is unset. A missing or observation-time-stale
directory head is retriable and never falls back to caller evidence. The
transport currently caps the complete JSON request at 64 KiB even though the
core PurchaseSpec library supports larger exact bodies.

The untrusted create body contains `taskId`, HTTP `method`, canonicalizable
HTTPS `url`, optional exact `requestBodyBase64`, optional headers, response
contract, category, optional reason reference, SellerQuote, and its signature.
There is no organization, agent, directory contract/evidence, expected terms,
policy, approval, reservation, or signer-proof field. An optional
`adaptationGrantId` is resolved from durable state under the authenticated
organization and agent; caller-supplied grant artifacts are rejected. Its
signature and scope are independently verified and the grant is consumed in
the same transaction as the new intent. See `ASCP_ADAPTATION_GRANTS.md`.

Acceptance for this slice requires:

- a lost-response retry returns the original operation even after quote expiry
  or configured-directory rotation, while changed input under the same key
  conflicts;
- current head, snapshot identity and quote evidence are read in one statement
  and bound to the same chain, contract, version and finalized block;
- a missing/stale head, unknown leaf, invalid signature/body binding, or nonce
  collision creates no operation;
- unauthorized, malformed and failed responses carry the same generated
  `correlationId` in the response body and `X-Correlation-ID` header; and
- real PostgreSQL proves exact canonical PurchaseSpec/body bytes, replay, nonce
  ownership and tenant-scoped read behavior.
