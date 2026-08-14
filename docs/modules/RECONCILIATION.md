# Base reconciliation and halt-safety module

Status: production observer, signer receipt registration, receipt/finality
worker wiring, durable CallEscrow intent and transition reconciliation, and
live Evidence Fetch release and acknowledged-refund manifests complete;
dedicated provider selection and funded signer proof remain open

Packages: `internal/reconciliation`, `internal/controlplane`, `pkg/referencesigner`

Read-only observer: `cmd/base-observer`

Continuous runtime: `internal/reconciliation.Supervisor`, started by
`cmd/control-plane-api`

Receipt/finality runtime: `internal/reconciliation.Worker`, started by
`cmd/control-plane-api`

Operator client: `cmd/flowops-operator`

Escrow lifecycle verifier: `cmd/escrow-conformance`

## Purpose

This module keeps FlowOps honest when an RPC is reachable but Base is stale, disputed, halted, or recovering. It combines:

- independent Base head and canonical-anchor observations;
- a fail-closed chain gate shared by authorization issuance and the customer reference signer;
- durable broadcast and ambiguous-execution state;
- quorum receipt validation for native-USDC transfers;
- continuous durable receipt finalization and bounded reorg monitoring;
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

The customer signer uses a hash-chained one-way attempt journal. It stores the
exact prepared transaction before network access, rechecks the current FlowOps
trust root and every local halt/freeze/time gate, synchronizes `BROADCASTING`,
and then crosses the wallet boundary once. Restart or any error after that
boundary becomes `AMBIGUOUS`; only receipt delivery is retried.

The customer signer registers a domain-separated signed broadcast receipt at
`POST /v1/signer/broadcasts`. FlowOps resolves the durable authorization and
derives the expected organization, agent, task, intent, chain, asset, recipient,
and amount itself; the signer supplies only its signed transaction hash, sender,
outcome, and broadcast time. One authorization deterministically maps to one
execution. A callback arriving after a chain halt is retained as
`PENDING_CHAIN_RECOVERY`, because the wallet may already have submitted it.
The escrow signer posts the same proof shape to
`POST /v1/signer/escrow-broadcasts`; FlowOps binds it to the exact issued
escrow terms and registers only a FUND candidate. x402 facilitator settlements
still require their own protocol-aware registration.
The hash-chained execution event preserves the exact authorization, signed
receipt, and verifying public key. The reconciliation engine independently
recomputes the authorization digest, re-verifies the signature, and matches all
executable economic fields before accepting the event. Key removal stops new
callbacks but does not erase historical proof.

The runtime worker scans only journaled `BROADCAST` and
`PENDING_CHAIN_RECOVERY` executions; it cannot create a payment attempt and
never rebroadcasts one. Missing receipts, timeouts, partial responses, and
provider disagreement leave the execution unresolved. A successful direct-USDC
receipt produces a deterministic settlement transaction over
`agent_service_expense` and `pending_settlement`; a reverted receipt produces no
ledger entry. Both paths still pass through the engine's quorum validation and
single durable append.

Settled executions remain under reorg watch until independent providers confirm
the original block hash at the configured lookback depth. That positive
canonical evidence and the minimum agreed head are journaled on the execution,
so restart does not poll old settlements forever. A conflicting canonical hash
at that depth triggers the exact correction path. Reorganizations deeper than
the configured lookback remain an explicitly accepted residual risk for the
capped pilot and require operator incident handling.

## CallEscrow lifecycle evidence

The escrow adapter accepts a completed, strict lifecycle manifest and
queries two to five independent Base RPC providers for every transaction. It
does not accept a wallet key and cannot sign, submit, retry, release, or refund.

The control plane also registers one immutable escrow intent from an issued,
still-valid exact authorization before broadcast. Its signed terms bind the
contract, buyer, provider, call ID, digests, deadlines, and release window.
Admission also requires the exact configured reviewed deployment contract,
asset, and immutable release window; omitting that complete tuple disables
escrow. After broadcast, a step-up-authenticated Owner or Admin supplies only
the action-specific dynamic fields and transaction hash; the registry
reconstructs all immutable receipt expectations and persists the candidate
before the continuous worker queries it. Cross-tenant IDs, reordered actions,
altered terms, duplicate hash reuse, and terminal replay are refused.

For every successful transition it binds the canonical transaction and block to
the deployed escrow contract, asset, call ID, buyer, provider, amount, task and
request digests, deadlines, delivery digests, release path, or refund origin as
applicable. Funding, release, and refund require exactly one matching USDC
`Transfer` before exactly one matching CallEscrow transition event. Removed,
duplicated, substituted, out-of-order, reverted, under-confirmed, or
provider-disputed evidence is refused.

The manifest accepts only these state paths:

- `FUND -> REFUND` from `Funded`;
- `FUND -> ACKNOWLEDGE -> REFUND` from `Acknowledged`; or
- `FUND -> ACKNOWLEDGE -> DELIVER -> RELEASE`.

The CLI remains a portable proof for a completed lifecycle. The continuous
worker now persists canonical funding, acknowledgement, delivery, release, and
refund transitions in the hash-chained journal. Funding, release, and refund
ledger entries are created only with matching quorum evidence. Confirmed
transitions remain under bounded reorg watch; a removed transition reverses all
dependent ledger effects and quarantines the call instead of inventing a
replacement outcome.

Run the completed manifest against independent public endpoints with:

```sh
go run ./cmd/escrow-conformance \
  -manifest /path/to/local-lifecycle.json \
  -rpc alpha=https://FIRST_PUBLIC_BASE_RPC \
  -rpc beta=https://SECOND_INDEPENDENT_BASE_RPC
```

The manifest is local evidence, not a signing file. It uses canonical lowercase
addresses and hashes, atomic integer USDC amounts, the complete immutable call
snapshot repeated for each transition, and each already-broadcast transaction
hash. Do not commit a pilot manifest until every field and receipt is verified.

The committed successful-release and acknowledged-refund manifests are
documented in `docs/evidence/CALL_ESCROW_EVIDENCE_FETCH_LIVE_2026-08-14.md`
and `docs/evidence/CALL_ESCROW_EVIDENCE_FETCH_REFUND_2026-08-14.md`.

## Ledger invariants

- All amounts are canonical signed integer base units; zero, decimals, floats, and non-canonical encodings are rejected.
- Every transaction has 2 to 32 postings that sum exactly to zero.
- Transaction IDs are idempotent: identical replay returns the original, changed content conflicts.
- Balances are organization-scoped.
- Corrections append an exact reversing transaction and preserve the original.
- The hash-chained journal is synchronized before state becomes visible and locked to one process.

## Halt invariants

- No new executable authorization or signer approval while the state is not `HEALTHY`.
- No FlowOps-authorized new wallet broadcast while paused. A cryptographically
  attested callback for a transaction that may already have been submitted is
  still journaled directly as `PENDING_CHAIN_RECOVERY`.
- No stale settlement or wall-clock refund recognition.
- No blind rebroadcast: an identical registration returns the existing ambiguous execution.
- A customer-signer authorization issued before the latest healthy recovery epoch is refused and must be re-evaluated and reissued.
- Reservations, expected transaction data, last trusted checkpoint, and journal evidence survive restart.
- Quarantined executions require a separate operator resolution workflow and are not silently reopened.

## Verification

```sh
go test -race ./internal/reconciliation ./internal/controlplane ./internal/controlapi ./pkg/referencesigner ./pkg/broadcastreceipt
make smoke-reconciliation
make smoke-escrow-reconciliation
make smoke-escrow-durable
```

The smoke drill enters healthy state, registers a broadcast, simulates responsive-but-stale observers, reaches `HALTED`, proves stale finalization/refund/rebroadcast are refused, resumes observations, reconciles one canonical outcome, and requires manual release without double-posting.

The worker smoke cases additionally prove deterministic receipt settlement,
durable positive-finality checkpoints, and atomic reorg reversal.

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
- complete the separately approved funded Sepolia proof for the customer-side
  escrow executor; local tests already validate signed terms and exact calldata,
  and the registry itself never broadcasts;
- add funding, unknown-transfer, transaction-replacement, and dropped-transaction workflows;
- assess and document production Clef/HSM operations for the runnable
  customer-side signer packaging; the deployed no-funds pilot worker remains idle until a
  design partner provisions a signer receipt public key and supplies a real
  transaction;
- expose status, exceptions, backfill progress, and manual gates in the dashboard;
- execute the live halt/recovery acceptance run with the customer signer and a real Sepolia transaction.

The module supplies P0 halt-safe refusal and a deterministic manual recovery kernel. Automated provider scoring and operator-free resume are intentionally not claimed.
