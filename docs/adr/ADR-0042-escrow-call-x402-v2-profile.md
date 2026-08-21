# ADR-0042: `escrow-call/1` x402 v2 wire profile

- Status: accepted
- Date: 2026-08-21

## Context

The earlier x402 adapter selected standard `exact/eip3009` offers and the
builder-code experiment prepared direct payments. Neither represented the ASCP
escrow rail. Reusing either path would omit the already-locked escrow call,
allow caller-controlled payment headers, or bind payment metadata into the
seller action so the 402 retry could not reproduce the approved request.

The ASCP profile needs two independent proofs: the original seller request must
remain identical to the approved PurchaseSpec, while the x402 retry must echo
the seller's exact accepted requirement and bind the stored call, escrow, and
execution commitment. Both proofs must be checked at the last point before
network egress.

## Decision

Implement `pkg/escrowcall` as a separate `escrow-call/1` scheme over the official
x402 v2 Go types. The challenge uses x402 version 2, CAIP-2 Base networks,
decimal-string amounts, and an `escrowCall` extension carrying the exact
SellerQuote and signature. Offer construction revalidates the persisted
PurchaseSpec/body, requires its hash and resource URL to match the quote, and
reproves the durable intake quote digest and recovered signer.

`ResourceRequestDigest` is RFC 8785 canonical JSON over the original method,
canonical URL, sorted bound-header hashes, and body hash. `PaymentProofDigest`
is domain-separated Keccak-256 over the canonical decoded `PAYMENT-SIGNATURE`
payload. That payload contains only x402 version, the exact accepted object, and
`{callId, escrowContract, commitmentHash, schemeVersion}`. It is base64 encoded
under `PAYMENT-SIGNATURE`; legacy `x-escrow-intent` and `x-escrow-call` headers
and all caller payment headers are removed.

The pre-egress API recomputes the request digest from canonical persisted bytes,
strictly decodes the payment header, rejects duplicate keys, unknown bytes,
oversize input, non-canonical serialization, binding substitution, and exact-
accepted drift, then returns the payment-proof digest. No HTTP client may send
the retry unless this call succeeds.

`PAYMENT-RESPONSE` uses the x402 v2 settle envelope. Success binds call ID,
content digest, lock transaction, payer, network, and amount. Failure permits
only the seven profile error reasons; `VERSION_UNSUPPORTED` carries exactly the
supported scheme-version list. Response decoding requires the expected call ID,
preventing cross-operation replay.

## Consequences

- The direct-payment experiment remains isolated and cannot silently become the
  production ASCP rail.
- All envelope and digest bytes are deterministic, integer-safe RFC 8785 values.
- The official x402 v2 HTTP client consumes the published challenge, payment,
  and response headers without translation.
- The shared vector freezes challenge, payload, response, error, request digest,
  payment digest, quote signature, and recovered quote signer.
- The module does not perform HTTP egress, read escrow state, lock funds, submit
  transactions, or persist seller results. Runtime rails orchestration, seller
  on-chain verification/idempotency storage, and the keeper remain separate
  modules.
