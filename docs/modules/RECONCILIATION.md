# Base reconciliation and halt-safety module

Status: production runtime wiring implemented; dedicated provider selection and Sepolia measurement remain open

Packages: `internal/reconciliation`, `internal/controlplane`, `pkg/referencesigner`

Read-only observer: `cmd/base-observer`

Continuous runtime: `internal/reconciliation.Supervisor`, started by
`cmd/control-plane-api`

Operator client: `cmd/flowops-operator`

## Purpose

This module keeps FlowOps honest when an RPC is reachable but Base is stale, disputed, halted, or recovering. It combines:

- independent Base head and canonical-anchor observations;
- a fail-closed chain gate shared by authorization issuance and the customer reference signer;
- durable broadcast and ambiguous-execution state;
- quorum receipt validation for native-USDC transfers;
- balanced, append-only operational ledger postings;
- explicit manual halt and recovery release.

It is an operational subledger and settlement-safety boundary, not complete statutory accounting.

## Chain states

- `HEALTHY`: independent observers agree and an operator has released autonomous execution.
- `SUSPECTED_STALL`: startup, restart, stale data, insufficient observers, checkpoint regression, or first disagreement. Authorization, signer broadcast, settlement finalization, and refund recognition fail closed.
- `HALTED`: repeated unhealthy observations or a manual halt. Broadcast executions become `PENDING_CHAIN_RECOVERY`; they are not failed, settled, retried, or released.
- `RECOVERING`: observer agreement has returned, but backfill and ambiguous execution resolution are still in progress. Receipt reconciliation is allowed; new autonomous execution remains paused.

Recovery requires the configured consecutive healthy-observation window, zero unresolved broadcasts, and an explicit named operator release. A process restart from `HEALTHY` returns to `SUSPECTED_STALL`; stale in-memory health is never inherited.

Global halt and resume are protected by a dedicated operator-control key, not a
tenant credential or Sites membership. The `flowops-operator` client reads that
key from the environment, refuses redirects, and sends strict JSON over HTTPS.

## Independent observer contract

Two to five HTTPS providers with distinct names and hostnames are configured. For every snapshot the observer set:

1. verifies `eth_chainId`;
2. reads each latest block;
3. selects the lowest reported head as the comparison anchor;
4. asks every responding provider for that exact block;
5. emits provider-specific head and anchor number, hash, block time, and observation time.

The engine rejects duplicate providers, stale observations, future timestamps, excessive head skew, anchor disagreement, and regression/conflict with the last trusted checkpoint. Distinct hostnames are a configuration floor; operators must still verify that the providers are operationally independent.

The production supervisor runs once immediately and then on a fixed interval.
It persists empty or partial observations so provider outages advance the same
fail-closed state machine as disagreements. It reports only provider counts;
secret-bearing URLs and detailed transport errors are not API fields or
supervisor log attributes. Durable journal failure stops the service.

## Receipt and USDC verification

A successful execution can become `SETTLED` only when the configured provider quorum agrees on:

- Base chain and transaction hash;
- successful receipt status;
- block number and block hash;
- configured confirmation floor;
- exactly one non-removed native-USDC `Transfer(address,address,uint256)` log matching the expected payer, recipient, asset, and integer amount;
- a block at or behind the last trusted canonical checkpoint.

The settlement ledger transaction is written in the same durable event as the resolved execution. Replaying the same input is idempotent; changing evidence or postings under the same execution is a conflict. Direct settlement, refund, or funding postings without a canonical-evidence path are refused.

If the configured quorum later reports a different canonical hash at the original settlement height beyond the reorg lookback, FlowOps atomically reopens the execution, appends an exact correction that reverses the prior ledger entry, enters `RECOVERING`, and requires a fresh canonical outcome or quarantine before manual resume. The original settlement and its timestamps remain in the journal.

## Ledger invariants

- All amounts are canonical signed integer base units; zero, decimals, floats, and non-canonical encodings are rejected.
- Every transaction has 2 to 32 postings that sum exactly to zero.
- Transaction IDs are idempotent: identical replay returns the original, changed content conflicts.
- Balances are organization-scoped.
- Corrections append an exact reversing transaction and preserve the original.
- The hash-chained journal is synchronized before state becomes visible and locked to one process.

## Halt invariants

- No new executable authorization or signer approval while the state is not `HEALTHY`.
- No new broadcast registration while paused.
- No stale settlement or wall-clock refund recognition.
- No blind rebroadcast: an identical registration returns the existing ambiguous execution.
- A customer-signer authorization issued before the latest healthy recovery epoch is refused and must be re-evaluated and reissued.
- Reservations, expected transaction data, last trusted checkpoint, and journal evidence survive restart.
- Quarantined executions require a separate operator resolution workflow and are not silently reopened.

## Verification

```sh
go test -race ./internal/reconciliation ./internal/controlplane ./pkg/referencesigner
make smoke-reconciliation
```

The smoke drill enters healthy state, registers a broadcast, simulates responsive-but-stale observers, reaches `HALTED`, proves stale finalization/refund/rebroadcast are refused, resumes observations, reconciles one canonical outcome, and requires manual release without double-posting.

For a read-only live snapshot using public endpoints:

```sh
go run ./cmd/base-observer \
  -chain-id 84532 \
  -rpc alpha=https://FIRST_PUBLIC_BASE_SEPOLIA_RPC \
  -rpc beta=https://SECOND_INDEPENDENT_BASE_SEPOLIA_RPC
```

Do not place secret-bearing RPC URLs on a command line. Production endpoints belong in the deployment secret manager.

## Explicit remaining work

- select and contractually assess at least two production Base RPC providers;
- complete and record the multi-hour Sepolia confirmation, stall-age,
  head-skew, reorg-lookback, rate-limit, and recovery-window measurement;
- add contract-specific escrow event reconciliation after the escrow state machine is finalized;
- add funding, unknown-transfer, transaction-replacement, and dropped-transaction workflows;
- expose status, exceptions, backfill progress, and manual gates in the dashboard;
- execute the live halt/recovery acceptance run with the customer signer and a real Sepolia transaction.

The module supplies P0 halt-safe refusal and a deterministic manual recovery kernel. Automated provider scoring and operator-free resume are intentionally not claimed.
