# ASCP governance receipt observer

This module closes approved chain-backed proposal workflows without trusting a
caller-supplied receipt or transaction hash. The internal worker lists every
`APPROVED_PENDING_CHAIN` workflow (hard-capped at 1,000 rather than silently
starving work), discovers its exact `GovernanceWorkflowBound` log from two to
five independent Base RPC providers, and requires a configured quorum.
One canonical evidence group must reach quorum; a dissenting minority cannot
veto it, while two independently qualifying groups remain ambiguous and fail
closed.

For every provider the observer verifies the configured Base chain, reviewed
contract address, exact workflow ID and payload hash, allowed function selector,
and scans the configured deployment history in bounded 10,000-block windows.
This preserves duplicate and premature-binding detection without relying on an
oversized provider-specific log query. It then verifies the
successful receipt, canonical block hash, binding-log identity, and the paired
action-specific event emitted before the binding. The canonical block timestamp
must be strictly later than the durable dual-control approval timestamp, so a
governor cannot pre-execute a visible proposal and have it accepted later. It rejects removed logs,
multiple matching bindings, malformed log identity, RPC disagreement, failed
transactions, noncanonical blocks, and insufficient finality. Nonce invalidation
may contain a contiguous run of 1–100 action events immediately before its
binding; every other supported selector requires the action event immediately
before its binding. Unrelated calls of the same type elsewhere in a Safe batch
do not contaminate the pair.

Cycle telemetry separates completed, temporarily deferred, and deterministically
rejected observations. A malformed finalized receipt or observer disagreement
therefore remains pending for retry but is not hidden in the ordinary RPC-lag
count.

The selector map is intentionally closed:

- payout change: directory `approveVersion`;
- signer caps: spend-module `scheduleCaps`;
- verifier governance: escrow `addVerifier` or `revokeVerifier`;
- break glass: escrow or spend-module emergency pause;
- module governance: authorizer, escrow allowlist, or nonce invalidation;
- directory cancel: directory `cancelVersion`.

Scheduled verifier, cap, and directory changes complete the workflow at their
finalized scheduling receipt. The observer does not report the later activation
as effective; authority/root/cap reconciliation remains a separate projection.
Unmapped Safe-module lifecycle, deployment, directory-authority rotation, and
AgentRegistry-admin rotation remain fail closed.

This pre-alpha slice closes the existing `APPROVED_PENDING_CHAIN` state directly
to `APPROVED` after finalized observation. It does not yet implement the richer
`SUBMITTED`/`CONFIRMED`/`FINALIZED` execution lifecycle, keeper resubmission, or
post-finalization reorg recovery required by the v3.3 execution-state module.

Migration `0028_ascp_governance_receipt_ownership.sql` claims
`(chain_id, transaction_hash, binding_log_index)` in the same PostgreSQL
transaction that completes the workflow and writes its immutable event/outbox.
Exact retries return the recorded result; a second workflow cannot own the same
receipt. The runtime role has only `SELECT` and `INSERT` on this immutable table.

Configure all of `FLOWOPS_ASCP_CALL_ESCROW_CONTRACT`,
`FLOWOPS_ASCP_SPEND_MODULE_CONTRACT`, `FLOWOPS_ASCP_DIRECTORY_CONTRACT`, and
`FLOWOPS_ASCP_GOVERNANCE_FROM_BLOCK` to enable the worker. A partial tuple fails
startup; an absent tuple leaves chain completion fail closed.

Focused validation:

```sh
go test -race ./internal/reconciliation ./internal/ascpworkflow ./internal/ascpgovernanceobserver ./cmd/control-plane-api
```
