# x402 V2 Adapter Evidence

Date: 2026-08-11
Module: `internal/x402adapter`

## Dependency pin

- Go module: `github.com/x402-foundation/x402/go/v2 v2.21.0`
- Release tag commit: `34cb6bd04c88f4333f56b9c778d3d35df997379c`
- Audited upstream main/spec commit: `1d15062628b086b497ca10bb9b4c675a528c864e`

FlowOps imports only the upstream V2 wire types. The Builder Code package also
imports facilitator/EVM settlement code and would pull a large unrelated
dependency graph into the quote-normalization boundary. FlowOps therefore uses
a local fail-closed ERC-8021 Schema 2 parser tested against the official vectors
at the audited upstream commit.

## Implemented boundary

- x402 V2 only.
- Base mainnet or Base Sepolia CAIP-2 identifiers only.
- Exact native-USDC payments only.
- EIP-3009 only; Permit2 and unknown transfer methods are rejected.
- Caller and environment maximums are both enforced before signing.
- The quote binds method, canonical HTTPS URL, body digest, recipient, asset,
  amount, timeout, resource metadata, payment extras, and extensions.
- Equal-price but materially different offers fail as ambiguous.
- FlowOps Builder Code service entries are client-first and deduplicated.
- Attribution becomes `VERIFIED_SUFFIX` only after canonical settlement calldata
  parses to Schema 2 and contains the expected app and FlowOps service codes.

## Read-only live capability check

On 2026-08-11, `https://x402.org/facilitator/supported` advertised:

- x402 V2 `exact` on `eip155:84532`;
- the `builder-code` extension; and
- EVM signer `0xd407e409E34E0b9afb99EcCeb609bDbcD5e7f1bf` under `eip155:*`.

This proves advertised capability only. No payment was signed or sent, and the
FlowOps-specific calldata classification remains `UNRESOLVED` as recorded in
`CALLDATA_EXPERIMENT.md`.

## Commands

```text
go test -race ./internal/x402adapter
make check
make smoke-x402-readonly
```
