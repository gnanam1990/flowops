# ADR-0009: Dashboard Control-Plane and Identity Boundary

Status: Accepted
Date: 2026-08-11

## Decision

The TypeScript dashboard is a human control surface, not an independent source
of economic truth. The Go control plane remains canonical for agents, tasks,
policies, approvals, authorization state, reservations, ledger outcomes, Base
observations, and evidence.

The initial dashboard renders a typed immutable preview snapshot and disables
all economic writes. It does not add D1 or R2 persistence. A later adapter may
replace the preview snapshot only through authenticated control-plane reads and
exact command endpoints; browser-local state never establishes approval,
payment, settlement, refund, or pause.

ChatGPT/Sites identity headers may identify and personalize a viewer. They do
not prove FlowOps organization membership, role, approval authority, or signer
control. Production reads and writes require a server-side FlowOps session,
organization membership, role checks, command-specific authorization, and
step-up authentication for emergency or high-risk actions.

## Rationale

Duplicating operational records in a dashboard database creates two ledgers and
makes stale browser or cache state dangerous. Keeping the write boundary in the
control plane preserves the existing policy, approval, nonce, chain-health, and
evidence invariants.

A truthful preview lets the operator workflow and accessibility contract ship
without inventing backend success or weakening custody posture.

## Consequences

- Every displayed record must include freshness and its preview/live status.
- Available, reserved, pending, and unresolved money remain distinct.
- Live commands return durable command IDs and authoritative states; optimistic
  browser success is forbidden for economic actions.
- Emergency pause must require step-up authentication and fail closed if the
  control plane cannot confirm the command.
- Deploying the preview does not make FlowOps pilot-ready.

## Acceptance gate for live mode

Live mode remains blocked until tests prove organization isolation, role and
step-up enforcement, stale-snapshot handling, exact-intent approval binding,
idempotent commands, chain-halt status propagation, audit-log correlation, and
no fabricated success after timeout or control-plane failure.
