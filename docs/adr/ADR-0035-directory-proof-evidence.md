# ADR-0035: ServiceDirectory proof evidence boundary

Status: Accepted
Date: 2026-08-20

## Decision

`pkg/directoryproof` independently reproduces `ServiceDirectory` seller and
resource leaf hashes using Solidity static ABI words and verifies sorted-pair
Merkle proofs against one observed root. It then derives the exact
`sellerquote.DirectoryEvidence` required by durable intake.

Evidence is accepted only when seller and resource proofs share one root and
version, their terms equal the quote, and the seller is `ACTIVE`, resource
supports escrow, and no protective overlay pauses the seller. Key revocation
is preserved in the evidence so SellerQuote intake rejects it before claiming a
nonce.

## Production boundary

This is a pure verifier, not a live chain client. A following reader module
must obtain `currentVersion`, `currentRoot`, paused/revoked overlays, leaves,
and proofs from a pinned ServiceDirectory at the required finalized block and
record block hash/number plus independent-observer agreement. Callers must not
construct `DirectoryEvidence{Verified:true}` without that reader path.
