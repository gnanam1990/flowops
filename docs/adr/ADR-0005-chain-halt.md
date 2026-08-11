# ADR-0005: Base Chain-Halt Safety and Recovery

Status: Accepted with P0/P1 split  
Date: 2026-08-11

## Context

Base's public incident record documents a critical mainnet stall on 25–26 June 2026. FlowOps is Base-only, so it cannot promise payment continuity during a halt.

## Decision

### P0: halt-safe invariants and manual recovery

- Treat old/non-progressing heads or provider disagreement as a suspected stall.
- Pause new autonomous authorization envelopes and signer broadcasts.
- Preserve pending intents, reservations, nonces, evidence, and the last trusted checkpoint.
- Never mark a stale transaction settled, fabricate an escrow release/refund, or blindly rebroadcast.
- Expose the affected layer and last trusted block/time to customers.
- Require a human operator to corroborate recovery on two independent providers, reconcile all ambiguous broadcasts, and explicitly release the pause.

### P1: automated detection and recovery

- Three-observer quorum and provider scoring
- formal `SUSPECTED_STALL` / `HALTED` / `RECOVERING` / `HEALTHY` state machine
- configurable stability window
- automated checkpoint backfill and reorg resolution
- controlled autonomous resume after every ambiguous transaction resolves or is quarantined

## Why split

The P0 properties are mostly refusal, truthful status, and deterministic manual procedure. They protect money without putting a full reconciliation-grade recovery controller on the pilot critical path.

## Acceptance gate

A responsive-but-stale RPC drill must prove no new broadcast, no stale settlement/refund, no duplicate execution, preserved evidence, and a clean manual recovery. Automated staggered-provider recovery is P1.
