# ADR-0003: Optional Delivery-Assured Escrow Rail

Status: Accepted; v1 redesign selected
Date: 2026-08-11

## Decision

FlowOps will offer an optional task-bound escrow rail for compatible providers. It will reuse Tollbooth's proven primitives: immutable call identity, request/response hashes, price/provider snapshot, buyer acknowledgement release, optimistic release after a response, and exact expiry refund when no response arrives.

Tollbooth's current `Held` dispute state will **not** ship. It has no resolver and intentionally strands funds. FlowOps v1 selects the finite no-dispute design:

- a provider must acknowledge by the acknowledgement deadline, inclusive;
- a provider must submit non-zero response and evidence digests by the delivery deadline, inclusive;
- the buyer may accept and release immediately;
- otherwise, anyone may release only after the disclosed optimistic window;
- a missed acknowledgement or delivery deadline makes an exact buyer-only refund permissionlessly executable; and
- every delivered position has a permissionless path to `Released`, every missed-deadline position has a permissionless path to `Refunded`, and no `Held` state exists.

This does not arbitrate subjective quality and does not let FlowOps redirect funds. Adding rejection, dispute, resolver, pause, fee, or rescue powers requires a new ADR plus security and legal review.

## Chain-time rule

Expiry and release are onchain state transitions. During a Base halt, FlowOps may show wall-clock lateness but cannot report release or refund until the contract transition is canonically confirmed.

## Supply bootstrap

FlowOps Evidence Fetch is the first compatible provider. It proves delivery of normalized text, HTTP metadata, and a content hash. It never claims the content is true or useful, and never marks failed, empty, oversized, redirected-to-private-network, or unsafe fetches as delivered.

## Acceptance gate

Ported contracts must pass Base-specific unit, fuzz, invariant, Sepolia, reorg, and halt tests plus external security review before handling non-trivial value.
