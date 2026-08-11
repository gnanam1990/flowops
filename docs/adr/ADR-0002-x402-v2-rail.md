# ADR-0002: x402 V2 Rail on Base

Status: Accepted for Base Sepolia; production facilitator open  
Date: 2026-08-11

## Decision

FlowOps will implement x402 V2 using the current modular x402 packages and CAIP-2 network IDs. The first settlement scheme is EVM `exact` with EIP-3009 USDC on Base Sepolia and then Base mainnet.

The public `https://x402.org/facilitator` is selected only for the Phase 0 Base Sepolia experiment. A production hosted facilitator remains an explicit vendor decision and must pass the same conformance, availability, idempotency, and attribution checks.

## Existing-code disposition

Snapfall's V1 sidecar is not ported as the protocol implementation. Its canonical intent binding, post-sign error taxonomy, hostile-response tests, no-fake-settlement rule, and EIP-712 domain tests should be migrated into the new rail's test suite.

## Required states

- `QUOTED`
- `AUTHORIZED`
- `SUBMITTED`
- `PENDING_CHAIN`
- `SETTLED`
- `REVERTED`
- `DROPPED_OR_REPLACED`
- `UNKNOWN_REQUIRES_RECONCILIATION`

Resource delivery must not transform an unknown broadcast into a settled payment. A database receipt is never sufficient without canonical Base evidence.

## Builder Code posture

FlowOps registers a client service code as `s`; Evidence Fetch declares its app code as `a`; facilitator `w` is observed, not assumed. No transaction is “attributed” until its suffix is parsed from canonical calldata.

## Acceptance gate

Base Sepolia success, rejection, timeout, replay, response loss, facilitator error, recipient substitution, price increase, chain halt, and Builder Code parsing must all pass before mainnet.
