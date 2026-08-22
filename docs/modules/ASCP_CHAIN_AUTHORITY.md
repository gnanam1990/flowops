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
- minimum decoded timelock and documented emergency path.

Actions are separate for CallEscrow verifier governance, ServiceDirectory
publish/cancel/authority rotation/pause/key revocation, AgentRegistry
register/policy/status/admin rotation,
ASCPSpendModule authorizer/allowlist/caps/pause/nonces, Safe module
enable/disable, and Safe owner rotation. An action absent from the installed
matrix cannot be created as a chain workflow.

The release configuration cannot redefine an action's ABI surface. The module
derives the required selector and event topics from a closed action matrix and
rejects a mismatching rule at startup. Safe owner swap requires both its
`RemovedOwner` and `AddedOwner` logs at distinct receipt indexes.

## Completion

The internal observer supplies at least the configured quorum of independent
`AuthorityObservation` records. They must have distinct provider identities
and agree byte-for-byte after provider name is removed. Each proves the exact
transaction and canonical finalized block, contract/code hash, principal,
relayer, selector, events, workflow/payload, log indexes, and decoded timelock.
Only then may `APPROVED_PENDING_CHAIN` become `APPROVED`. Owner HTTP routes do
not expose completion.

Scheduled governance completes the workflow for the scheduling transaction;
it does not claim the delayed value is active. Activation remains a separate
finalized chain observation.

## Failure behavior

Unknown action, malformed matrix, role mismatch, same-person approval, fewer
than two providers, duplicate provider, provider disagreement, wrong code or
principal, relayer substitution, missing/coincident required events, short
timelock, non-final receipt, and receipt replay against another workflow all
fail closed without changing workflow state.

## Operational boundary

`FLOWOPS_ASCP_CHAIN_AUTHORITY_RULES_JSON` is release-manifest configuration and
must not be generated from an Owner request. Retain the signed matrix, contract
source/runtime match, Safe threshold and owner evidence, hot-key rotation
evidence, raw provider receipts/blocks/traces, decoded logs, and rule-change
approval. A rule change is a deployment governance event, not a workflow
payload edit.
