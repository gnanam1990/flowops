# FlowOps Base Proposal Submission Package v1

Status: public proposal and controlled-pilot package; not a production-payment
launch

## Proposal summary

FlowOps is a Base-only control and evidence plane for autonomous agent
payments. It gives teams a way to decide whether an agent may spend, require a
human approval for higher-risk work, hand an exact and time-bound authorization
to a customer-managed signer, and reconcile the outcome against canonical Base
chain evidence.

The product is deliberately designed so FlowOps does not custody customer
private keys or treat a database record as proof of an onchain settlement,
release, or refund.

## What exists today

| Capability | Public evidence | Current status |
| --- | --- | --- |
| Public product/control-room experience | [FlowOps Control Room](https://flowops-control-room.gnanasekaran-sekaree.chatgpt.site) | Live public proposal view; no organization data in public mode |
| Base mainnet proposal provenance | [Verified FlowOpsProposalAnchor](https://base.blockscout.com/address/0x149d03ec527ad8667d47e7b6a2d316dd54033250?tab=contract) | Deployed, source verified, evidence-only |
| Exact creation proof | [Base transaction](https://base.blockscout.com/tx/0x7fe3986c45a1c4de2c9ca421222569ba8e41cc6b7fe9173340a3954c9306a76b) | Confirmed on Base mainnet, zero value transferred |
| Sepolia delivery-assured payment lifecycle | [Reference-signer and escrow evidence](../evidence/REFERENCE_SIGNER_FUNDED_ESCROW_2026-08-15.md) | Verified testnet proof only |
| Chain safety posture | [Control-plane operations](../operations/CONTROL_PLANE_DEPLOYMENT.md) | Two-observer quorum, halt-safe behavior, manual resume gate |

## Why Base

Agent software can buy data, compute, APIs, and digital services far faster
than a person can supervise each request. The hard problem is not merely
moving USDC: it is retaining an answer to who authorized the action, under
which policy, for which task, to which recipient, with what approval, and
whether the stated result was delivered.

Base gives FlowOps a low-cost settlement environment and a growing agent-
payment ecosystem. FlowOps contributes the missing control and evidence layer:

- deterministic allow, deny, or approval decisions;
- exact task-, policy-, amount-, recipient-, nonce-, and expiry-bound
  authorization envelopes;
- customer-owned signing rather than FlowOps-held keys;
- reconciliation from independent canonical Base observations; and
- an optional delivery-assured escrow pattern for providers that acknowledge
  and prove a delivered response.

## What the mainnet anchor proves

The anchor binds the SHA-256 digest of the immutable proposal document and a
specific repository revision to a Base mainnet transaction. It is a reviewer
provenance artifact, not a payment contract.

The anchor is permanently labelled `EXPERIMENTAL_UNAUDITED_NO_FUNDS`. It has
no payment, deposit, vault-creation, upgrade, ownership, or withdrawal path.
It must never receive ETH or tokens and cannot be promoted into the production
release.

Read the exact limits and verification details in
[the anchor evidence record](../evidence/BASE_MAINNET_PROPOSAL_ANCHOR_2026-08-15.md).

## Controlled-pilot plan

The next product milestone is a small, allowlisted pilot with technically
capable design partners. It will start with customer-managed signing,
transaction caps, explicit service allowlists, manual operational review, and
observable Base reconciliation.

Before FlowOps can accept unrestricted user funds or market itself as a
production payment system, it requires a separate audited mainnet payment
deployment, independent security review, legal/custody review, production
signer/RPC admission evidence, and a new explicit deployment approval. Those
items are intentionally outside this proposal package.

## Reviewer checklist

1. Open the public Control Room and confirm the proposal-only warnings.
2. Open the verified Base contract and confirm the address and source status.
3. Open the creation transaction and confirm its zero value.
4. Compare the immutable proposal digest in
   [`FLOWOPS_BASE_MAINNET_EXPERIMENTAL_ANCHOR_V1.md`](FLOWOPS_BASE_MAINNET_EXPERIMENTAL_ANCHOR_V1.md)
   to the anchor evidence record.
5. Review the public limits above before treating any implementation evidence
   as a claim of production readiness, product usage, or transaction volume.

## Claims discipline

This package does **not** claim DAU, WAU, transaction volume, grant approval,
external audit completion, or production readiness. FlowOps will report those
only when they exist as FlowOps-specific, independently verifiable facts.
