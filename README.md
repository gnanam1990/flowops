# FlowOps

FlowOps is a Base-only control and evidence plane for autonomous agent payments. It decides whether an action is authorized, asks for human approval when required, hands a bounded envelope to a customer-managed signer, and reconciles the resulting x402, direct-USDC, or delivery-assured escrow transaction against canonical Base evidence.

Status: pre-alpha, Phase 1 implementation. No mainnet funds.

## Product boundary

FlowOps owns policy, authorization, approvals, evidence, and reconciliation. Customers own their signing keys. A valid FlowOps authorization is necessary but never sufficient for a payment: the customer signer independently enforces its local trust root, limits, nonce, freeze, and chain-liveness rules.

## Repository map

```text
pkg/envelope/          signed authorization envelope and canonical digest
internal/policy/       deterministic allow, deny, or approval decision engine
cmd/reference-signer/  customer-run verifier and broadcast boundary
services/              x402, evidence, and reconciliation services
contracts/             Base escrow and related contracts
apps/dashboard/        operator and customer UI
docs/product/           revisioned product definition
docs/evidence/          immutable Phase 0 evidence
docs/adr/               architecture decisions
```

## Build rules

- Base Sepolia before Base mainnet.
- No private key is accepted by the FlowOps control plane.
- No settlement, release, or refund state without canonical chain evidence.
- No module commit until that module's tests pass.
- One module or coherent cross-cutting migration per conventional commit.

## Development

Prerequisites: Go 1.26+, Node 22+, pnpm 11+, and Foundry 1.7+.

```bash
make test
make check
```

The current build sequence and blockers are recorded in `docs/BUILD_PLAN.md`.
