# FlowOps for Base: reviewer brief

Status: public proposal and controlled-pilot package. This is not a production
payment launch.

## Start here

- Live control room: https://flowopsagent.xyz
- Public reviewer brief: https://flowopsagent.xyz/base
- Public repository: https://github.com/gnanam1990/flowops
- Base mainnet proposal anchor:
  https://base.blockscout.com/address/0x149d03ec527ad8667d47e7b6a2d316dd54033250?tab=contract
- Base Sepolia reference-signer and escrow evidence:
  [REFERENCE_SIGNER_FUNDED_ESCROW_2026-08-15.md](../evidence/REFERENCE_SIGNER_FUNDED_ESCROW_2026-08-15.md)

## What FlowOps is

FlowOps is a Base-only control and evidence plane for autonomous agent
payments. It evaluates whether a requested action is authorized, asks for
human approval when required, hands a bounded authorization envelope to a
customer-managed signer, and reconciles the resulting transaction against
canonical Base evidence.

The product is built around one operating question: which agent acted, whose
money was involved, for which task, under which policy version, with whose
approval, to which recipient, and with what observed outcome?

## What exists today

| Capability | Evidence | Current boundary |
| --- | --- | --- |
| Live control room | https://flowopsagent.xyz | Public health plus organization-scoped PostgreSQL and Base evidence for authorized members; no demo records |
| Public reviewer brief | https://flowopsagent.xyz/base | Public product and evidence links |
| Mainnet provenance | Verified FlowOpsProposalAnchor | Evidence-only; no funds or payment entry points |
| Escrow lifecycle | Base Sepolia reference-signer proof | 0.1 test USDC and terminal refund; testnet only |
| Control and evidence architecture | Public repository and ADRs | Pre-alpha, controlled-pilot design |

## Why Base

Agent software can purchase APIs, data, compute, and digital services faster
than people can supervise every request. Simply moving USDC does not answer
whether a payment was appropriate or whether a provider delivered.

FlowOps uses Base for low-cost settlement and observable evidence. Its product
layer contributes deterministic policy decisions, explicit approvals,
customer-managed signing, and reconciliation from independent canonical
observations.

## Safety and availability

FlowOps does not custody customer private keys. The customer signer maintains
its own local trust root, caps, nonce-once behavior, freeze control, and
chain-liveness checks. A FlowOps authorization is necessary but never
sufficient for a payment.

The Base mainnet proposal anchor is permanently experimental and no-funds. It
cannot accept a deposit, create a vault, move USDC, upgrade, or become the
production release.

FlowOps does not claim DAU, WAU, transaction volume, audit completion, or
production readiness. A separate audited deployment, security review,
legal/custody review, production signer/RPC admission, and explicit deployment
approval are required before unrestricted mainnet funds.

## Further reading

- [Proposal submission package](FLOWOPS_BASE_PROPOSAL_SUBMISSION_PACKAGE_V1.md)
- [Product requirements](../product/FLOWOPS_PRD_v1.3.md)
- [Customer-managed signer ADR](../adr/ADR-0001-customer-managed-signer.md)
- [Escrow rail ADR](../adr/ADR-0003-escrow-rail.md)
- [Chain-halt ADR](../adr/ADR-0005-chain-halt.md)
- [Proposal-anchor evidence](../evidence/BASE_MAINNET_PROPOSAL_ANCHOR_2026-08-15.md)
