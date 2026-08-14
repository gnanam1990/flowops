# CallEscrow Module Contract

Status: local implementation, verified Base Sepolia deployment, read-only
lifecycle receipt verifier, and live Evidence Fetch release and forced-expiry
refund complete; mainnet gates remain open
Date: 2026-08-14

## Purpose and promise

`CallEscrow` is an optional payment rail for providers that explicitly implement the FlowOps acknowledgement and objective-delivery protocol. It gives a task a deterministic acknowledgement deadline, delivery deadline, buyer-accept release, optimistic release, and missed-deadline refund.

It does **not** prove truth, usefulness, or subjective output quality. It does not protect arbitrary x402/Bazaar services. V1 has no buyer-dispute or FlowOps resolver path because an unresolved `Held` state would strand funds and a resolver would materially change the custody/legal posture.

## Immutable boundary

One non-upgradeable deployment pins:

- one ERC-20 asset, which must be the verified native USDC address for the selected Base network; and
- one optimistic release window, greater than zero and no longer than 30 days.

There is no owner, proxy, pause, fee, registry, rescue destination, resolver, or FlowOps fund-movement role. A bug requires migration to a newly reviewed version.

## Call identity and authority

The canonical call ID is:

```text
keccak256(abi.encode(
  keccak256("FLOWOPS_CALL_ESCROW_V1"),
  chain_id,
  escrow_contract,
  buyer,
  task_digest,
  request_digest
))
```

This prevents another wallet from occupying a visible call ID and prevents the same ID from replaying across a chain or contract version. `fund` snapshots buyer, provider, atomic amount, task digest, request digest, acknowledgement deadline, and delivery deadline. The customer signer must independently authorize that exact calldata.

## State machine

```text
None -> Funded -> Acknowledged -> Delivered -> Released
          |             |
          +-> Refunded <-+
```

- Provider acknowledgement is allowed through `acknowledgeBy`, inclusive.
- Provider delivery is allowed through `deliverBy`, inclusive, and binds non-zero response and evidence digests.
- Buyer acceptance releases immediately to the snapshotted provider.
- Optimistic release is permissionless at or after `deliveredAt + optimisticReleaseWindow` and can pay only the snapshotted provider.
- Refund is permissionless strictly after a missed acknowledgement or delivery deadline and can pay only the original buyer.
- `Released` and `Refunded` are mutually exclusive and terminal. A call ID is never reusable.

All deadlines use `block.timestamp`. During a Base halt no transition occurs merely because wall-clock time passed; FlowOps must wait for canonical onchain evidence before reporting release or refund.

## Failure and attack handling

- Zero identities, amounts, digests, unsafe deadlines, duplicate IDs, wrong actors, early/late transitions, and replayed terminal calls revert.
- A buyer cannot be its own provider.
- Funding checks the escrow balance delta and atomically rejects fee-on-transfer assets.
- Checks-effects-interactions plus `ReentrancyGuard` prevent an outbound asset callback from finalizing another position.
- Asset `Transfer` logs precede terminal `Released`/`Refunded` logs so canonical reconciliation observes money movement before the outcome marker.
- Accidental token transfers are not recoverable by an admin because no such role exists. They do not increase `totalLocked`.

## Verification

Run:

```bash
git submodule update --init --recursive
forge fmt --check
forge build --sizes
forge test
forge coverage --report summary
make smoke-escrow
make check
```

The current local suite includes 21 unit/fuzz tests and three stateful invariants. Default settings execute 512 fuzz cases and 256 invariant runs at depth 64. The invariants prove live-position conservation, locked-balance equality, and that minted value remains only with buyer, provider, or escrow. The focused smoke proves buyer-accepted release, acknowledged-but-undelivered refund, and cross-position reentrancy blocking.

## Remaining gates

- Wire the completed escrow-specific event decoder into durable intent
  registration and reorg correction; the current conformance command is
  deliberately read-only.
- Complete independent security and legal review before any Base mainnet deployment or non-trivial value.

The successful release and failed-delivery refund lifecycles are recorded in
`docs/evidence/CALL_ESCROW_EVIDENCE_FETCH_LIVE_2026-08-14.md` and
`docs/evidence/CALL_ESCROW_EVIDENCE_FETCH_REFUND_2026-08-14.md`.
