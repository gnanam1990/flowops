# FlowOps Build Plan

Status: active  
Baseline: FlowOps PRD v1.3 plus Phase 0 findings dated 2026-08-11

## Commit-sized implementation order

1. Repository governance and immutable evidence.
2. Canonical authorization-envelope module.
3. Deterministic policy engine.
4. Customer reference signer with durable nonce-once enforcement. **Verifier,
   durable nonce journal, hash-chained attempt journal, one-way at-most-once executor, signed
   callback, and no-funds conformance smoke are implemented; concrete wallet
   adapter, runnable sidecar, and funded Sepolia proof remain open.**
5. Control-plane intent and approval lifecycle.
6. x402 V2 Base Sepolia adapter and Builder Code conformance fixture.
7. Evidence Fetch provider. **Implementation complete; verification commands are documented in the module contract.**
8. Base reconciliation and halt-safe state. **Continuous production observer wiring, durable quorum progress, customer-signer receipt registration, receipt/finality worker, bounded reorg correction, customer-side one-way transaction executor, and the manual operator gate are implemented; concrete wallet integration, dedicated provider selection, and extended Sepolia threshold measurements remain external gates.**
9. Escrow contracts after the dispute-state redesign. **Local implementation,
   a verified Base Sepolia deployment, funded release/refund evidence, and a
   structurally blocked Base mainnet readiness package are complete; durable
   event registration, production dependencies, and external review remain
   gated.**
10. Dashboard and operator workflows. **Preview-safe surface and membership-bound live reads implemented; step-up write UX and ledger-backed aggregates remain integration gates.**
11. Authenticated control-plane API and PostgreSQL command boundary. **Production container, audited Sites owner bootstrap, credential rotation, and explicit edge-proxy transport checks are implemented; managed PostgreSQL deployment and Base observer wiring remain live gates.**

Each item lands as one or more isolated conventional commits with focused tests. Cross-module integration comes only after both module contracts are stable.

## Current hard blockers

- FlowOps app/service Builder Code has not been designated.
- Reference-signer Base Sepolia wallet has not been designated or funded.
- The hosted-facilitator calldata experiment remains `UNRESOLVED`.
- Dedicated production facilitator and independent paid Base RPC providers are not selected; the public Sepolia pair is only an integration drill.
- Base Sepolia CallEscrow has one funded release and one funded refund proof.
  Those proofs do not designate a production deployer or external reviewer and
  do not authorize Base mainnet.
- External security and legal reviews have not started.
- The Base mainnet deployment script deliberately has a zero deployer, zero
  review digest, and disabled broadcast. Public-RPC USDC observations are
  read-only preflight evidence and are not approved production observers.
- Dashboard live writes are intentionally unavailable until a separate fresh
  step-up ceremony and durable browser command-recovery flow are implemented.
- Sites owner provisioning and exchange-token rotation must be executed and
  evidenced on the selected managed database; production operators must not
  hand-edit membership rows.
- The API schema, PostgreSQL adapter, production container, and owner workflow
  are implemented, but managed PostgreSQL backup/restore, TLS, least-privilege
  roles, and rotation are not operationally proven.
- GitHub-hosted Actions remains blocked by the account billing/spending-limit hold. FlowOps now uses an isolated, one-job ephemeral self-hosted Linux runner; pull-request and post-merge `main` checks have passed on that runner.

These block live settlement or mainnet; they do not block local implementation of the authorization vertical slice.
