# Agent Registry

## Why and entry

`contracts/src/AgentRegistry.sol` is the on-chain identity and lifecycle record for one organization's agents. It exposes keeper-relayed `register`, `updatePolicyHash`, and `setStatus` actions plus Safe-only `setRegistryAdmin` governance. It holds no assets and has no arbitrary-call, withdrawal, proxy, or upgrade surface.

## Inputs and authorization

Every administrative action carries the shared ASCP v4 `AdminActionAuthorization`. The contract checks its immutable organization domain, own address, chain, registry-admin role, exact function selector, full semantic payload hash, workflow ID, nonzero permanent operation ID, unused role nonce, exact current admin epoch, ECDSA signer, and contract-enforced validity window of at most 600 seconds. Anyone may relay the signed bytes; the event and transaction receipt preserve signer and gas payer separately.

Registration accepts a 1-64 byte display label and nonzero policy hash. Its permanent `agentId` is derived from `ASCP_AGENT_ID_V1`, chain, registry, organization, and the signed admin operation ID. Display labels are untrusted bytes for UI purposes and must always be escaped when rendered.

## Outputs and lifecycle

`getAgent(agentId)` returns the exact label and label hash, current policy hash, status, registration time, and last update time. New agents are Active. Active and Suspended agents may receive policy changes and move between those two states. Either may become Retired; no action can change a Retired agent. Registration count is lifetime count and never decreases.

Events:

- `AgentRegistered` binds agent, policy, label, workflow, admin operation, signing admin, and relayer.
- `AgentPolicyUpdated` binds old and new policy hashes.
- `AgentStatusSet` binds old and new lifecycle states.
- `RegistryAdminSet` binds each monotonic epoch change.

## Failure and recovery

Wrong org/chain/contract/role/selector/payload/workflow/epoch/signer, replayed operation or nonce, expired or oversized bearer windows, unknown agents, no-op changes, and all post-retirement mutations revert without consuming a new authorization. Registry-admin compromise recovery is a governor-Safe rotation; the new epoch invalidates old signatures without making a later A-to-B-to-A assignment revive them.

The local `agents` table is not automatically chain truth. A production indexer must ingest finalized registry events, retain raw observations, reconcile local policy/status projections against chain state, freeze on mismatch, and backfill before readiness. Suspension must fail Ring 1 from that reconciled projection. Until that integration and a production-equivalent keeper proof exist, this contract is a reviewed local module only.

## Verification

- Foundry mutation tests cover exact payload/workflow binding, relayer separation, operation and nonce replay, wrong signer, expiry/window limits, governance, epoch rotation, policy changes, and the complete status lifecycle.
- A 512-run label-boundary fuzz test proves the on-chain size limit.
- Two stateful invariants execute 32,768 generated transitions and prove registration identity/count immutability plus Retired absorption.
- `pkg/adminauthorization` and `vectors/admin-action-authorization-v1.json` reproduce the same EIP-712 domain, struct hash, and digest in Go and Solidity.
- Full Forge, Go race/vet, dashboard, deployment, and readiness gates run before merge.
