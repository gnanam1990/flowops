# FlowOps Base Mainnet Proposal Anchor Evidence

Status: deployed and independently verified; experimental evidence only

## What is anchored

On 2026-08-15, FlowOps deployed the evidence-only
`FlowOpsProposalAnchor` to Base mainnet.

| Field | Verified value |
| --- | --- |
| Chain | Base mainnet (`8453`) |
| Contract | [`0x149D03Ec527Ad8667d47e7b6a2d316Dd54033250`](https://base.blockscout.com/address/0x149d03ec527ad8667d47e7b6a2d316dd54033250?tab=contract) |
| Creation transaction | [`0x7fe3986c45a1c4de2c9ca421222569ba8e41cc6b7fe9173340a3954c9306a76b`](https://base.blockscout.com/tx/0x7fe3986c45a1c4de2c9ca421222569ba8e41cc6b7fe9173340a3954c9306a76b) |
| Creation block | `50008264` |
| Transaction value | `0` wei |
| Proposal digest | `0x35476d70f7c33d19bb8fc1fa3484e289f0a42aac43e2beca7f941f5340132362` |
| Anchored source revision | `bd9292d0f916b1e3d828443b41e31a8e635b2b3e` |
| Source status | Fully verified on Base Blockscout |

The canonical machine-readable record is
[`deployments/base-mainnet-proposal-anchor.json`](../../deployments/base-mainnet-proposal-anchor.json).
It binds the receipt, independent-observer results, creation-input hash,
runtime-code hash, emitted event, source-verification settings, and the
post-deployment nonce observation.

## Why the anchored proposal says “proposed”

[`FLOWOPS_BASE_MAINNET_EXPERIMENTAL_ANCHOR_V1.md`](../proposals/FLOWOPS_BASE_MAINNET_EXPERIMENTAL_ANCHOR_V1.md)
was finalized before the deployment ceremony. Its SHA-256 digest is the value
stored in the contract, so that document must remain immutable. Its historical
“proposed; no Base mainnet deployment exists” status describes the point at
which the exact anchored text was approved; it is not a claim about the current
chain state.

This companion record, the deployment JSON, and the operational runbook record
the post-deployment fact without rewriting the onchain-bound proposal.

## What this does and does not prove

The anchor proves that one exact proposal document and one exact source revision
were committed to a canonical Base transaction. It does **not** prove product
traction, user numbers, transaction volume, audit completion, Base approval,
or production readiness.

The contract is permanently `EXPERIMENTAL_UNAUDITED_NO_FUNDS`:

- production readiness is `false`;
- vault creation is `false`;
- USDC deposits and every payment path are unavailable; and
- users must not send ETH or tokens to the address.

The one-time broadcast authorization was consumed after successful deployment.
The committed deployment package is disabled again; no additional proposal-anchor broadcast is authorized. A production FlowOps
release requires a new contract address, independent security and legal review,
production signer and RPC admission, and a separate human deployment approval.

## Reviewer links

- [Public FlowOps Control Room](https://flowops-control-room.gnanasekaran-sekaree.chatgpt.site)
- [Base Blockscout verified contract](https://base.blockscout.com/address/0x149d03ec527ad8667d47e7b6a2d316dd54033250?tab=contract)
- [Base Blockscout creation transaction](https://base.blockscout.com/tx/0x7fe3986c45a1c4de2c9ca421222569ba8e41cc6b7fe9173340a3954c9306a76b)
- [Operational runbook](../operations/FLOWOPS_PROPOSAL_ANCHOR_BASE_MAINNET.md)
