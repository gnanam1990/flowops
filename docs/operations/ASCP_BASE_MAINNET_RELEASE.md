# ASCP Base mainnet release

Status: structurally implemented and deliberately blocked; no deployment or funding is authorized.

## Current preparation state

The production Safe is finalized on Base mainnet, but the ASCP contract graph
is not approved or deployed. The exact preparation-only candidate is recorded
at `deployments/base-mainnet-ascp-deployment-candidate-v1.json`; its SHA-256 is
stored separately in
`deployments/base-mainnet-ascp-deployment-candidate-v1.sha256`.

The candidate binds the current source baseline, compiler and dependency
revisions, deployer nonce, finalized 2-of-3 Safe, its exact ordered owner set,
threshold, nonce, empty module list, runtime code hash and singleton, authorities, organization
domain, canonical USDC, four predicted CREATE addresses, constructor arguments,
creation bytecode, encoded constructor arguments, complete init-code hashes,
initial caps, and required write-inert post-deployment state. Run:

```sh
make test-ascp-mainnet-candidate
```

With `BASE_MAINNET_FORK_RPC_URL` set, the same target also deploys the exact
candidate graph inside a pinned finalized Base mainnet fork and verifies every
address and zero-fund invariant. It never signs or broadcasts a transaction.

This candidate is deliberately not the release-plan digest or a promotion
commit. `DeployASCPBaseMainnet.s.sol` still pins the zero deployer, zero nonce,
zero Safe, zero review digest, zero release-plan digest, and disabled broadcast.
External review, production RPC admission, owner-control evidence, the reviewed
promotion commit, signed runtime release manifest, and a fresh zero-fund
broadcast approval remain mandatory separate gates.

## Required sequence

1. Complete external contract review and bind its SHA-256 digest into a reviewed release plan.
2. Designate the hardware deployer, production Safe, Safe owners/threshold, directory publisher, directory pauser, registry admin, spend authorizer, and organization domain in a separate promotion PR.
3. Replace the zero constants in `contracts/script/DeployASCPBaseMainnet.s.sol` only in that reviewed PR. The committed script must otherwise remain unable to broadcast.
4. Run the full script on a pinned Base mainnet fork and compare all constructor bindings and creation bytecode.
   The promoted script must pin the canonical USDC runtime code hash and the
   deployer's exact starting nonce, validate the production Safe's reviewed
   runtime, singleton, owner set, threshold, nonce and empty module list, and
   prove that all four predicted CREATE addresses have no
   code, nonce, native balance, or USDC balance before broadcast.
5. Obtain explicit zero-fund broadcast approval. Deploy the four contracts through the hardware-wallet ceremony. Do not enable the Safe module or transfer assets in the deployment transaction.
6. Verify source and runtime bytecode independently through every admitted paid RPC provider.
7. Execute and reconcile the separately approved Safe actions that enable the module, allowlist the exact escrow code hash, publish the initial directory root, and activate the verifier. Keep funding disabled.
8. Build the release image from the immutable reviewed commit in the trusted
   build pipeline, push it to the private registry, and pin its immutable image
   digest. Extract `/flowops/control-plane-api` from that exact image and obtain
   its canonical artifact digest:

   ```sh
   go run ./cmd/release-manifest artifact-digest /secure/control-plane-api
   ```

9. Fill the schema-v2 `deployments/base-mainnet-ascp-release.template.json` with canonical
   evidence, including the CLI-produced `controlPlaneArtifactSha256`. Sign it offline:

   ```sh
   FLOWOPS_BASE_MAINNET_RELEASE_PRIVATE_KEY_B64=... \
     go run ./cmd/release-manifest sign /secure/unsigned-release.json \
     > /secure/signed-release.json
   ```

10. Verify the signed file using only the production public key:

   ```sh
   FLOWOPS_BASE_MAINNET_RELEASE_PUBLIC_KEY_B64=... \
     go run ./cmd/release-manifest verify /secure/signed-release.json
   ```

11. Deploy the exact registry image digest used in step 8. The mainnet build must include
    `--build-arg FLOWOPS_SOURCE_COMMIT=<the exact 40-character reviewed commit>`
    because startup requires the baked claim to equal signed `sourceCommit`. That
    caller-controlled value is not sufficient artifact provenance and cannot
    replace the signed executable digest.
12. Configure the runtime with the exact manifest and matching ASCP and observer tuples. Base mainnet startup hashes the running executable inode and requires it to equal signed `controlPlaneArtifactSha256`; it also requires the baked build commit to equal signed `sourceCommit`, rejects any quorum, confirmation, reorg, freshness, timeout, interval, or recovery setting that differs from the signed profile, then checks every contract and canonical USDC through the complete paid-RPC set before opening PostgreSQL or serving traffic.
13. Run a zero-fund soak. Funding requires a second signed manifest carrying the separately reviewed funded-pilot evidence digest and another explicit human approval.

## Required runtime variables

- `FLOWOPS_BASE_CHAIN_ID=8453`
- `FLOWOPS_BASE_RPC_PROVIDERS_JSON`
- `FLOWOPS_BASE_RPC_ADMISSION_JSON`
- `FLOWOPS_BASE_MAINNET_RELEASE_MANIFEST_JSON`
- `FLOWOPS_BASE_MAINNET_RELEASE_PUBLIC_KEY_B64`
- `FLOWOPS_METRICS_KEY_B64`
- `FLOWOPS_ESCROW_CONTRACT` equal to the signed `ascp_call_escrow`
- `FLOWOPS_ESCROW_ASSET` equal to canonical Base USDC
- `FLOWOPS_ESCROW_RELEASE_WINDOW_SECONDS` equal to the signed settlement window
- `FLOWOPS_ASCP_DIRECTORY_CONTRACT`
- `FLOWOPS_ASCP_AGENT_REGISTRY_CONTRACT`
- `FLOWOPS_ASCP_CALL_ESCROW_CONTRACT`
- `FLOWOPS_ASCP_SPEND_MODULE_CONTRACT`
- `FLOWOPS_ASCP_GOVERNANCE_FROM_BLOCK`
- `FLOWOPS_PILOT_MAX_PER_ACTION_ATOMIC` equal to the signed initial per-action limit
- `FLOWOPS_PILOT_MAX_OUTSTANDING_ATOMIC` equal to the signed initial outstanding limit

The private release-signing key must never be present in the production runtime.
