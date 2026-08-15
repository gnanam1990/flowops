# FlowOps Build Plan

Status: active  
Baseline: FlowOps PRD v1.3 plus Phase 0 findings dated 2026-08-11

## Commit-sized implementation order

1. Repository governance and immutable evidence.
2. Canonical authorization-envelope module.
3. Deterministic policy engine.
4. Customer reference signer with durable nonce-once enforcement. **Verifier,
   durable nonce journal, hash-chained attempt journal, one-way at-most-once executor, signed
   callback, Clef wallet adapters, runnable sidecar, and no-funds conformance
   smoke are implemented; one capped funded escrow FUND-to-REFUND lifecycle is
   canonically reconciled on Base Sepolia.**
5. Control-plane intent and approval lifecycle.
6. x402 V2 Base Sepolia adapter and Builder Code conformance fixture.
7. Evidence Fetch provider. **Implementation complete; verification commands are documented in the module contract.**
8. Base reconciliation and halt-safe state. **Continuous production observer wiring, durable quorum progress, customer-signer receipt registration, receipt/finality worker, bounded reorg correction, customer-side one-way transaction executor, and the manual operator gate are implemented; the funded customer signer path has completed one Base Sepolia FUND-to-REFUND run, while production provider selection and extended threshold measurements remain external gates.**
9. Escrow contracts after the dispute-state redesign. **Local implementation,
   a verified Base Sepolia deployment, funded release/refund evidence, durable
   intent/transition reconciliation with reorg correction, and a structurally
   blocked Base mainnet readiness package are complete; the independently
   enforcing customer escrow FUND signer and one funded, canonically reconciled
   Sepolia proof are complete; production dependencies and external review
   remain gated.**
10. Dashboard and operator workflows. **Preview-safe reads, exact-member step-up command binding, exact-digest approval/denial, durable browser recovery, and persistent organization-wide authorization pause are implemented. The production step-up issuer and ledger-backed aggregates remain integration gates.**
11. Authenticated control-plane API and PostgreSQL command boundary. **Production container, audited Sites owner bootstrap, credential rotation, and explicit edge-proxy transport checks are implemented; managed PostgreSQL deployment and Base observer wiring remain live gates.**
12. Independent capped-pilot limits. **The control plane and both customer signer rails enforce the proposed per-customer Base mainnet profile before the wallet boundary, with restart-safe conservative exposure reconstruction. One capped escrow signer path is funded and reconciled on Sepolia; full two-rail production-shaped pilot admission remains open and mainnet funding remains disabled.**
13. Final Base mainnet readiness audit. **One read-only aggregate gate now authenticates the readiness, promotion, source-rehearsal, security-review, and funded-signer evidence records, separates eight implementation capabilities from eleven production blockers, and is required by the hardware broadcast wrapper. Its current and correct decision is `BLOCKED`.**

Each item lands as one or more isolated conventional commits with focused tests. Cross-module integration comes only after both module contracts are stable.

## Current hard blockers

- FlowOps app/service Builder Code has not been designated.
- The Base Sepolia reference-signer wallet and one capped escrow lifecycle are
  evidenced; this test identity is prohibited for Base mainnet promotion.
- The hosted-facilitator calldata experiment remains `UNRESOLVED`.
- Dedicated production facilitator and independent paid Base RPC providers are not selected; the public Sepolia pair is only an integration drill.
- Base Sepolia CallEscrow has one funded release and one funded refund proof.
  Those proofs do not designate a production deployer or external reviewer and
  do not authorize Base mainnet.
- External security and legal reviews have not started.
- The Base mainnet deployment script deliberately has a zero deployer, zero
  review digest, and disabled broadcast. Public-RPC USDC observations are
  read-only preflight evidence and are not approved production observers.
- The proposed pilot profile is structurally pinned at 1 USDC per action and 10
  USDC conservative exposure per customer signer. One 0.1 test-USDC escrow
  signer lifecycle passed, but funded two-rail production-shaped pilot evidence
  and single-customer or aggregate pilot admission remain hard blockers.
- Dashboard write transport and durable recovery are implemented, but hosted
  writes remain disabled until the production identity provider issues fresh,
  short-lived step-up credentials bound to the same Sites principal.
- Sites owner provisioning and exchange-token rotation must be executed and
  evidenced on the selected managed database; production operators must not
  hand-edit membership rows.
- The API schema, PostgreSQL adapter, production container, and owner workflow
  are implemented, but managed PostgreSQL backup/restore, TLS, least-privilege
  roles, and rotation are not operationally proven.
- GitHub-hosted Actions now runs on `ubuntu-24.04-arm`; PR #29 and its post-merge
  `main` run passed. The previous ephemeral self-hosted runner procedure is
  retained only as a documented fallback.

These block live settlement or mainnet; they do not block local implementation of the authorization vertical slice.
