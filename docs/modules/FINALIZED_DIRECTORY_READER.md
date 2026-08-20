# FlowOps finalized ServiceDirectory reader

## Why and entry

`directoryreader.Reader` is the mandatory boundary between an off-chain
directory proof payload and SellerQuote intake. Call `EvidenceForQuote` after
persisting the immutable purchase request and before accepting a quote
signature.

## Inputs and behavior

Construction pins a Base chain ID, the deployed `ServiceDirectory` address,
and its expected runtime-code hash. It requires two to five named, independent
sources and a quorum of at least two.

Each source must obtain `finalized` block identity plus `currentVersion`,
`currentRoot`, seller/resource leaves and proofs, `pausedSeller`, and
`quoteKeyRevoked` with `eth_call` at that exact block. It must also get the
runtime bytecode hash for the configured contract from the same provider. The
reader runs all sources concurrently and only creates evidence when every
successful source returns the byte-identical snapshot; a stale/mismatched
block, root, overlay, leaf, proof, chain, contract, or code hash fails closed.

## Outputs and interfaces

The result supplies `sellerquote.DirectoryEvidence`, the finalized block
number/hash, version/root, provider identities, and a SHA-256 snapshot digest.
Persist these fields alongside the intake record for audit/replay. The reader
does not validate a SellerQuote signature, write on-chain, or make a payment.

## Failure and production boundary

Source failures never count toward quorum. Any conflicting valid response
rejects the full read rather than selecting a majority, because this boundary
authorizes spending. A paused seller, inactive seller/resource, or revoked key
is returned as verified-but-inactive evidence; the quote intake validator then
rejects it.

This module deliberately defines the safe adapter contract only. A deployment
must provide independent RPC-backed `Source` implementations, a finality
monitor, source-health alerts, and durable recording of the returned result.
