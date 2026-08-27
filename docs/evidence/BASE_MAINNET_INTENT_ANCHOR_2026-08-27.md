# Base mainnet intent-anchor evidence

Status: **deployed, finalized, source verified, zero value, no payment
execution**.

| Evidence | Value |
| --- | --- |
| Contract | [`0xD109ec995d8fC1FFD2fd66f367288b3Bc3EC8AAA`](https://base.blockscout.com/address/0xd109ec995d8fc1ffd2fd66f367288b3bc3ec8aaa?tab=contract) |
| Creation transaction | [`0x62e4b292d3e02a515390d574a20a550c4331ba6fd877bfcdb699d678e71c8d24`](https://base.blockscout.com/tx/0x62e4b292d3e02a515390d574a20a550c4331ba6fd877bfcdb699d678e71c8d24) |
| Base block | `50531762` |
| Block hash | `0x2d251b9c304e48df78e3ce6acf0295f63a801cd6609e9b41b4d83815c77dffa6` |
| Deployer | `0x3c1DAA7a6193848320e9477cBcfb7F512c0Fd74B` |
| Deployer nonce | `0` |
| Transaction value | `0 wei` |
| Gas used | `328426` |
| Effective gas price | `6000000 wei` |
| Total paid | `1970556000000 wei` |
| Approval digest | `0x20ea55570d31230094be2e4217e9b070694a2e888408d2c044970fd3d9d699d5` |
| Reviewed source commit | `ea21fbaaa8c8cc3aecca17e910146911703507da` |

Two independent observers, `mainnet.base.org` and `base.drpc.org`, returned the
same successful receipt, deployment-block hash, creation sender and nonce,
zero transaction value, contract address, and runtime code hash. Their
finalized tags both advanced beyond block `50531762` before this record was
marked finalized.

The creation-input Keccak-256 is
`0xefb111e5a3fd1eb31422a41d57a811f28d215e72b6f0cdf04d385fc83c06a863`.
The deployed runtime Keccak-256 is
`0x832a61ee74a1df09968706b4ffe3aacab23ad8ba463cc5407e8f795c499f4151`,
and its SHA-256 is
`0x0f3cfa13e6e029ab7ce2c4d3afa478ab1a4034e8e4fc91e456df1127ca91bdc8`.
All match the reviewed local build exactly.

Base Blockscout reports the source fully verified with Solidity
`v0.8.26+commit.8a97fa7a`, Cancun EVM, optimizer enabled, and 200 optimizer
runs.

Independent finalized-state reads returned:

- `BASE_MAINNET_CHAIN_ID = 8453`;
- `KIND = 0x7505f4374d8412c378c634523e83068844ffa970d7071f09a84c912a54ed76d9`;
- `DEPLOYMENT_STATUS = LIMITED_MAINNET_INTENT_EVIDENCE_NO_FUNDS`;
- `acceptsFunds = false`; and
- `executesPayments = false`.

This contract only anchors controller-scoped intent and policy evidence. It is
not a vault, escrow, token approval, payment executor, custody system, or proof
that the broader ASCP payment plane is production ready. The one-time deployment
gate is disabled after the successful broadcast.
