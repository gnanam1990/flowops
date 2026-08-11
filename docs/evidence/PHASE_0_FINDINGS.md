# FlowOps Phase 0 Findings

Date: 2026-08-11  
PRD baseline: `outputs/FLOWOPS_PRD_v1.3.md`

## Outcome

Phase 0 is **substantially executed but its exit gate is not yet passed**.

Repository reproducibility, contract/source equivalence, the invariant freeze, the Base decision records, the x402 reference implementation, and the selected testnet facilitator's advertised capability are now evidenced. Two actions cannot be completed responsibly from the current workspace:

1. FlowOps has no designated app/service identity or reference-signer wallet, so claiming dashboard and agent Builder Codes would register the wrong identity.
2. A FlowOps-specific Base Sepolia settlement needs a funded signer and explicit confirmation before the test payment is sent. No signer, USDC balance, or Builder Code is configured.

The selected-path attribution result is therefore **`UNRESOLVED`**, not supported or unsupported.

## Exit-gate scorecard

| Phase 0 requirement | Status | Evidence / remaining work |
|---|---|---|
| Immutable repository inventory | Complete | Root `PORT_INVENTORY.md` |
| Clean-checkout Snapfall tests | Complete | 119 contract tests; race-enabled daemon suite; all sidecar checks; 78 dashboard tests and production build |
| Clean-checkout Tollbooth tests | Complete | Typecheck and 43 Foundry tests |
| Deployed Arc source equivalence | Complete | Runtime bytecode comparisons and immutable getter checks recorded in inventory |
| Invariant freeze | Complete | `PORT_INVENTORY.md` |
| Customer-signer ADR | Complete, provisional thresholds | `adrs/ADR-0001-customer-managed-signer.md` |
| x402 rail ADR | Complete, hosted-mainnet provider open | `adrs/ADR-0002-x402-v2-rail.md` |
| Escrow rail ADR | Complete, dispute redesign required | `adrs/ADR-0003-escrow-rail.md` |
| Base confirmation ADR | Complete, numeric thresholds open | `adrs/ADR-0004-base-confirmation.md` |
| Chain-halt ADR | Complete; P0/P1 split adopted | `adrs/ADR-0005-chain-halt.md` |
| Contract ownership ADR | Provisional pending legal/security review | `adrs/ADR-0006-contract-ownership.md` |
| Builder Code issuance and role mapping | Partial | `adrs/ADR-0007-builder-code-attribution.md`; identity-specific issuance blocked |
| Hosted-facilitator calldata experiment | Prepared, not paid | `CALLDATA_EXPERIMENT.md`; classification `UNRESOLVED` |

## Findings that change the build plan

### 1. The port is real, but the x402 rail is a rewrite

Snapfall's strongest reusable assets are the policy-to-approval-to-Grant boundary, durable exactly-once claim, freeze behavior, and job-keyed ledger. Its live Arc x402 transaction proves EIP-3009 settlement but was self-facilitated, used the V1-era sidecar, and has no ERC-8021 marker. FlowOps should preserve the adversarial tests and intent binding while implementing the protocol boundary against current x402 V2 packages.

### 2. “Cross-job spending is structurally inexpressible” needs narrowing

Snapfall makes an unsafe **approval-to-funding call** structurally unavailable because a populated `Grant` cannot be constructed outside the lifecycle. That is strong evidence. It is not yet an end-to-end proof that a new multi-customer FlowOps signer, nonce store, reconciliation worker, and Base adapter cannot cross tasks. The FlowOps acceptance test must span all those boundaries before the broader claim is restored.

### 3. Tollbooth's happy and timeout paths are reusable; its dispute state is not

Acknowledgement release, optimistic release, expiry refund, price snapshotting, recipient pinning, and conservation all reproduce. The `Held` state is intentionally terminal in v1 and has no resolver. Shipping it unchanged would turn a delivery dispute into indefinite custody. FlowOps must either omit disputes from the first escrow rail or ship an explicit finite resolution mechanism.

### 4. The existing dashboard is not a clean carry

It builds and its 78 tests pass, but the current dependency audit reports four high findings affecting Next.js and indirect packages. Reusing visual/component work is reasonable; inheriting the package graph without remediation is not.

### 5. Builder Code protocol support is stronger than hosted-path evidence

Current x402 docs specify the complete `a`/`s`/`w` flow and Schema 2 wire format. The pinned reference implementation's complete Go suite passes. On 2026-08-11, `https://x402.org/facilitator/supported` returned x402 V2 Base Sepolia support and listed `builder-code` among its extensions.

However, a scan of the latest 2,000 outgoing Base Sepolia transactions from the advertised facilitator signer `0xd407e409E34E0b9afb99EcCeb609bDbcD5e7f1bf` found zero calldata values ending in the ERC-8021 marker. This sample does **not** establish a defect: the sampled clients may not have supplied a Builder Code, and the public facilitator may omit its own `w` code. It does establish that capability advertisement alone cannot replace the FlowOps-specific experiment.

### 6. Phase 0 is not a one-week phase

The clean-room inventory and proof work alone spans contracts, Go, TypeScript, onchain receipts, runtime equivalence, dependency review, seven decisions, identity registration, and a live testnet payment. The realistic planning unit is:

- **Phase 0A, 5–7 working days:** repository evidence, invariants, and ADRs.
- **Phase 0B, 3–5 working days:** product identity, Builder Code issuance mapping, funded signer, live facilitator experiment, and any facilitator regression investigation.

That is 8–12 working days, assuming dashboard access and faucet funding do not block.

### 7. Chain-halt work should split without weakening safety

Adopt the review recommendation:

- **P0 halt-safe invariants:** refuse new autonomous authorizations/broadcasts on suspected halt; no stale settlement/refund; no blind rebroadcast; checkpoint ambiguous work; truthful degraded UI; manual operator release.
- **P1 automated detection/recovery:** observer quorum, provider disagreement state machine, stability window, automated backfill/reorg resolution, and controlled autonomous resume.

Two independent RPC providers are a P0 dependency because even a manual operator needs corroborating chain heads. Three providers and fully automated quorum/recovery can remain P1.

## Required v1.4 edits derived from evidence

Do not rewrite the PRD until the live attribution experiment lands. When it does, v1.4 should make only these evidence-driven changes:

1. Split P0-19 into P0 halt-safe invariants/manual recovery and P1 automated detection/recovery.
2. Add two independent Base RPC providers to the P0 dependency matrix; make a third provider optional/P1.
3. Change Phase 0 from one week to 8–12 working days or two named subphases.
4. Narrow Snapfall “per-job structural isolation” to the proven Grant/funding boundary and add an end-to-end customer/task isolation acceptance test.
5. Classify Snapfall x402 as a V2 rewrite with reusable threat-model tests, not a direct port.
6. Prohibit Tollbooth's unresolved `Held` state from being ported unchanged.
7. Add the dashboard dependency remediation gate.
8. Replace the Builder Code `UNRESOLVED` classification with the actual transaction evidence and selected facilitator/version behavior.

## Exact unblock package

To finish Phase 0B, provide or designate:

- one FlowOps app/service identity in base.dev and its intended owner/payout address;
- one Base Sepolia EOA for the reference signer;
- at least `0.001` test USDC at `0x036CbD53842c5426634e7929541eC2318f3dCF7e` in that signer;
- the FlowOps app code for `a` and FlowOps client/service code for `s` (they may be the same only after the issuance ADR records why);
- confirmation to send one `0.001` test-USDC x402 payment through `https://x402.org/facilitator`.

No mainnet funds, Base mainnet transaction, grant application, or production registration is part of this experiment.
