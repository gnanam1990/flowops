# FlowOps intent anchor on Base mainnet

Status: **implementation complete; deployment deliberately blocked**.

This runbook deploys the limited `FlowOpsIntentAnchor` integration. It does not
deploy CallEscrow or the ASCP payment contracts, approve USDC, move funds, or
claim production payment readiness.

## What becomes functional

After deployment and dashboard configuration, a user can:

1. connect a wallet on Base mainnet;
2. prepare a canonical, controller-bound agent-spend intent and policy digest;
3. send a zero-value `anchorIntent(bytes32,bytes32,uint64)` call;
4. inspect the transaction receipt on Base Blockscout; and
5. read the exact controller-scoped record back from the deployed contract.

The `/mainnet` route remains visibly deployment-pending and disables the write
until `FLOWOPS_MAINNET_INTENT_ANCHOR_ADDRESS` contains a validated non-zero
address.

## Preparation gate

`contracts/script/DeployFlowOpsIntentAnchorBaseMainnet.s.sol` currently pins
zero values for the deployer, reviewed source commit, approval digest, predicted
contract address, initcode hash, and runtime hash. `MAINNET_BROADCAST_ENABLED`
is false. These values make the script refuse on Base mainnet before it can
reach a wallet prompt.

Run the focused implementation checks:

```sh
make test-mainnet-intent-anchor
```

## Promotion commit

After the implementation commit is clean and reviewed:

1. Select the exact deployment wallet. A fresh application-specific hardware
   identity is preferred; do not expose a private key to FlowOps or commit it.
2. Observe its `latest` and `pending` nonces through two independent Base
   mainnet RPC providers. They must agree.
3. Compute the CREATE address for that wallet and exact nonce.
4. Pin the reviewed 20-byte source commit, creation-bytecode hash, deployed
   runtime-bytecode hash, expected nonce, and expected contract address.
5. Prepare one canonical approval statement naming chain ID 8453, deployer,
   source commit, expected nonce/address, both bytecode hashes, maximum gas
   limit, maximum fee per gas, maximum total gas spend, zero transaction value,
   and no token approval or funding. Pin its SHA-256 digest.
6. Change `MAINNET_BROADCAST_ENABLED` to true only in that reviewed promotion
   commit. Run the complete test suite and a Base-mainnet fork rehearsal.

Changing the contract after the source commit is pinned invalidates the
promotion. Nonce drift invalidates the expected address and requires a new
approval rather than an automatic retry.

## One-time broadcast

Immediately before the transaction:

1. Recheck both provider nonces and recent canonical head agreement.
2. Confirm the wallet address and Base mainnet chain ID on the signing device.
3. Simulate the exact promotion commit and ensure value is zero.
4. Show the exact gas ceilings and canonical approval statement to the user.
5. Obtain explicit approval for this one deployment transaction.
6. Broadcast once. If the wallet or RPC result is unknown, preserve the hash or
   attempt evidence and investigate; never resend blindly.

## Post-deployment evidence

Before enabling the dashboard:

- obtain a successful canonical receipt through two providers;
- verify the transaction sender, nonce, creation input, and zero value;
- confirm the predicted address and runtime hash through both providers;
- read `BASE_MAINNET_CHAIN_ID`, `KIND`, `DEPLOYMENT_STATUS`, `acceptsFunds`, and
  `executesPayments` from the deployed contract;
- verify and publish the exact source on Base Blockscout; and
- commit a credential-free deployment record with the transaction, block,
  source commit, hashes, observers, and verification URL.

Only then configure `FLOWOPS_MAINNET_INTENT_ANCHOR_ADDRESS` in the hosted
dashboard and deploy the dashboard build. The UI must continue to state that
the integration anchors intent evidence and does not execute payments.
