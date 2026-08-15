# ADR-0025: Final Base mainnet readiness audit

- Status: accepted; mainnet remains blocked
- Date: 2026-08-14

## Context

FlowOps had separate fail-closed records and validators for readiness,
hardware-wallet promotion, source rehearsal, and the external-review package.
Each record was accurate, but an operator still had to join their meaning by
hand. Implementation evidence could also be confused with promotion evidence:
the escrow reconciler, signer, pilot limits, RPC admission, hardware ceremony,
and review package exist, while independently approved production evidence is
still required for mainnet.

## Decision

Add one read-only aggregate audit that authenticates all four canonical records,
checks their cross-record bindings, verifies the in-code broadcast guards and
the contract's unaudited notice, and emits a secret-free JSON decision. The
current decision is `BLOCKED`, with deployment and funding both unauthorized.
After the funded reference-signer proof completed on 2026-08-15, eight completed
implementation capabilities and eleven unresolved promotion gates are reported
separately. The removed blocker is only the capped funded Sepolia signer proof;
full production-shaped pilot evidence remains blocked.

The audit's test-only record overrides are cleared by the only supported
hardware-wallet broadcast wrapper. That wrapper now calls the audit in
`--require-ready` mode before reading promotion timestamps, simulating, or
requesting a hardware signature. The current command must fail. A future
promotion PR must update both the underlying evidence and this audited release
state; changing one record cannot make the aggregate gate pass.

## Consequences

- A local implementation test can no longer be presented as a funded or
  independently approved mainnet proof.
- Operators get one deterministic list of work that can be performed by the
  engineering team and work that requires an independent reviewer, legal
  counsel, paid providers, hardware operators, or explicit human approval.
- CI proves the blocked state without contacting Base or exposing RPC secrets.
- This ADR authorizes no deployment, approval, transfer, or funding action.

## Verification

Run `make mainnet-final-audit`, `make test-mainnet-final-audit`, and `make
check`. `--require-ready` must return non-zero until a separate reviewed
promotion changes every canonical gate.
