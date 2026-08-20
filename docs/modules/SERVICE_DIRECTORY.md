# FlowOps ServiceDirectory

## Why and entry

`contracts/src/ServiceDirectory.sol` is the governed source of active seller
and resource roots. A future escrow/signer checks its current root and proves
the exact seller/resource leaves before any money lock. The contract's entry
points are proposal, governor approval/cancellation, delayed activation, and
one-way protective overlay calls.

## Inputs, outputs, and boundaries

The directory publisher submits a `DirectoryProposal` with a signed,
short-lived EIP-712 `AdminActionAuthorization`. The authorization binds the
chain, contract, org domain, role, function selector, complete proposal hash,
workflow hash, nonce, epoch, and operation ID. A relayer may submit it but
cannot change its fields.

The governor contract approves a full proposal hash; activation waits one hour
for ordinary changes or 24 hours for payout/key authority changes. The active
output is `currentVersion()` plus `currentRoot()`. `verifySeller` and
`verifyResource` accept only proofs against that current version/root.

`pauseSeller(true)` and `setQuoteKeyRevoked(true)` require a pauser
authorization. Their `false` variants require the governor. No directory call
can transfer assets, choose an escrow recipient, sign a quote, or perform a
payment.

## Failure states and acceptance

- An invalid predecessor, live successor, duplicate proposer nonce, duplicate
  admin operation/nonce, expired authorization, wrong signer, altered payload,
  wrong role, or wrong chain reverts.
- A governor cannot approve by version alone: the expected immutable proposal
  hash must match.
- A cancelled version needs a new proposal nonce and authorization before the
  same version number may be reused.
- Missing/unavailable directory blobs are not handled by this contract. The
  approval/reconciliation service must freeze affected intake until a verified,
  governor-recorded replacement is available.
- The current `CallEscrow` does not consume this directory; that integration is
  intentionally a later audited migration.

Run `forge test --match-path contracts/test/ServiceDirectory.t.sol -vv` for
the focused test and fuzz suite.
