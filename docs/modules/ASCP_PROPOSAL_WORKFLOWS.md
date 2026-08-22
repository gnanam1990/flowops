# ASCP proposal workflows

This module implements the owner-facing dual-control governance boundary from
the ASCP PRD. It covers `PAYOUT_CHANGE`, `SIGNER_CAPS`,
`VERIFIER_GOVERNANCE`, `PRODUCTION_GATE`, `BREAK_GLASS`, `ROLE_ADMIN`,
`MODULE_GOVERNANCE`, and `DIRECTORY_CANCEL`.

## HTTP contract

| Method | Path | Purpose |
| --- | --- | --- |
| `POST` | `/v1/workflows` | Create a proposal for an exact `workflowId` + typed governance action |
| `GET` | `/v1/workflows/{workflowID}` | Read a same-tenant workflow and lazily expire it |
| `POST` | `/v1/workflows/{workflowID}/approve` | Apply the second human decision |
| `POST` | `/v1/workflows/{workflowID}/cancel` | Cancel a still-proposed workflow |

Every state-changing request requires `Idempotency-Key`. Decision request
bodies are exactly `{}`; unknown fields are rejected. There is deliberately no
public completion route.

For every chain-backed kind, the create body must contain a caller-generated,
nonzero 32-byte `workflowId` plus exactly one action variant. It must omit
`payloadHash`: `pkg/governanceworkflow` derives the payload hash, selector, and
full calldata from the typed values and persists the canonical action. The
configured governance gate then requires the exact Base network, reviewed
contract, workflow-kind, and selector tuple. Missing configuration returns
`GOVERNANCE_TARGETS_UNAVAILABLE`; substituted targets are invalid. Local
`PRODUCTION_GATE` and `ROLE_ADMIN` workflows omit `workflowId` and receive a
server-generated ID; they use a caller-supplied local payload hash and no chain
action.
Directory-approval actions carry the immutable proposal fields and proposer
nonce; the server derives the expected proposal hash used in calldata. Callers
cannot supply that hash independently. The service repeats exact action binding
and the configured target gate immediately before approval writes an execution
command.

## Role and state rules

- Payout: `SELLER_ADMIN` proposes; another `SELLER_ADMIN` or `ORG_ADMIN`
  approves.
- Signer caps, verifier governance, and module governance:
  `SIGNER_OPERATOR` proposes; `ORG_ADMIN` approves.
- Production gate and role administration: `ORG_ADMIN` proposes; a different
  `ORG_ADMIN` approves.
- Break glass: `ORG_ADMIN` proposes; `INCIDENT_RESPONDER` approves.
- Directory cancellation: `SELLER_ADMIN` proposes; `ORG_ADMIN` approves.

Proposal, approval, and cancellation require a human credential whose recorded
step-up occurred within five minutes and remains valid. Proposer self-approval
is rejected. Proposals expire after exactly 24 hours. Competing approval and
cancellation transactions lock the same row and converge on one terminal
outcome.
An exact replay that only returns an already-recorded create/decision outcome
does not require a new step-up ceremony, and its idempotency key remains bound
to that request; any changed workflow or action conflicts.

Chain-backed workflows transition as
`PROPOSED → APPROVED_PENDING_CHAIN → SUBMITTED → CONFIRMED → FINALIZED`.
Approval atomically emits `ascp.governance.execute`. The command is
`ASCP_GOVERNANCE_EXECUTION_V1` and contains the exact target, zero value, `CALL`
operation, selector, calldata, canonical action, payload hash, and approval
identity. Its `executeAfter` is one second later than the durable approval, so
a compliant relayer cannot create the same-second receipt ambiguity that the
observer must reject. Consumers must not rebuild bytes from mutable state. Internal relayer
and reconciler methods record the exact transaction hash and confirmation; only the
independent finalized-receipt quorum reaches `FINALIZED`. If the process was
offline, final receipt recovery reconstructs any missing intermediate events in
the same transaction. `REVERTED`, `REORGED`, `TIMED_OUT`, and
`REQUIRES_REAPPROVAL` are explicit side states with closed reason codes. There
is no public completion route or caller-supplied receipt path. The current
lifecycle rejects generic submission directly from `REORGED` or `TIMED_OUT`:
an outer transaction hash alone cannot prove an unchanged Safe retry. The
governance Safe relayer opens only its dedicated proven-retry transition after
persisting exact approved Safe bytes, Safe nonce, current precondition, and
independent retry-classification evidence required by AC-83. The database
trigger joins that proof to the durable relay job before accepting `SUBMITTED`.

Chain governance contracts now recompute the exact approved payload and emit a
shared workflow-binding event with the action event. See
`ASCP_GOVERNANCE_WORKFLOW_BINDINGS.md`. The internal worker discovers the exact
binding and completes only after paired-event, canonical-block, finality, and
atomic one-time receipt ownership checks pass.

## Persistence and operations

Migrations `0027_ascp_proposal_workflows.sql`,
`0028_ascp_governance_receipt_ownership.sql`, and
`0029_ascp_governance_action_lifecycle.sql`, and
`0030_ascp_governance_safe_relayer.sql` add the authoritative workflow,
idempotent action, immutable event, and immutable outbox tables. A database
trigger rejects payload or identity changes, deletion, and illegal transitions.
Runtime role setup grants column-level updates only for the reviewed transition
fields. The separately capped governance-relayer role owns only relay and
proof-bound workflow transitions.
Migration 0029 fails old untyped live chain rows closed: unapproved rows become
`EXPIRED`, approved rows become `REQUIRES_REAPPROVAL/PRECONDITION_CHANGED`, and
both receive migration action/event/outbox records. Existing finalized history
is preserved, including legacy immutable `COMPLETE`/`APPROVED` audit values.

The current separation rule proves different proposer and approver identities.
The wider PRD deny-overrides rule for recent authors of affected policies or
directory versions remains a tracked acceptance gap until those surfaces have
one authoritative authorship registry.

Run focused validation with:

```sh
go test -race ./internal/ascpworkflow ./internal/ascpgovernanceobserver ./internal/reconciliation ./internal/controlapi ./cmd/control-plane-api
FLOWOPS_TEST_DATABASE_URL="$FLOWOPS_TEST_DATABASE_URL" go test ./internal/controlapi \
  -run TestASCPWorkflowRealPostgresConcurrentDecisionAndImmutableAudit -count=3
deploy/control-plane/test-postgres-readiness.sh
```

The real-PostgreSQL test races twenty approval/cancellation contenders, checks
one durable outcome, verifies the chain execution command, rejects direct
payload mutation and event deletion, hides cross-tenant reads, and proves the
submitted/confirmed/finalized event sequence plus exact finalized-receipt replay.
