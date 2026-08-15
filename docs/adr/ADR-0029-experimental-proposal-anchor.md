# ADR-0029: Experimental Base Mainnet Proposal Anchor

Status: implemented; one experimental evidence-only deployment verified
Date: 2026-08-15

## Context

FlowOps needs an honest Base mainnet proof for proposal review without exposing
real USDC to unaudited payment logic. Deploying the current `CallEscrow` with a
zero balance would not satisfy that boundary: its permissionless `fund` method
would become callable directly even if the UI hid every funding control.

A toggleable factory would create a similar trust problem. An administrator,
compromised key, or configuration error could enable contract creation before
the intended audit and production-release ceremony.

## Decision

FlowOps will use a separate `FlowOpsProposalAnchor` contract as the only
eligible pre-audit Base mainnet deployment. It is an evidence anchor, not a
factory. Its runtime permanently exposes only immutable proposal evidence and
pure negative capability getters. It contains no production or economic write
path.

The initial mainnet deployment script was structurally blocked by five
independent fields: zero deployer, zero proposal digest, zero source commit,
zero deployment-approval digest, and disabled broadcast. The approved promotion
package pinned the first three fields plus nonce, predicted address, bytecode
hashes, and gas ceilings. The activation-approval package additionally bound a
canonical approval statement while preserving the user's actual response and
kept broadcast disabled. The final broadcast activation passed focused and full
local repository checks before the separate human approval and the single
broadcast. After canonical receipt and source-verification evidence were
recorded, the committed package was returned to a disabled state.

The resulting contract is
`0x149D03Ec527Ad8667d47e7b6a2d316Dd54033250` on Base mainnet, created in block
`50008264` by transaction
`0x7fe3986c45a1c4de2c9ca421222569ba8e41cc6b7fe9173340a3954c9306a76b`.
It is fully source-verified and remains permanently evidence-only.

The public UI fails closed when no verified address is configured. A configured
address is displayed only as experimental and unaudited, with production,
funding, and vault creation explicitly disabled.

Because the anchor has no post-deployment authority or economic method, its
one-time proposal ceremony may use a dedicated software EOA with a minimal gas
balance. That exception is valid only when two independent Base observers agree
that the account has no code and matching latest and pending nonces, the exact
expected CREATE address and gas ceiling are pinned, the signer credential never
enters the repository or command line, and a fresh human approval precedes the
single broadcast. The account is permanently ineligible for `CallEscrow`, a
factory, a vault, a customer signer, treasury custody, or any production role.
Production deployment continues to require the hardware-backed identity and
recovery posture in ADR-0018.

## Consequences

- A mainnet anchor can prove that a proposal and source revision existed at a
  canonical Base transaction.
- It does not prove product usage, financial safety, audit completion, proposal
  acceptance, or production readiness.
- The anchor cannot become the production release. Audited payment contracts
  require a new address and the existing ADR-0018 promotion ceremony.
- Unsolicited direct token transfers to the address cannot be prevented by an
  ERC-20 recipient contract. The UI and documentation must warn users not to
  send assets, and the anchor deliberately has no withdrawal function.
- Existing CallEscrow mainnet prohibitions and evidence records remain
  unchanged.
- Using a software EOA for the evidence-only anchor is not evidence of
  production key security and creates no exception to ADR-0018.
