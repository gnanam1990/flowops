# ADR-0001: Customer-Managed Reference Signer

Status: Accepted for MVP  
Date: 2026-08-11

## Decision

FlowOps will not hold or remotely operate customer private keys. The MVP uses the PRD's Model D: FlowOps evaluates policy and returns a signed authorization envelope; a customer-run reference signer independently validates the envelope and decides whether to broadcast.

The reference signer is a P0 product artifact, not example code. It must be installable as a small container or binary and must support EOA/HSM or customer wallet adapters without sending key material to FlowOps.

## Mandatory local checks

- FlowOps tenant and signer trust root
- organization, agent, task, action, and rail IDs
- canonical intent digest and policy version
- recipient, asset, amount ceiling, chain ID, and contract/resource
- issued-at, expiry, and one-time nonce
- cumulative local caps and allow/deny lists
- customer revocation and task/org freeze
- Base chain-liveness circuit breaker
- simulation/preflight where the rail permits it

The customer can revoke FlowOps trust by removing the trust root or disabling the signer without FlowOps cooperation.

## Consequences

- Avoids FlowOps operational control of customer wallets.
- Narrows the initial ICP to teams willing to run signing infrastructure.
- Makes signer packaging, upgrade safety, nonce durability, and observability adoption-critical.
- FlowOps cannot claim a payment was broadcast merely because it issued an envelope.

## Acceptance gate

No pilot funds until the reference signer passes nonce-once, substitution, stale-policy, expired-envelope, freeze, halt, restart, tampered-envelope, and customer-revocation tests.
