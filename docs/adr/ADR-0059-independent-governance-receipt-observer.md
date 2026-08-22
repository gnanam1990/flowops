# ADR-0059: Governance workflow completion uses an independent receipt observer

## Status

Accepted for the pre-alpha control-plane implementation.

## Decision

No HTTP client, Safe relayer, or keeper callback may assert governance
completion. An internal worker discovers the exact workflow-binding log through
the independently configured Base RPC quorum, re-reads the successful receipt
and canonical block, applies the closed workflow-kind/contract/selector/event
map, and requires finalized confirmation depth before completing a workflow.
The canonical block timestamp must be strictly later than the durable workflow
approval timestamp; a pre-executed or same-second ambiguous action cannot close
the later approval.

Receipt ownership is claimed atomically with workflow completion and immutable
event/outbox insertion. The ownership identity is chain ID, transaction hash,
and binding-log index, allowing precise log identity while preventing one event
from closing two workflows. RPC disagreement, reorg, missing evidence, or
insufficient finality leave the workflow pending. A quorum-level deterministic
receipt rejection moves the row to `REQUIRES_REAPPROVAL`; minority rejection
does not.

## Consequences

- The observer does not sign or relay governance transactions.
- An absent or partial contract tuple cannot create a permissive fallback.
- Scheduling receipts prove scheduling, not later activation.
- Production still requires reviewed deployed runtimes, provider admission,
  production-equivalent Safe/keeper evidence, and independent review.
- Cycle telemetry distinguishes temporary quorum/finality deferral from
  deterministic receipt rejection; rejected rows terminalize and cannot starve
  later bounded batches. A keyset cursor rotates across deferred rows, so a full
  oldest batch cannot permanently hide newer finalized receipts.
- Durable `SUBMITTED`, `CONFIRMED`, and `FINALIZED` transitions are implemented,
  including atomic recovery of missing intermediate transitions. Automated
  Safe fee-bump/resubmission and post-finalization reorg recovery remain later
  execution/reconciliation work.
