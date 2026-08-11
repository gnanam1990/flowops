# Codex Working Agreement

## Scope

This repository implements FlowOps from the revisioned product document and the Phase 0 evidence under `docs/`. Do not import portfolio claims or code by memory. Every port must link back to an immutable source commit recorded in `docs/evidence/PORT_INVENTORY.md`.

## Commit discipline

1. Work on one module or one coherent migration at a time.
2. Run the module's focused tests before committing.
3. Run repository-wide checks before pushing.
4. Use conventional commits: `feat(module):`, `fix(module):`, `test(module):`, `docs(scope):`, or `chore(scope):`.
5. Never combine unrelated modules merely to reduce commit count.
6. Never amend or force-push a shared commit without explicit approval.

## Safety invariants

- FlowOps never stores or receives customer private keys.
- Authorization envelopes are exact, expiring, nonce-bound, policy-versioned capabilities.
- Customer signer validation is independent of the control plane.
- Unknown chain or facilitator outcomes are quarantined, never retried blindly.
- Database records cannot invent onchain settlement, release, or refund.
- Escrow timeouts follow confirmed chain state, not wall-clock wishes.
- Builder Code attribution is evidence metadata, never accounting truth.

## Verification

- Go: `go test -race ./...`, `go vet ./...`, and formatting check.
- Solidity: `forge test` plus invariant/fuzz tests.
- TypeScript: typecheck, tests, lint, and production build where applicable.
- Security-sensitive changes require negative tests for substitution, replay, expiry, freeze, and restart.

## Repository hygiene

- Never commit `.env`, credentials, wallets, keystores, generated databases, build outputs, or test keys used outside deterministic fixtures.
- Preserve unrelated changes.
- Record new external dependencies and trust assumptions in an ADR.
