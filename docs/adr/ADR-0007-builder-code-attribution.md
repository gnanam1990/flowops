# ADR-0007: Builder Code Issuance, Roles, and Evidence

Status: Partially resolved; safe experiment harness implemented, identity-specific issuance and settlement evidence pending
Date: 2026-08-11

## Confirmed mechanism

Base describes Builder Codes as ERC-721 identifiers obtained through base.dev and associated with app metadata and a payout address. Base also exposes an unauthenticated agent API that deterministically returns a `bc_...` code for an EVM wallet. Current x402 V2 defines:

- `a`: resource-server app code;
- `s`: client/server/facilitator service code array; and
- `w`: facilitator wallet code.

The facilitator encodes present fields into an ERC-8021 Schema 2 suffix and appends it to settlement calldata.

## Unresolved issuance mapping

Public documents do not establish that the dashboard-issued ERC-721 and wallet-derived agent API code have identical token ownership, payout metadata, registry representation, analytics, and rewards semantics. No FlowOps identity or reference-signer wallet is designated, so creating codes now would bind evidence to the wrong identity.

## Intended FlowOps role inventory

| Participant | Intended role | Code policy |
|---|---|---|
| FlowOps control/client layer | x402 `s`; direct transactions when FlowOps is the submitter | Dedicated FlowOps service code |
| FlowOps Evidence Fetch | x402 `a` | Dedicated app/provider code |
| Customer reference signer | direct agent attribution where the customer opts in | Customer-owned wallet-derived or dashboard code; never silently FlowOps-owned |
| Hosted facilitator | x402 `w` | Facilitator-owned and observed from calldata |

Codes are not reused across these roles merely for prettier metrics. Reuse requires matching ownership, payout, purpose, and analytics semantics.

## Evidence hierarchy

1. Canonical transaction calldata parsed to exact `a`/`s`/`w`.
2. Registry/token/ownership and payout evidence for the code.
3. base.dev analytics as a distribution signal.
4. FlowOps internal task and canonical settlement records as product/financial truth.

## Current selected-path result

The public Base Sepolia facilitator advertises `builder-code`, and the pinned reference implementation passes. `cmd/x402-builder-experiment` now prepares a digest-bound EIP-3009 authorization without accepting a private key, requires an exact settlement confirmation, verifies before settling, and proves the resulting transfer/calldata through two independent RPCs. A FlowOps-coded settlement has not yet been sent. Classification remains `UNRESOLVED`; see `../evidence/CALLDATA_EXPERIMENT.md`.

## Completion gate

Record the app/service code's ERC-721 token and metadata, call the agent API for the designated reference signer, compare their registry/indexer behavior, send the confirmed Base Sepolia test payment, and parse the canonical suffix before combining any attributed metrics.
