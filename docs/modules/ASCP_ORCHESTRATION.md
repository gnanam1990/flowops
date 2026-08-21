# ASCP policy, approval, and authorization orchestration

## Entry points

Authenticated agents use:

- `POST /agent/v1/intents/{operationId}/evaluate`;
- `GET /agent/v1/intents/{operationId}/decision`;
- `POST /agent/v1/intents/{operationId}/authorization`; and
- `GET /agent/v1/intents/{operationId}/authorization`.

Human members use `GET /v1/ascp/approvals/{approvalId}`. An Owner, Admin,
Finance, or Approver with fresh step-up authentication decides the exact
snapshot through `POST /v1/ascp/approvals/{approvalId}/decision` and a scoped
idempotency key.

## Trusted derivation

The caller supplies only an opaque operation ID, or an approval action plus the
displayed review-snapshot hash. `ascporchestration` reconstructs all economic
inputs from tenant- and actor-scoped SQL:

1. exact persisted SellerQuote, canonical PurchaseSpec bytes, and request body;
2. active organization/agent policy and its configuration hash;
3. economically live task/day reservations for policy preflight;
4. a server-derived organization domain and EIP-712 ExecutionCommitment;
5. immutable escrow deadlines constrained by the deployed contract; and
6. the exact human review snapshot or a distinct automatic-decision reference.

One serializable transaction locks the intent and inserts the append-only policy
decision plus any required approval. Replays return that original decision even
if the active policy is later removed. Decisions cannot be updated or deleted.

Authorization first returns an already-created result. A new authorization
requires a still-live commitment and quote. `ascpexecauth` then revalidates the
current agent, organization pause, active policy hash, canonical PurchaseSpec,
finalized directory head/evidence, seller terms, approval/automatic-decision
binding, execution snapshot, and all five server-derived budget dimensions in
the same serializable transaction as the reservation.

## Outcomes and failures

- `DENY`: immutable reason, no approval, authorization, or reservation.
- `REQUIRE_APPROVAL`: one expiring `REQUESTED` approval; authorization remains
  unavailable until the exact snapshot is approved.
- `AUTO_APPROVE`: no human approval row; the execution row references the
  append-only automatic decision.
- A changed policy, seller, directory, pause state, commitment window, approval,
  or budget condition prevents a new executable authorization.
- Infrastructure ambiguity rolls back. Demonstrated business mismatch may
  persist `INVALIDATED`; it never creates a reservation.
- Cross-organization and cross-agent identifiers return the not-found boundary.

This module does not activate a signer handle, sign, broadcast, settle, release,
refund, reconcile, or post the operational ledger.

## Verification

Focused race tests cover derived IDs, checked deadline arithmetic,
automatic-decision binding, REST/MCP delegation, reservation expiry at both
signer-activation boundaries, and negative authorization states. A real
PostgreSQL test applies every migration and proves human and automatic paths,
parallel evaluation/authorization collapse to one durable result, replay after
policy removal, cross-tenant and cross-agent concealment, expired-pending
approval rejection, exact approval resume, append-only decisions, atomic
reservation, and authorization replay.
