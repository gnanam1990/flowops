# ADR-0029: Experimental Base Mainnet Proposal Anchor

Status: accepted for implementation; deployment remains blocked
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

The committed mainnet deployment script is structurally blocked by five
independent fields: zero deployer, zero proposal digest, zero source commit,
zero deployment-approval digest, and disabled broadcast. A later promotion
commit must bind all fields and pass focused, repository-wide, CI, and review
gates. Merging that commit will still not substitute for fresh broadcast
approval.

The public UI fails closed when no verified address is configured. A configured
address is displayed only as experimental and unaudited, with production,
funding, and vault creation explicitly disabled.

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
