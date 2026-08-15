# Base mainnet final readiness audit

Status: `BLOCKED`

Deployment authorized: no

Funding authorized: no

Network: Base mainnet (`8453`)

Updated: 2026-08-15 after the funded Base Sepolia signer proof

## What engineering has completed

- durable CallEscrow transition reconciliation and reorg correction;
- customer-controlled exact `fund(...)` signer path;
- a funded, capped Base Sepolia reference-signer FUND-to-REFUND lifecycle with
  canonical reconciliation and ledger/finality evidence;
- 1 USDC per-action and 10 USDC conservative per-signer pilot limits;
- production-shaped RPC admission rules;
- Ledger/Trezor-only deployment ceremony and post-deployment verification;
- deterministic source-verification rehearsal; and
- an exact, fail-closed external-review package.

These are implementation artifacts. They are not production approvals.

## Promotion blockers

1. Independent security review, retest, and a final report digest.
2. Specialist legal review of the ownerless delivery-assurance escrow.
3. A new production hardware-wallet deployer identity.
4. Key-ownership evidence and an approved recovery runbook.
5. Two selected, paid, operationally independent Base RPC providers.
6. Production admission evidence for the durable reconciliation path.
7. A measured and approved positive deployment-confirmation depth.
8. Approval of the rehearsed source-verification process.
9. Funded proof that both signer rails and pilot limits enforce the complete
    production-shaped path.
10. A fresh, time-bounded, nonce-bound, gas-capped human broadcast approval.
11. A reviewed promotion PR that replaces the zero deployer, zero review
    digest, and disabled broadcast constants.

## Machine result

`make mainnet-final-audit` authenticates the canonical readiness, promotion,
source-rehearsal, and review-package records and prints a JSON report. It makes
no network call. `make test-mainnet-final-audit` proves that the same package
rejects invented review completion, a fabricated deployer, premature source
approval, enabled funding, unknown modes, and a readiness request while any
blocker remains.

The hardware-wallet wrapper clears every test override and invokes the audit
with `--require-ready`. It therefore refuses before simulation or a hardware
prompt today. No mainnet transaction or fund movement was performed by this
audit.
