# ADR-0062: Signed Base mainnet release admission

## Status

Accepted for implementation. No Base mainnet deployment or funding is authorized by this ADR.

## Decision

FlowOps will admit Base mainnet only through a current, Ed25519-signed schema-v2
release manifest under the `flowops:base-mainnet-release:v2` signing domain.
The manifest binds the exact reviewed source commit, the SHA-256 of
the exact `control-plane-api` executable extracted from the immutable release
image, designated deployer, external-review
digest, typed-data registry, paid-RPC admission, canonical USDC code hash,
complete ASCP contract graph, deployment transactions and blocks, Safe owners
and threshold, independently assigned authority roles, initial pilot limits,
the governance observation start block, and the complete observer timing,
quorum, finality, reorg, and recovery profile.

`FLOWOPS_BASE_CHAIN_ID=8453` is rejected unless:

1. the release manifest is strict JSON with no duplicate, unknown, or trailing fields;
2. its signature verifies against the separately configured production public key;
3. the manifest is current, expires within 31 days, and explicitly enables the runtime;
4. the running process hashes its own executable inode and the result equals the signed artifact digest;
5. every runtime address, pilot limit, and observation bound equals the signed value;
6. two to five independently admitted paid RPC providers return chain ID `8453`;
7. every provider returns bytecode whose Keccak-256 hash matches the manifest for canonical USDC and all four ASCP contracts.

`FLOWOPS_SOURCE_COMMIT` is secondary build metadata and is not accepted as
artifact provenance. The release approver must build from an immutable reviewed
reference, extract `/flowops/control-plane-api`, record the CLI-produced
`artifact-digest` in the manifest, sign that manifest offline, and deploy the
same image by immutable registry digest. The artifact digest is never accepted
from a runtime environment variable.

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
- Modified source cannot be admitted under the same claimed commit because the
  running executable must equal the separately signed artifact SHA-256.
- A single compromised or stale RPC cannot admit different bytecode.
- Operator key rotation is performed by changing the trusted release public
  key through a separately reviewed secret/configuration change.
- Runtime startup depends on live quorum reads and therefore fails closed when
  the production observer set is unavailable.
