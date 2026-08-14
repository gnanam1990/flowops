# CallEscrow Threat Model

Status: external review package prepared; review not complete

Target network: Base mainnet (`8453`)

Asset: Circle native USDC on Base
Reviewed source commit: `808caa4c9905334c52d6f237863f5ff33b11ffb0`

## Security objective

For each task-bound call, only the snapshotted buyer can fund or accept, only
the snapshotted provider can acknowledge or submit delivery, and terminal
settlement can transfer exactly the locked amount only to the snapshotted
provider or buyer. A call ID binds chain, contract, buyer, task digest, and
request digest. No administrator can redirect funds, upgrade the contract,
pause it, or rescue assets.

The contract proves lifecycle and byte-digest conditions. It does not prove
that delivered content is true, useful, safe, or subjectively satisfactory.

## Assets and trust boundaries

- Buyer USDC held by `CallEscrow` while a call is Funded, Acknowledged, or
  Delivered.
- Immutable lifecycle terms and evidence digests stored on Base.
- Circle native USDC is an external dependency. Its proxy, administrators,
  freezing, blacklisting, availability, and future implementation behavior are
  outside FlowOps control.
- Base consensus, block timestamps, reorg behavior, and chain availability are
  external dependencies. Deadlines are chain-time conditions, not wall-clock
  promises.
- Customer signers, policy services, RPC admission, event reconciliation,
  provider services, and UI are separate trust boundaries and require their
  own review.

## Actors and capabilities

- Buyer: approves and funds exact call terms; may accept objective delivery.
- Provider: acknowledges and submits non-zero response/evidence digests.
- Public finalizer: may trigger an already-determined release or refund but
  cannot choose the recipient or amount.
- Malicious token/provider/buyer/finalizer: may reorder calls, reenter through
  token behavior, race boundaries, replay identifiers, or submit meaningless
  non-zero digests.
- Base/Circle administrator or infrastructure failure: may halt progress,
  reorganize observations, freeze an account, or change token implementation
  behavior.

## Required invariants

1. A call ID is non-zero, domain-separated, chain-bound, contract-bound,
   buyer-bound, and task/request-bound.
2. A call can be funded once and cannot change buyer, provider, amount,
   digests, or deadlines.
3. Funding succeeds only when the exact requested token amount arrives.
4. State transitions are one-way; Released and Refunded are terminal and
   mutually exclusive.
5. `totalLocked` equals the sum of live call liabilities. Unsolicited token
   balances are not liabilities and must never increase a payout.
6. Every terminal transition reduces locked accounting before the external
   transfer and is protected from reentrancy.
7. Release pays only the snapshotted provider; refund pays only the
   snapshotted buyer. Public callers cannot select either.
8. Acknowledgement and delivery include their exact deadline; refunds require
   the first timestamp strictly after the missed deadline; optimistic release
   includes its exact boundary.
9. Transfer logs precede FlowOps terminal events so reconciliation observes
   asset movement before the lifecycle claim.
10. During a halt or observer disagreement, offchain systems must preserve
    pending state and must not invent release or refund.

## Abuse cases and mitigations

| Threat | Required behavior |
| --- | --- |
| Front-run or replay a visible call ID | `deriveCallId` binds domain, chain, contract, buyer, task, and request. |
| Substitute provider, amount, deadlines, or evidence | Exact calldata is immutable after `fund`; signer must authorize the same tuple. |
| Fee-on-transfer or short-receipt token | Exact before/after balance check reverts the whole funding transition. |
| Reenter during transfer | Every state-changing external entrypoint is `nonReentrant`; accounting changes before payout. |
| Third party steals release/refund | Finalization may be public, but recipient and amount are stored and immutable. |
| Fake delivery | Zero digests are rejected, but non-zero digests prove only byte commitments; provider admission and offchain evidence validation remain necessary. |
| Chain halt or reorg | Deadlines remain chain-time conditions; offchain reconciliation must wait for canonical confirmation and recover explicitly. |
| USDC freeze/blacklist/upgrade | No contract-level bypass exists. Mainnet promotion requires reviewers to analyze the pinned live USDC dependency and operational response. |
| Forced/unsolicited token transfer | It does not affect `totalLocked` or any payout. With no rescue role, the excess remains stuck. |
| Compromised buyer/provider key | Contract role checks cannot restore a compromised key. Customer signer controls, caps, revocation, and incident response are separate gates. |

## Known limitations

- `KL-01-DIGESTS-PROVE-BYTES-NOT-QUALITY`: non-zero commitments do not prove
  truth, usefulness, safety, or subjective quality.
- `KL-02-OWNERLESS-NO-PAUSE-RESCUE-OR-UPGRADE`: the reduced admin attack
  surface also removes emergency pause, migration, and rescue powers.
- `KL-03-UNSOLICITED-ASSET-REMAINS-UNRECOVERABLE`: tokens sent outside `fund`
  are not claimable and cannot be rescued.
- `KL-04-USDC-UPGRADE-FREEZE-BLACKLIST-DEPENDENCY`: Circle-controlled token
  behavior can prevent funding or settlement.
- `KL-05-CHAIN-TIME-HALT-AND-REORG-DEPENDENCY`: wall-clock expiry does not
  settle while Base is not producing canonical blocks.
- `KL-06-ANYONE-MAY-FINALIZE-PINNED-RECIPIENT`: public finalization is
  intentional; it never grants recipient choice.
- `KL-07-EXTERNAL-SIGNER-AND-RECONCILIATION-REQUIRE-SEPARATE-REVIEW`: contract
  review cannot clear offchain security gates.

## External-review exit criteria

The external-review gate may become complete only when the independent report
identifies this exact commit and compiler/dependency bindings, all Critical and
High findings are resolved and retested, the final report SHA-256 is committed,
and the contract's `UNAUDITED` notice is removed in the same reviewed promotion
PR. A package, automated lint, passing tests, or an AI review is not an external
security audit.

## Primary technical references

- Solidity security considerations:
  <https://docs.soliditylang.org/en/latest/security-considerations.html>
- Foundry invariant testing:
  <https://getfoundry.sh/forge/invariant-testing>
- OpenZeppelin `SafeERC20` API:
  <https://docs.openzeppelin.com/contracts/5.x/api/token/erc20>
- Circle native USDC contract registry:
  <https://developers.circle.com/stablecoins/usdc-contract-addresses>
- Circle stablecoin EVM implementation features, including pause, upgrade, and
  blacklist controls: <https://github.com/circlefin/stablecoin-evm>
