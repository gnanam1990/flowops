# FlowOps Build Plan

Status: active  
Baseline: FlowOps PRD v1.3 plus Phase 0 findings dated 2026-08-11

## Commit-sized implementation order

1. Repository governance and immutable evidence.
2. Canonical authorization-envelope module.
3. Deterministic policy engine.
4. Customer reference signer with durable nonce-once enforcement.
5. Control-plane intent and approval lifecycle.
6. x402 V2 Base Sepolia adapter and Builder Code conformance fixture.
7. Evidence Fetch provider. **Implementation complete; verification commands are documented in the module contract.**
8. Base reconciliation and halt-safe state. **Implementation complete; live provider selection and Sepolia threshold measurements remain external gates.**
9. Escrow contracts after the dispute-state redesign. **Local implementation complete; Base Sepolia deployment, event reconciliation, and external review remain gated.**
10. Dashboard and operator workflows. **Preview-safe operator surface implemented; authenticated control-plane reads/writes and production organization authorization remain integration gates.**
11. Authenticated control-plane API and PostgreSQL command boundary. **Local implementation complete; managed PostgreSQL operations, owner bootstrap, credential rotation, Base observer wiring, and Sites membership exchange remain deployment gates.**

Each item lands as one or more isolated conventional commits with focused tests. Cross-module integration comes only after both module contracts are stable.

## Current hard blockers

- FlowOps app/service Builder Code has not been designated.
- Reference-signer Base Sepolia wallet has not been designated or funded.
- The hosted-facilitator calldata experiment remains `UNRESOLVED`.
- Production facilitator and independent Base RPC providers are not selected.
- Base Sepolia escrow deployer, native USDC address verification, deployment window, and external reviewer are not designated.
- External security and legal reviews have not started.
- The dashboard control-plane adapter and server-side Sites-to-FlowOps membership exchange are not implemented; preview controls cannot move funds or mutate authorization state.
- The API schema and PostgreSQL adapter are implemented, but managed PostgreSQL backup/restore, TLS, least-privilege roles, owner bootstrap, and credential rotation are not operationally proven.
- GitHub-hosted Actions remains blocked by the account billing/spending-limit hold. FlowOps now uses an isolated, one-job ephemeral self-hosted Linux runner; pull-request and post-merge `main` checks have passed on that runner.

These block live settlement or mainnet; they do not block local implementation of the authorization vertical slice.
