# ADR-0059: Cross-language ASCP v4 typed-data registry

- Status: accepted
- Date: 2026-08-22

## Context

ASCP money and governance boundaries use six EIP-712 messages. Independent Go, Solidity, seller, MCP, and browser implementations must not infer field order, integer width, domain version, or JSON number representation. The prior spend-module authorization types were still on domain v3, omitted the module field, encoded nonces as `bytes32`, and omitted allowance leadership fencing.

## Decision

`schemas/ascp-typed-data-v4.registry.json` is the checked-in machine-readable registry for `ExecutionCommitment`, `SellerQuote`, `LockAuthorization`, `AllowanceAuthorization`, `AdminActionAuthorization`, and `VerdictAttestation`. Each entry pins one exact type string, JSON Schema, and signed conformance vector. Every vector publishes the v4 domain separator, full EIP-712 encoded data, struct hash, digest, Solidity-compatible signature, recovered signer, and canonical JSON bytes.

Go packages retain domain-specific validation and consume the same vector values. `pkg/typedregistry` rejects registry or artifact drift and requires every exact type string to appear in the Solidity registry. `contracts/src/libraries/ASCPTypeHashes.sol` is the compile-time Solidity source for all six type hashes, including off-chain `SellerQuote`; the Escrow, SpendModule, ServiceDirectory, and AgentRegistry contracts consume its applicable constants. The dependency-free `sdk/typescript` implementation computes Ethereum Keccak and EIP-712 encoding independently and tests every registry entry.

JSON transports encode `uint256` values as canonical decimal strings. Numeric `uint64` fields are restricted by schema to the interoperable JSON safe-integer range; the EIP-712 width remains `uint64`. Callers needing a larger protocol value must be rejected instead of allowing a JavaScript parser to round signed bytes.

`LockAuthorization` and `AllowanceAuthorization` now bind `module`, use `uint256 nonce`, include leadership and authorizer epochs, and use domain version 4. Module nonce storage, events, invalidation governance, and Go governance-payload encoding use `uint256` consistently. This is an ABI-breaking protocol correction; deployment requires a new immutable module followed by predecessor drain, not an in-place upgrade.

## Consequences

- A field, type, order, domain, schema, encoded byte, digest, or canonical JSON change fails cross-language tests.
- Existing v3 spend-module signatures and `bytes32[]` nonce-invalidation calldata are intentionally incompatible with the successor.
- Leadership epoch remains issuance and keeper fencing metadata under the bounded-drain model: nonzero epoch is contract-bound, while previously released signatures remain usable until consumption, expiry, or finalized invalidation.
- The SHA-256 artifact manifest provides repository integrity. A release-governance signature over the manifest remains a deployment gate and is not synthesized by local tests.
