# FlowOps SellerQuote intake

## Why and entry

`pkg/sellerquote` is the canonical ASCP v4 SellerQuote EIP-712 implementation.
`Intake.Accept` is the entry point after the caller has persisted a purchase
specification and obtained verified ServiceDirectory evidence.

## Inputs and outputs

The input contains the opaque operation ID, API-scoped idempotency key,
ServiceDirectory address, SellerQuote, signature, expected immutable terms,
and verified directory evidence. On success it returns the SellerQuote digest
and recovered signer. A replay with the same key and exact input returns the
same result marked `replayed`.

## Failure states

- malformed, zero, non-canonical, expired, or non-canonically signed quotes
  reject before state mutation;
- an altered domain, quote key, directory version, seller/resource, payout,
  acknowledgement authority, resource price, verification work/budget/hash,
  purchase hash, scheme, chain, asset, or revocation overlay rejects;
- a quote nonce claimed by another operation returns `ErrNonceConsumed`;
- a reused key with different input returns `ErrIdempotencyConflict`; and
- a previously claimed operation cannot be attached to a different quote.

## Production contract

The caller must use a durable `ClaimStore` that atomically inserts the ASCP
intent and quote-nonce ownership. The supplied in-memory implementation is
test-only. This module does not dispatch, sign, reserve funds, or authorize a
legacy payment route.
