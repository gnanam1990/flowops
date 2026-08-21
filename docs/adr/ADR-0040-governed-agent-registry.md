# ADR-0040: Governed on-chain AgentRegistry

- Status: accepted
- Date: 2026-08-21

## Context

Agent identity, policy binding, suspension, and final retirement need a chain-observable control surface. The registry administrator is a capped hot authority: it must not hold ETH, broadcast transactions, move funds, or rotate itself.

## Decision

Deploy an immutable, single-organization `AgentRegistry` governed by a contract address that is a Safe in production. A registry-admin key signs exact EIP-712 `AdminActionAuthorization` messages and a keeper relays them. Every message binds the organization, chain, registry contract, registry-admin role, function selector, semantic payload, permanent admin operation ID, nonce, monotonic admin epoch, validity window of at most ten minutes, and workflow ID.

Registration derives `agentId` from the organization, chain, registry deployment, and permanent admin operation ID. Agents begin Active. Policy updates are exact-hash replacements. Status may move between Active and Suspended until Retired; Retired is permanently absorbing. Only the governor may rotate the registry admin, and every assignment increments the epoch, including A-to-A and A-to-B-to-A.

## Consequences

- A keeper or public relayer can submit an action but cannot alter it or select another action.
- The registry-admin key has no on-chain fund authority and needs no gas balance.
- Events expose the signing admin and transaction relayer separately for reconciliation.
- Local control-plane agent rows remain an operational projection. Production activation requires a finalized-event indexer, startup/backfill reconciliation, and fail-closed drift handling before the local projection may be treated as the Ring 1 status source.
- The contract and local tests are not an independent audit or production deployment approval.
