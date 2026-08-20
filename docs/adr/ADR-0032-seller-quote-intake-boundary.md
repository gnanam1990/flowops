# ADR-0032: SellerQuote digest and intake boundary

Status: Accepted
Date: 2026-08-20

## Decision

FlowOps now has one Go implementation of the ASCP v4 `SellerQuote` EIP-712
type. It validates exact lowercase wire values, computes the prescribed type
string, domain separator, struct hash, and digest, rejects high-S signatures,
and recovers the seller key.

The intake service requires a verified directory observation for the quote's
pinned version: active seller/resource, unrevoked quote key, key epoch,
seller/resource IDs, payout, acknowledgement authority, and expected purchase,
verification, chain, and asset bindings. It consumes the quote nonce through a
single `ClaimStore` call, pairing it with the immutable operation ID and an
idempotency-input hash.

## Deliberate production boundary

`MemoryClaimStore` exists only for deterministic tests and local simulations.
It is not durable and is not connected to a public endpoint. A production
adapter must claim the nonce and insert the successor ASCP intent in one SQL
transaction, with idempotency scope `{orgId, actor, endpoint, logicalOperation}`
and permanent financial tombstones. The legacy `PaymentIntent` route does not
accept SellerQuote payloads and remains outside this module.

Likewise, `DirectoryEvidence` is an input from a future finalized-chain reader
that proves the ServiceDirectory leaf and overlays. This change neither claims
that such a reader exists nor permits an unverified root/blob to authorize a
payment.

## Verification

The package test suite covers the normative type string and vector, signature
recovery, domain substitution, expiry, changed directory terms, key revocation,
idempotent replay, operation conflicts, and a concurrent single-winner nonce
claim. The schema and vector have a checked SHA-256 integrity manifest. That
manifest is deliberately unsigned and not yet a v3.4 deployment artifact
manifest: adding signature, signer rotation, and build-time pin enforcement is
the next dedicated module.
