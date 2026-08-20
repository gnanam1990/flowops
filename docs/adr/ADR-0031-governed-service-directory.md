# ADR-0031: Governed ServiceDirectory before escrow integration

Status: Accepted
Date: 2026-08-20

## Decision

FlowOps v2 introduces a non-custodial `ServiceDirectory` contract that records
governed Merkle roots for seller and resource terms. It is deployed separately
from the existing `CallEscrow` and holds no ETH or token balance.

The initial contract implements:

- immutable contract-governor address, which must be contract code (a Safe in
  a real deployment);
- EIP-712 `AdminActionAuthorization` verification for the directory publisher
  and protective pauser, with exact domain, contract, chain, role, selector,
  payload, workflow, epoch, operation, nonce, and short expiry bindings;
- immutable proposal records, a single live successor, governor-only approval,
  class-dependent activation delays, cancellation before activation, and
  re-proposal with a fresh nonce;
- current-root selection and sorted-pair Merkle verification for seller and
  resource leaves; and
- immediately protective seller/key overlays which hot keys may only tighten;
  only the governor can reverse an overlay.

## Deliberate integration boundary

The existing `CallEscrow` predates ASCP v3.4 and is **not** modified to consume
this directory. Wiring an existing funded escrow to newly introduced root,
leaf, quote, verifier, and module semantics in a partial change would create
an ambiguous security boundary. A successor escrow integration must enforce
the exact v3.4 commitment, current directory version, seller/resource proofs,
ACTIVE seller status, payout/ack linkage, and revocation overlays atomically.

Similarly, the FlowOps proposal workflow is currently durable off-chain. The
governor Safe must only submit `approveVersion` after the approval service has
validated and retained the availability/root/diff evidence identified by the
proposal workflow hashes. A future on-chain workflow-receipt adapter may make
that predicate contract-verifiable; until then this remains a production gate,
not an implied proof.

## Consequences

- The contract has no generic administrator, withdrawal, upgrade, or arbitrary
  call method.
- Hot keys have no fund-moving authority and cannot unpause a seller or
  un-revoke a quote key.
- Directory data is root-committed, but off-chain blobs and quote-intake nonce
  consumption are later modules. A root alone does not validate a seller quote.
- The new contract is unaudited and has no deployment script. It must not be
  deployed to Base mainnet or described as production-ready.

## Verification

Foundry tests cover governor-only approval/cancellation, proposal replay and
payload substitution, exact EIP-712 authorization binding, activation delays,
cancel/re-propose lifecycle, sorted Merkle leaf proofs, and one-way protective
overlays. The Merkle pair verifier additionally receives 512 fuzz runs.
