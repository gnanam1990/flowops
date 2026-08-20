# FlowOps ServiceDirectory proof evidence

## Why and entry

`directoryproof.EvidenceForQuote` turns one finalized directory observation
into verified SellerQuote evidence without trusting a mutable off-chain blob.

## Inputs and behavior

It receives the directory contract/root/version/block, seller/resource leaves,
their sorted-pair Merkle proofs, and protective overlays. It reproduces the
contract leaf hashes, verifies both against the exact same root, then compares
payout, acknowledgement authority, signing key, resource, price, and
verification terms to the SellerQuote.

## Failure contract

Malformed encodings, root/proof mismatch, different seller/resource terms, or
an unverified observation reject. Paused sellers, non-active status,
non-escrow resources, and revoked keys yield evidence that downstream quote
intake fails closed. This package performs no RPC or payment action.
