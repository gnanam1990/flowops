# ADR-0033: PurchaseSpec canonicalization boundary

Status: Accepted
Date: 2026-08-20

## Decision

`pkg/purchasespec` is the single builder for the ASCP v4 `purchaseSpecHash`.
It accepts only an agent-originated request description and has no transport,
network, signing, or payment capability. It computes the exact body hash from
the persisted outbound bytes (or the empty string for `GET`), normalizes the
HTTPS URL, and binds only material headers.

The builder rejects agent-supplied credentials, cookies, host/forwarded,
payment, hop-by-hop, and `x-forwarded-*` headers. It strips `traceparent` and
`accept-encoding`, records that stripping in the result, and never lets those
rails-generated values affect the resource binding. Header values are OWS
trimmed only; internal whitespace and commas remain byte-significant.

The JSON object contains no maps or floating point values. It is encoded in
declared field order with HTML escaping disabled; input rejects the two Unicode
separators that Go's encoder would escape. This is the RFC 8785-compatible
subset used by the ASCP PurchaseSpec shape. The artifact vector pins its exact
bytes and Keccak-256 hash.

## Production boundary

The caller must persist the returned normalized spec and exact outbound body
before quote intake. This module does not make the request, and a later rails
module must calculate `ResourceRequestDigest` immediately before egress from
those persisted values. It is not safe to rebuild a PurchaseSpec from mutable
in-memory request state after approval.

The manifest is an unsigned integrity manifest. Signature, publisher rotation,
and Go/TypeScript/Solidity build pinning remain the dedicated artifact-manifest
module; this change makes no production-deployment claim.
