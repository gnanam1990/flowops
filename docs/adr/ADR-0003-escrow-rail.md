# ADR-0003: Optional Delivery-Assured Escrow Rail

Status: Accepted with redesign condition  
Date: 2026-08-11

## Decision

FlowOps will offer an optional task-bound escrow rail for compatible providers. It will reuse Tollbooth's proven primitives: immutable call identity, request/response hashes, price/provider snapshot, buyer acknowledgement release, optimistic release after a response, and exact expiry refund when no response arrives.

Tollbooth's current `Held` dispute state will **not** ship unchanged. It has no resolver and intentionally strands funds. Before pilot, FlowOps must choose one of:

1. launch without buyer disputes, disclosing ack/optimistic/expiry semantics; or
2. add a finite resolver with roles, evidence, deadlines, outcomes, and an appeal/administrative posture reviewed for legal and custody implications.

## Chain-time rule

Expiry and release are onchain state transitions. During a Base halt, FlowOps may show wall-clock lateness but cannot report release or refund until the contract transition is canonically confirmed.

## Supply bootstrap

FlowOps Evidence Fetch is the first compatible provider. It proves delivery of normalized text, HTTP metadata, and a content hash. It never claims the content is true or useful, and never marks failed, empty, oversized, redirected-to-private-network, or unsafe fetches as delivered.

## Acceptance gate

Ported contracts must pass Base-specific unit, fuzz, invariant, Sepolia, reorg, and halt tests plus external security review before handling non-trivial value.
