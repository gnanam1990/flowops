# ADR-0062: Signed Base mainnet release admission

## Status

Accepted for implementation. No Base mainnet deployment or funding is authorized by this ADR.

## Decision

FlowOps will admit Base mainnet only through a current, Ed25519-signed release
manifest. The manifest binds the exact reviewed source commit, external-review
digest, typed-data registry, paid-RPC admission, canonical USDC code hash,
complete ASCP contract graph, deployment transactions and blocks, Safe owners
and threshold, independently assigned authority roles, initial pilot limits,
and the governance observation start block.

`FLOWOPS_BASE_CHAIN_ID=8453` is rejected unless:

1. the release manifest is strict JSON with no duplicate, unknown, or trailing fields;
2. its signature verifies against the separately configured production public key;
3. the manifest is current, expires within 31 days, and explicitly enables the runtime;
4. every runtime address, pilot limit, and observation bound equals the signed value;
5. two to five independently admitted paid RPC providers return chain ID `8453`;
6. every provider returns bytecode whose Keccak-256 hash matches the manifest for canonical USDC and all four ASCP contracts.

The release signature is an operational admission control, not an external
audit, legal approval, Safe transaction, source-verification receipt, or
funding authorization. Funding remains separately disabled unless the signed
manifest binds funded-pilot evidence.

## Release scope

The first production profile is escrow-first. Direct x402 settlement remains
reserved and must not be advertised or enabled by this release. The deployed
graph is:

- `ServiceDirectory`;
- `AgentRegistry`;
- `ASCPCallEscrow`;
- `ASCPSpendModule`.

The deployment script intentionally leaves the graph write-inert: it does not
enable the module on the Safe, allowlist the escrow, publish a directory root,
activate a verifier, or move USDC. Each action requires a separate dual-control
Safe workflow and canonical receipt before the final release manifest is signed.

## Consequences

- A copied environment variable cannot silently substitute a contract, asset,
  RPC admission, pilot cap, or governance start block.
- A single compromised or stale RPC cannot admit different bytecode.
- Operator key rotation is performed by changing the trusted release public
  key through a separately reviewed secret/configuration change.
- Runtime startup depends on live quorum reads and therefore fails closed when
  the production observer set is unavailable.
