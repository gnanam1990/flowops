# ASCP asset-health and token-blocked accounting

AC-26 is implemented as a fail-closed Base USDC dependency boundary. The
separately supervised `cmd/ascp-asset-health` process has no wallet key and no
broadcast path. At one finalized block from each of two to five independent
HTTPS RPC providers, it verifies:

- the EIP-1967 proxy implementation address and the implementation runtime-code
  hash against pinned deployment configuration;
- `paused()` and `isBlacklisted(address)` for the configured buyer and escrow;
- a zero-value transfer simulation from that buyer to that escrow; and
- exact agreement on chain, asset, finalized block/hash, implementation, code,
  and control-surface results.

The durable state machine is `NORMAL`, `TOKEN_PAUSED`,
`ASSET_TRANSFER_BLOCKED`, and `RECOVERING`. A missing row, a non-`NORMAL` row,
or a stale `NORMAL` observation rejects a new execution authorization inside
the same serializable transaction that would reserve budget. Provider failure
cannot refresh the observation, so the authorization gate closes when the
bounded observation age expires. Authorization freshness is hard-capped at one
minute even if another observation consumer is configured with a longer bound.

## Accounting behavior

Entering either blocked state atomically appends a balanced classification for
every finalized open escrow position:

```text
Dr TokenBlockedRestrictedUSDC
Cr EscrowRestrictedUSDC
```

`refund_due` is recorded when `settle_by` has passed. The entry does not claim
that a refund or settlement happened on-chain. `ascp_classified_ledger_postings`
is the canonical statement view combining the settlement journal and this
append-only classified subledger. Recovery appends the exact inverse; it never
updates or deletes the blocked entry. `ascp_token_blocked_positions` is the
current open-position view and derives `refund_due` from `settle_by`, including
positions that became due after the original block classification.

## Recovery contract

A clean quorum after a blocked state moves only to `RECOVERING`. The existing
settlement worker then rechecks every finalized attempt whose canonical check
predates that first clean observation. That recovery anchor is frozen while
later clean polls remain append-only audit evidence; new unhealthy evidence
cancels recovery. A reorg follows the existing reversal and quarantine path.

Recovery can reach `NORMAL` only when a database-derived proof establishes all
of the following as zero:

1. unresolved or structurally incomplete payment operations;
2. finalized attempts without a canonical check at or after the clean
   observation; and
3. finalized open locks without an active token-blocked classification.

The proof binds chain, asset, health epoch, clean observation digest, finalized
block, and reconciliation time. The store locks the health row, recalculates
all three predicates, appends the proof and inverse classifications, and moves
to `NORMAL` in one serializable transaction. Serialization/deadlock conflicts
are retried three times. A caller-supplied boolean, old epoch proof, concurrent
new operation, or stale canonical check cannot resume authorizations.

## Deployment

Apply migration `0029_ascp_asset_health.sql`, create a dedicated LOGIN role
without memberships or owned objects, and apply:

```sh
psql "$MIGRATION_OWNER_DATABASE_URL" \
  --set=asset_health_role="$FLOWOPS_ASSET_HEALTH_DATABASE_ROLE" \
  --file=deploy/control-plane/configure-asset-health-role.sql
```

Run `/flowops/ascp-asset-health` with:

| Variable | Contract |
| --- | --- |
| `FLOWOPS_ASSET_HEALTH_DATABASE_URL` | Dedicated role and exactly `sslmode=verify-full` |
| `FLOWOPS_ASSET_HEALTH_CHAIN_ID` | `8453` or `84532` |
| `FLOWOPS_ASSET_HEALTH_ASSET` | Exact lowercase nonzero USDC proxy address |
| `FLOWOPS_ASSET_HEALTH_PROXY_IMPLEMENTATION` | Reviewed lowercase nonzero implementation address |
| `FLOWOPS_ASSET_HEALTH_RUNTIME_CODE_HASH` | Reviewed lowercase nonzero 32-byte Keccak hash |
| `FLOWOPS_ASSET_HEALTH_BUYER` | Exact operational buyer address used by the transfer probe |
| `FLOWOPS_ASSET_HEALTH_ESCROW` | Exact operational escrow address used by the transfer probe |
| `FLOWOPS_ASSET_HEALTH_RPC_PROVIDERS_JSON` | Strict array of 2–5 independent-name, distinct-host HTTPS providers |
| `FLOWOPS_ASSET_HEALTH_RPC_QUORUM` | Integer from 2 through provider count |
| `FLOWOPS_ASSET_HEALTH_INTERVAL` | Optional `5s`–`1m`, default `30s` |
| `FLOWOPS_ASSET_HEALTH_QUERY_TIMEOUT` | Optional positive duration below interval, default `15s` |
| `FLOWOPS_ASSET_HEALTH_MAX_OBSERVATION_AGE` | Optional interval-or-longer duration through `5m`, default `1m` |

Alert on observation failure, any non-`NORMAL` transition, time spent in
`RECOVERING`, repeated serialization exhaustion, or growth in unresolved and
stale-canonical counts. Restarting the monitor does not clear state or proofs.
Before production value, execute a fork drill for pause, buyer blacklist,
escrow blacklist, implementation/code change, provider outage, and recovery.

Focused automated evidence:

```text
go test -race ./cmd/ascp-asset-health ./internal/ascpassethealth \
  ./internal/ascpsettlement ./internal/reconciliation ./internal/ascpexecauth \
  ./internal/dbreadiness
```

Unit tests prove state, binding, retry-facing, reconciliation, and negative
authorization behavior. They do not claim that a real Circle-controlled pause,
blacklist, provider outage, or production recovery drill has been executed.
