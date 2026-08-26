# ASCP Owner chain-authority module

## Purpose

An Owner approval is not chain authority and a transaction hash is not
completion. This module binds every enabled chain-changing workflow to one
deployment-owned authority rule and closes it only from a canonical finalized
receipt proof.

## Contract

The immutable workflow identity is `{organizationId, workflowId, kind,
chainAction, payloadHash, proposer, proposerRole}`. Approval requires a fresh
step-up from the rule's second role and a principal different from the
proposer. The database prevents later substitution of `chainAction` or any
other identity field.

Each `AuthorityRule` names:

- Base chain, target contract and reviewed runtime code hash;
- exact on-chain principal;
- proposer role, approver role and quorum two;
- exact relayer or an explicit any-relayer policy;
- code-fixed function selector, primary action event, any required secondary
  action event, and workflow-binding event;
- minimum observed approval-to-execution delay and documented emergency path.

The executable inventory contains all 26 reviewed contract mutation surfaces.
Ten are currently enabled through the Owner workflow API: CallEscrow verifier
add/revoke/pause, ServiceDirectory publish/cancel, and ASCPSpendModule
authorizer/allowlist/caps/pause/nonce invalidation. Each enabled entry maps the
Owner endpoint, closed typed action, two-person approval, immutable Safe CALL
command, finalized observer quorum, atomic receipt ownership, and append-only
audit records.

The other 16 surfaces are deliberately classified as disabled: directory
authority/overlay operations, AgentRegistry mutations, and Safe module/owner
mutations do not yet have the complete typed action plus independent receipt
lifecycle. A disabled row cannot be installed through
`FLOWOPS_ASCP_CHAIN_AUTHORITY_RULES_JSON`; startup rejects it. This preserves
the reviewed ABI/event inventory without pretending that a contract method is
already an Owner API capability. The machine-readable inventory is
`docs/evidence/AC66_OWNER_CHAIN_API_INVENTORY_2026-08-26.json` and a test locks
it byte-semantically to `OwnerChainActionInventory()`.

The release configuration cannot redefine an action's ABI surface. The module
derives the required selector and event topics from a closed action matrix and
rejects a mismatching rule at startup. Safe owner swap requires both its
`RemovedOwner` and `AddedOwner` logs at distinct receipt indexes.

## Completion

The internal observer supplies at least the configured quorum of independent
`AuthorityObservation` records. At the exact receipt block, every provider
reads target runtime code, the code-fixed `governor()` or `safe()` principal,
and the outer transaction sender. They must have distinct provider identities
and agree byte-for-byte after provider name is removed. Each proves the exact
transaction and canonical finalized block, contract/code hash, principal,
relayer, selector, events, workflow/payload, log indexes, and the observed
approval-to-execution delay.
Only then may `APPROVED_PENDING_CHAIN` become `FINALIZED`. Owner HTTP routes do
not expose completion.

Scheduled governance completes the workflow for the scheduling transaction;
it does not claim the delayed value is active. Activation remains a separate
finalized chain observation.

## Failure behavior

Unknown or disabled action, malformed matrix, role mismatch, same-person approval, fewer
than two providers, duplicate provider, provider disagreement, wrong code or
principal, relayer substitution, missing/coincident required events, short
timelock, non-final receipt, and receipt replay against another workflow all
fail closed without changing workflow state.

## Operational boundary

`FLOWOPS_ASCP_CHAIN_AUTHORITY_RULES_JSON` is mandatory release-manifest
configuration whenever the governance observer tuple enables chain-changing
Owner workflows, and must not be generated from an Owner request. Neither side
can start alone. Missing historical code, principal, or
transaction RPC evidence fails completion closed. Retain the signed matrix, contract
source/runtime match, Safe threshold and owner evidence, hot-key rotation
evidence, raw provider receipts/blocks/traces, decoded logs, and rule-change
approval. A rule change is a deployment governance event, not a workflow
payload edit.
