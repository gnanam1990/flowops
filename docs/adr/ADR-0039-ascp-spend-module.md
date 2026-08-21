# ADR-0039: Exact-action ASCP spend module

- Status: accepted
- Date: 2026-08-21

## Context

The operational Safe must execute routine escrow locks without collecting Safe-owner signatures for every purchase. The spend-authorizer key is not a Safe owner, so its signature must be constrained by an independently enforced on-chain boundary.

## Decision

Deploy one immutable `ASCPSpendModule` per operational Safe. The module binds EIP-712 authorizations to its own address, chain, Safe, action, nonce, authorizer epoch, and a contract-enforced validity window of at most ten minutes.

The module exposes only two execution paths:

1. an exact, canonically encoded `ASCPCallEscrow.lockCall` to an address-and-runtime-code-hash allowlisted escrow, with zero native value and Safe operation `CALL`; and
2. an exact configured-token `approve` call to an allowlisted escrow, bounded by an allowance ceiling and exact current allowance.

Lock principal is bounded by per-transaction and UTC-day counters. Counters and nonces are written before the Safe call and roll back if `execTransactionFromModule` returns false. Refunds never reduce `executedPrincipal`. Allowance changes never increase it.

Only the bound Safe may rotate the authorizer, alter the allowlist, schedule caps, pause or unpause, or permanently invalidate nonces. Every authorizer assignment increments a monotonic epoch, including A-to-A and A-to-B-to-A changes. Cap activation has a one-hour delay. The module is not proxy-upgradeable.

## Consequences

- A spend-authorizer compromise is limited to signed, allowlisted actions and active caps, but remains an incident requiring Safe pause, nonce inventory, and key rotation.
- Pausing and rotating do not prove a bearer authorization consumed; budget release uses finalized nonce invalidation or chain-time expiry evidence.
- Contract migration deploys and audits a successor, then the Safe enables the successor and disables the old module.
- A production-equivalent Safe integration proof and independent contract audit remain release gates; local harness tests are not that external evidence.
