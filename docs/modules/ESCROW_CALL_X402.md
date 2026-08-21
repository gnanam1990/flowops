# `escrow-call/1` x402 v2 profile

## Why and entry

`pkg/escrowcall` is the canonical ASCP escrow wire boundary. It converts a
durably accepted SellerQuote and PurchaseSpec into a standard x402 v2 challenge,
creates the paid retry solely from stored operation data, verifies both request
and payment proofs immediately before egress, and strictly handles
`PAYMENT-RESPONSE`.

The public entries are `BuildOffer`, `ChallengeHTTP`,
`BuildPaymentSignature`, `VerifyBeforeEgress`, `PaymentHeaders`, `PrepareRequest`,
`BuildPaymentResponse`, `BuildPaymentError`, `DecodePaymentResponse`, and
`PaymentResponseHTTP`. `NewHTTPClient` supplies the bounded, no-cookie,
no-redirect client shape for a caller-owned restricted transport.

## Inputs

- Exact canonical PurchaseSpec JSON and the exact persisted seller-request body.
- x402 resource metadata whose URL equals the PurchaseSpec canonical URL.
- Unexpired SellerQuote and signature plus the durable intake binding:
  ServiceDirectory address, quote digest, and recovered quote signer.
- Stored operation binding: non-zero call ID, escrow contract, commitment hash,
  scheme version, and ResourceRequestDigest.
- For a success response: call ID, delivered content digest, finalized/safe lock
  transaction hash as required by the caller's recognition policy, and payer.

The wire package trusts the durable quote-intake record only for the directory
activation, revocation-overlay, epoch, and nonce-ownership decision already made
atomically by `pkg/sellerquote`; it independently reproves the quote digest and
signature relationship.

## Internal behavior

1. Revalidate PurchaseSpec canonical bytes and its exact body hash.
2. Require the quote's purchase hash and the x402 resource URL to match that
   specification; reject expired quotes and networks other than Base mainnet or
   Base Sepolia.
3. Recompute the quote EIP-712 digest for the stored ServiceDirectory and recover
   the stored quote signer.
4. Construct one `PaymentRequirements` with scheme `escrow-call/1`, CAIP-2
   network, decimal amount, asset, payee, bounded timeout, and SellerQuote
   extension.
5. Serialize all profile bytes through the integer-only RFC 8785 canonicalizer;
   uint256 monetary values stay decimal strings and unsafe JSON integers reject.
6. Derive `ResourceRequestDigest` from only the original seller action.
7. Generate a `PaymentPayload` with the exact accepted object and stored call,
   escrow, commitment, and scheme version; derive `PaymentProofDigest` over its
   decoded canonical bytes.
8. Immediately before egress, recompute the resource digest and compare the
   payment header byte-for-byte with the only valid stored-data-derived payload.
9. Clone outbound headers, remove every caller payment header and both legacy
   escrow headers, verify the actual method/URL and every remaining bound header,
   and add exactly one generated `PAYMENT-SIGNATURE`.
10. Clone the actual HTTP request, reject Host overrides, proxy-form request
    targets, trailers, transfer-encoding overrides, and non-canonical routes,
    then rebuild `Body` and `GetBody` from the exact persisted bytes.
11. Encode/decode standard x402 v2 `PAYMENT-RESPONSE` success or the normative
    error set, requiring the expected call ID on decode.
12. Return HTTP 402 plus `Cache-Control: no-store` for challenges/failures and
    HTTP 200 plus `Cache-Control: private` for successful payment responses.
13. Require a caller-supplied restricted transport, a positive timeout no longer
    than 60 seconds, no cookie jar, and refusal of every redirect.

## Outputs and interfaces

`Offer` contains the official x402 `PaymentRequired`, selected requirement,
canonical bytes, ResourceRequestDigest, and base64 `PAYMENT-REQUIRED` value.
`Payment` contains the official `PaymentPayload`, canonical bytes,
PaymentProofDigest, and base64 `PAYMENT-SIGNATURE`. `PaymentResponse` contains the
official x402 settle response, canonical bytes, and base64 `PAYMENT-RESPONSE`.

`vectors/escrow-call-v1.json` is the language-neutral conformance artifact. The
Go tests pass its bytes through the official x402 v2 HTTP client.

## Failure and recovery

- PurchaseSpec/body/resource/quote/intake mismatch: no challenge or payment
  header.
- Expired quote, unsupported chain, unsafe integer, malformed address/hash,
  unbounded metadata, or quote-signature mismatch: fail closed.
- Changed request body, operation binding, accepted requirement, legacy/caller
  header, duplicate JSON key, trailing value, non-canonical encoding, or
  oversized header: no egress header.
- A response for a different call, network, amount, payer, transaction, content
  digest, scheme version, or error vocabulary is rejected.
- `VERSION_UNSUPPORTED` without exactly `[1]` is rejected. Other failures may not
  smuggle extra metadata.
- The caller retains the operation in its durable state and reconciles escrow
  state; this codec never interprets a network timeout as payment failure or
  releases budget.

## Production operations boundary

This package performs no network or chain I/O and stores no state. Production
activation uses `internal/ascprails` for the durable buyer worker, restricted
egress and response capture. It still requires compatible seller middleware
that verifies finalized on-chain escrow state, a seller result store through
`settleBy + 400 days`, production metrics/audit export, and
keeper/reconciliation integration. Those adapters must call
`VerifyBeforeEgress`/`PaymentHeaders`; bypassing them is unsupported.

## Acceptance criteria

- Complete x402 v2 challenge, payment, and response envelopes use the official
  types and Base CAIP-2 identifiers.
- The SellerQuote, exact accepted object, call, escrow, commitment, original
  request, and response call ID reject every tested substitution.
- Only generated `PAYMENT-SIGNATURE` reaches the seller; legacy escrow and all
  caller payment headers are absent.
- All seven §12.3 errors round-trip; unsupported version publishes `[1]`.
- Published vector bytes, base64 transport, quote digest/signer, request digest,
  and payment digest are stable.
- Official x402 v2 HTTP-client conformance, focused race/coverage tests, full
  repository checks, and adversarial PR review pass before merge.
