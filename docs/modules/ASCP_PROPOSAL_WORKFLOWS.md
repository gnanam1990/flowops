# ASCP proposal workflows

This module implements the owner-facing dual-control governance boundary from
the ASCP PRD. It covers `PAYOUT_CHANGE`, `SIGNER_CAPS`,
`VERIFIER_GOVERNANCE`, `PRODUCTION_GATE`, `BREAK_GLASS`, `ROLE_ADMIN`,
`MODULE_GOVERNANCE`, and `DIRECTORY_CANCEL`.

## HTTP contract

| Method | Path | Purpose |
| --- | --- | --- |
| `POST` | `/v1/workflows` | Create a proposal for a precomputed `payloadHash` |
| `GET` | `/v1/workflows/{workflowID}` | Read a same-tenant workflow and lazily expire it |
| `POST` | `/v1/workflows/{workflowID}/approve` | Apply the second human decision |
| `POST` | `/v1/workflows/{workflowID}/cancel` | Cancel a still-proposed workflow |

Every state-changing request requires `Idempotency-Key`. Decision request
bodies are exactly `{}`; unknown fields are rejected. There is deliberately no
public completion route.

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

Chain-backed workflows transition from `PROPOSED` to
`APPROVED_PENDING_CHAIN`. Only the internal observer boundary, backed by an
independent finalized-receipt quorum, can reach `APPROVED`. There is no public
completion route or caller-supplied receipt path.

Chain governance contracts now recompute the exact approved payload and emit a
shared workflow-binding event with the action event. See
`ASCP_GOVERNANCE_WORKFLOW_BINDINGS.md`. The internal worker discovers the exact
binding and completes only after paired-event, canonical-block, finality, and
atomic one-time receipt ownership checks pass.

## Persistence and operations

Migrations `0027_ascp_proposal_workflows.sql` and
`0028_ascp_governance_receipt_ownership.sql` add the authoritative workflow,
idempotent action, immutable event, and immutable outbox tables. A database
trigger rejects payload or identity changes, deletion, and illegal transitions.
Runtime role setup grants column-level updates only for the reviewed transition
fields. PostgreSQL readiness verifies these exact grants and all four tables.

Run focused validation with:

```sh
go test -race ./internal/ascpworkflow ./internal/ascpgovernanceobserver ./internal/reconciliation ./internal/controlapi ./cmd/control-plane-api
FLOWOPS_TEST_DATABASE_URL="$FLOWOPS_TEST_DATABASE_URL" go test ./internal/controlapi \
  -run TestASCPWorkflowRealPostgresConcurrentDecisionAndImmutableAudit -count=3
deploy/control-plane/test-postgres-readiness.sh
```

The real-PostgreSQL test races twenty approval/cancellation contenders, checks
one durable terminal outcome, verifies exactly one transition event and outbox
record in addition to creation, rejects direct payload mutation and event
deletion, hides cross-tenant reads, and proves exact finalized-receipt replay.
