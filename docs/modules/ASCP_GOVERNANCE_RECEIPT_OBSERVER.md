# ASCP governance receipt observer

This module closes approved chain-backed proposal workflows without trusting a
caller-supplied receipt or transaction hash. The internal worker lists every
active chain workflow in bounded batches of at most 1,000, discovers its exact
`GovernanceWorkflowBound` log from two to
five independent Base RPC providers, and requires a configured quorum.
The worker advances a keyset cursor after every batch and wraps at the end, so
1,000 older deferred workflows cannot permanently starve newer rows.
One canonical evidence group must reach quorum; a dissenting minority cannot
veto it, while two independently qualifying groups remain ambiguous and fail
closed. A quorum that validates a receipt and a separate quorum that
deterministically rejects it is likewise an observer disagreement; neither side
is allowed to advance or terminalize the workflow.

For every provider the observer verifies the configured Base chain, exact
server-persisted reviewed contract and function selector, workflow ID and payload hash,
and scans the configured deployment history in bounded 10,000-block windows.
One observation accepts at most 1,000 windows; a provider head that would cause
a larger allocation or scan is rejected as unavailable instead of exhausting
memory or monopolizing the worker.
This preserves duplicate and premature-binding detection without relying on an
oversized provider-specific log query. It then verifies the
successful receipt, canonical block hash, binding-log identity, and the paired
action-specific event emitted before the binding. The canonical block timestamp
must be strictly later than the durable dual-control approval timestamp, so a
governor cannot pre-execute a visible proposal and have it accepted later. It rejects removed logs,
multiple matching bindings, malformed log identity, RPC disagreement, failed
transactions, noncanonical blocks, and insufficient finality. Nonce invalidation
may contain a contiguous run of 1–100 action events immediately before its
binding, and the run length must equal the exact approved nonce list; every
other supported selector requires the action event immediately
before its binding. Unrelated calls of the same type elsewhere in a Safe batch
do not contaminate the pair.

Cycle telemetry separates completed, temporarily deferred, and deterministically
rejected observations. Observer disagreement and insufficient quorum remain
deferred. Only a quorum-level deterministic receipt rejection moves the
workflow to `REQUIRES_REAPPROVAL`; that terminalized row leaves the pending
queue, so it cannot starve later batches.

The observer's chain/contract/selector map is also the proposal-time action
gate. A typed action for another Base network, an arbitrary contract, or a
selector outside its exact workflow kind is rejected before creation and can
never produce an execution outbox command.
The persisted action is independently rebound to the exact payload hash and
calldata before observation, so shape-valid but inconsistent JSONB cannot be
used as completion evidence.

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

Normal relayer and reconciler boundaries durably record `SUBMITTED` and
`CONFIRMED`; the receipt observer records `FINALIZED`. After process downtime,
one atomic completion transaction reconstructs missing submitted and confirmed
events before finalization. Revert, reorg, timeout, and reapproval states are
durable and reason-coded. Automated Safe fee-bump/resubmission and
post-finalization reorg recovery remain separate execution/reconciliation work;
this observer never invents a replacement owner signature. The lifecycle also
rejects a new outer transaction hash directly from `REORGED` or `TIMED_OUT`;
those retries remain closed until the relayer can prove byte-identical approved
Safe bytes and nonce state under AC-83.
Each observation still scans deployment history in bounded RPC windows rather
than consuming a durable indexed log projection. Production-scale history and
AC-34 load readiness therefore require a later persistent index/cursor plus
provider-limit and restart evidence; the worker timeout remains fail closed.

Migrations `0028_ascp_governance_receipt_ownership.sql` and
`0029_ascp_governance_action_lifecycle.sql` claim
`(chain_id, transaction_hash, binding_log_index)` in the same PostgreSQL
transaction that completes the workflow and writes its immutable event/outbox.
Exact retries return the recorded result; a second workflow cannot own the same
receipt. The runtime role has only `SELECT` and `INSERT` on this immutable table.
The lifecycle migration also terminalizes pre-upgrade live workflows that lack
server-derived action/calldata; it never treats their old caller hash as an
executable command.

Configure all of `FLOWOPS_ASCP_CALL_ESCROW_CONTRACT`,
`FLOWOPS_ASCP_SPEND_MODULE_CONTRACT`, `FLOWOPS_ASCP_DIRECTORY_CONTRACT`, and
`FLOWOPS_ASCP_GOVERNANCE_FROM_BLOCK` to enable the worker. A partial tuple fails
startup; an absent tuple leaves chain completion fail closed.

Focused validation:

```sh
go test -race ./internal/reconciliation ./internal/ascpworkflow ./internal/ascpgovernanceobserver ./cmd/control-plane-api
```
