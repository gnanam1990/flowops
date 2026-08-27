# ADR-0064: functional Base mainnet intent evidence

## Status

Accepted for a limited, zero-fund mainnet integration. Deployment remains
blocked until a separate promotion commit and explicit broadcast approval.

## Context

The existing `FlowOpsProposalAnchor` proves that a proposal and source revision
were published on Base mainnet, but it cannot represent a user action. FlowOps
needs a truthful functional mainnet path for reviewer evaluation without
turning an unaudited application build into a payment, custody, token-approval,
or arbitrary-execution system.

The relevant product statement is narrower than payment authorization: a user
should be able to bind an exact agent-spend intent to an exact policy version,
publish those digests from their own wallet, and independently read the record
from Base. The record must not imply that the payment was approved, signed,
settled, delivered, or reconciled.

## Decision

Introduce `FlowOpsIntentAnchor` on Base mainnet with one write operation:

```text
anchorIntent(intentDigest, policyDigest, expiresAt)
```

Records are scoped by `msg.sender` and `intentDigest`. A different wallet may
anchor the same digest without collision; the same wallet cannot replace or
replay its record. Expiry must be in the future and no more than 30 days from
the anchoring block.

The contract stores only digests and timestamps. It has no owner, admin,
upgrade, delegatecall, token, approval, transfer, arbitrary-call, withdrawal,
or payable surface. `acceptsFunds()` and `executesPayments()` permanently
return false. Direct ETH calls revert; users are warned not to transfer tokens
to the address because arbitrary ERC-20 transfers cannot be prevented or
recovered.

The dashboard canonicalizes the displayed controller, task, agent, recipient,
canonical Base USDC address, maximum atomic amount, purpose, policy version,
and expiry as `flowops-mainnet-intent-v1`, then SHA-256 hashes it. A separate
policy digest excludes task purpose and expiry but binds controller, agent,
recipient, asset, amount cap, and policy version. The browser sends only the
two digests and expiry to the contract with transaction value zero.

The UI is fail-closed until a validated deployment address is configured. It
fetches the live runtime and SHA-256 matches its exact compiled bytecode before
every write or verification, estimates the exact call before requesting a
wallet transaction, persists an unresolved transaction hash locally, blocks a
second preparation while that outcome is unresolved, waits for a successful
receipt, and separately reads the stored record. Wallet switching is restricted
to Base mainnet chain ID 8453.

## Trust and evidence boundaries

- A digest proves only that its controller anchored those bytes at that Base
  timestamp. The contract cannot prove that the offchain fields shown later
  are the original preimage unless the verifier independently reconstructs the
  exact canonical payload.
- A record is not a FlowOps approval, spend authorization, Safe signature,
  payment, settlement, delivery verdict, accounting entry, or production
  readiness claim.
- The browser wallet and injected EIP-1193 provider remain user-controlled.
  FlowOps never receives a private key or wallet seed.
- Public digests can still become correlatable if their preimages are disclosed.
  Sensitive free-form data must not be placed in the public canonical payload.
- A successful wallet receipt is not enough by itself; the product verifies the
  controller-scoped record through an independent read.

## Deployment consequence

The preparation script is committed with zero deployer, source commit,
approval digest, predicted address, and bytecode hashes, and with broadcast
disabled. A promotion commit must pin every value after a clean reviewed source
commit exists. The deployment transaction is zero-value and deploys only this
contract. Funding and all FlowOps payment contracts remain out of scope.
