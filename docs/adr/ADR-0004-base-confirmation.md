# ADR-0004: Base Confirmation and Canonical Evidence

Status: Accepted in principle; numeric policy pending measurement  
Date: 2026-08-11

## Decision

FlowOps separates transaction broadcast, sequencer inclusion, L2 confirmation, and the product's chosen finality threshold. A single RPC response, transaction hash, or HTTP 200 is not settlement evidence.

P0 uses at least two operationally independent Base RPC providers. Every chain checkpoint records provider, chain ID, block number/hash, observed time, and confidence level. Provider disagreement pauses autonomous finalization and escalates to operator review.

## Ledger rule

The operational ledger can move to `SETTLED`, `RELEASED`, or `REFUNDED` only after canonical receipt/log validation at the configured confidence. Reorged or missing evidence reopens reconciliation state; history remains append-only.

## Open measurements

- inclusion and confirmation threshold by rail
- maximum tolerated provider skew
- reorg lookback and checkpoint interval
- degraded-mode read/write behavior
- provider pair and optional third observer

These values must be measured on Base Sepolia and justified before mainnet; they are not copied from Arc.

## Acceptance gate

Tests must cover responsive-but-stale RPCs, conflicting heads, removed logs, transaction replacement, dropped transactions, reorged receipts, delayed indexing, and deterministic replay from the last trusted checkpoint.
