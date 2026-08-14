# ADR-0019: Independent Capped-Pilot Limits

Status: Accepted and implemented; funding remains disabled
Date: 2026-08-14

## Context

Tenant policy already bounded individual actions, tasks, and daily spend, and
the customer signer already imposed a per-action maximum. Those controls did
not establish one deployment-wide mainnet pilot profile or a durable local
aggregate ceiling. A permissive tenant policy or a signer restart could
therefore undermine a limit written only in an operations document.

## Decision

The initial Base mainnet profile is exactly 1 USDC per action and 10 USDC of
maximum conservative signer exposure. Both values use native USDC's six-decimal
atomic representation (`1000000` and `10000000`). Changing either value
requires a reviewed code and readiness-record diff.

The control plane applies the pilot gate after tenant policy and before writing
an intent. It counts all pending, approved, and issued reservations for the
same organization and customer. A pilot refusal is durably recorded with a
specific denial reason and cannot be converted into an authorization.

The customer signer applies the same gate before authorization verification,
wallet preparation, signing, or broadcast. It reconstructs exposure from its
process-locked, hash-chained attempt journal on every restart. Until an
independently verified settlement-release protocol is implemented, every
durable prepared attempt remains reserved for the lifetime of that pilot
journal. This is stricter than outstanding-balance accounting and cannot
understate exposure after an ambiguous transaction.

Base Sepolia may use separately approved test limits. A Base mainnet reference
signer refuses configuration that differs from the committed initial profile.
The production control-plane loader carries the same check for the future
mainnet observer promotion path.

## Consequences

- FlowOps cannot silently raise the initial mainnet pilot caps through a
  database policy or signer configuration change.
- Rejected requests never reach the wallet boundary.
- Restart, callback failure, and ambiguous broadcasts do not reset exposure.
- The conservative signer ceiling can stop new work after 10 USDC of lifetime
  prepared attempts. Restoring capacity requires a future reviewed canonical
  settlement-release design; deleting or rotating the journal is not an
  accepted workaround.
- Funding remains separately disabled. Implemented limits do not authorize a
  mainnet deployment or transfer.

## Verification

Run `make smoke-pilot-limits`, `make test-mainnet-readiness`, and the full
repository checks. Negative tests cover malformed values, uint256 overflow,
per-action overflow, aggregate overflow, restart reconstruction, pre-wallet
refusal, and readiness-record mutation.
