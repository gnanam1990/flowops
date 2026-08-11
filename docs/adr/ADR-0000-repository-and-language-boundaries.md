# ADR-0000: Repository and Language Boundaries

Status: Accepted  
Date: 2026-08-11

## Decision

FlowOps is a monorepo with deliberately small language boundaries:

- Go for the control plane, deterministic policy engine, reference signer, reconciliation, and service processes.
- Solidity/Foundry for Base contracts.
- TypeScript/Next.js for the dashboard.
- Current official x402 packages at the protocol boundary; no private x402 fork at project start.

Go owns the canonical authorization-envelope schema. Generated or golden vectors will bind TypeScript and Solidity-facing integrations to the same bytes and digest.

## Rationale

Snapfall's strongest reusable control-plane evidence is in Go, Tollbooth's escrow evidence is in Solidity, and the dashboard needs a browser-native stack. A monorepo keeps revisioned schemas, golden vectors, contracts, and product evidence together without forcing one runtime into every concern.

## Consequences

Every cross-language boundary needs a versioned schema and golden-vector test. No component may reinterpret money, timestamps, chain IDs, recipients, or nonces using language-specific defaults.
