# ADR-0038: ASCP settlement evidence and classified accounting

## Status

Accepted for the buildable Base pilot slice.

## Decision

Signer activation atomically creates one immutable payment operation bound to
the approved ExecutionCommitment, reservation, bearer digest, Safe buyer,
escrow, asset, payee, amount, chain and deterministic call ID. A separately
authenticated keeper callback may register only a `LOCK`, `RELEASE`, or
`REFUND` transaction identity. It cannot claim success or finality.

The settlement worker queries every admitted independent Base RPC and accepts
only quorum evidence with the exact ASCPCallEscrow event, exact USDC Transfer,
exact ordering, exact transaction/block identity, and sufficient confirmation
depth. Successful state changes, reservation changes, bearer consumption,
chain observations and ledger postings commit in one serializable PostgreSQL
transaction. Safe terminal receipts do not recognize expense or refund.

Finalized accounting uses balanced append-only postings:

- lock: debit `EscrowRestrictedUSDC`, credit `WalletAvailableUSDC`;
- release: debit `SellerExpense`, credit `EscrowRestrictedUSDC`;
- refund: debit `WalletAvailableUSDC`, credit `EscrowRestrictedUSDC`.

Independent canonical-block disagreement is deferred. A proved reorg appends
exact inverse postings. A lock reorg moves the reservation to `REORGED_BACK`.
A terminal reorg restores `COMMITTED_FINALIZED` and quarantines automatic retry
in `PENDING_CHAIN_RECOVERY`; it never manufactures an onchain outcome or
blindly releases budget.

## Consequences

- The database records evidence-derived financial classification, never wallet
  authority or a fabricated chain result.
- Release and refund are mutually exclusive under serializable concurrency.
- Observations and ledger history reject updates and deletes.
- Unknown, reverted and reorged outcomes preserve or conservatively restore the
  reservation until independently proved recovery.
- Keeper callback, operator control, site session and signer boundaries use
  distinct credentials and capabilities.
