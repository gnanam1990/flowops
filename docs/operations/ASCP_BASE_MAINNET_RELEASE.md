# ASCP Base mainnet release

Status: four contracts deployed and finalized as an owner-authorized, unaudited,
zero-fund graph; runtime activation and funding remain blocked.

## Current preparation state

The production Safe and four ASCP contracts are finalized on Base mainnet. The
canonical post-deployment evidence is
`deployments/base-mainnet-ascp-experimental-v1.json`. It records an explicitly
unaudited deployment: all sources are verified, but the Safe module is disabled,
the escrow is not allowlisted, no funding was authorized, and all four contract
ETH and USDC balances were observed as zero.

The exact public address tuple is mirrored into the deliberately incomplete
runtime binding profile at
`deploy/control-plane/base-mainnet-ascp-deployed-inactive.env.example` and the
dashboard record at
`apps/dashboard/app/mainnet/ascp-mainnet-deployment.json`. Validate that neither
surface drifted from the evidence record with:

```sh
make test-ascp-mainnet-runtime-bindings
```

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

The production deployment script remains fail-closed and is not retroactively
promoted by the experimental deployment. External review, production RPC
admission, exact on-chain activation review, and a signed runtime release
manifest remain mandatory before the control-plane process may observe this
tuple as an enabled Base mainnet runtime. Funding and payment authorization are
later, independent ceremonies.

## Required sequence

1. Complete independent review of the exact deployed source commit and bind its
   SHA-256 digest into a reviewed activation plan. Review must treat the current
   on-chain addresses and runtime code hashes as immutable inputs.
2. Complete production RPC admission with two or more independent paid providers.
3. Re-verify every deployment receipt, constructor binding, runtime code hash,
   source-verification result, Safe owner/threshold state, module state, escrow
   allowlist state, and zero balances through the admitted providers.
4. Review the exact authority rules and the separately proposed Safe actions.
   Do not enable the module, allowlist the escrow, publish a directory root, or
   activate a verifier merely to make the dashboard or runtime appear live.
5. Keep the zero-fund binding profile incomplete until steps 1–4 are evidenced.
6. Build the release image from the immutable reviewed commit in the trusted
   build pipeline, push it to the private registry, and pin its immutable image
   digest. Extract `/flowops/control-plane-api` from that exact image and obtain
   its canonical artifact digest:

   ```sh
   go run ./cmd/release-manifest artifact-digest /secure/control-plane-api
   ```

7. Fill the schema-v2 `deployments/base-mainnet-ascp-release.template.json` with canonical
   evidence, including the CLI-produced `controlPlaneArtifactSha256`. Sign it offline:

   ```sh
   FLOWOPS_BASE_MAINNET_RELEASE_PRIVATE_KEY_B64=... \
     go run ./cmd/release-manifest sign /secure/unsigned-release.json \
     > /secure/signed-release.json
   ```

8. Verify the signed file using only the production public key:

   ```sh
   FLOWOPS_BASE_MAINNET_RELEASE_PUBLIC_KEY_B64=... \
     go run ./cmd/release-manifest verify /secure/signed-release.json
   ```

9. Deploy the exact registry image digest used in step 6. The mainnet build must include
    `--build-arg FLOWOPS_SOURCE_COMMIT=<the exact 40-character reviewed commit>`
    because startup requires the baked claim to equal signed `sourceCommit`. That
    caller-controlled value is not sufficient artifact provenance and cannot
    replace the signed executable digest.
10. Configure the runtime with the exact manifest and matching ASCP and observer tuples. Base mainnet startup hashes the running executable inode and requires it to equal signed `controlPlaneArtifactSha256`; it also requires the baked build commit to equal signed `sourceCommit`, rejects any quorum, confirmation, reorg, freshness, timeout, interval, or recovery setting that differs from the signed profile, then checks every contract and canonical USDC through the complete paid-RPC set before opening PostgreSQL or serving traffic.
11. Run a zero-fund soak. Funding requires a second signed manifest carrying the separately reviewed funded-pilot evidence digest and another explicit human approval.

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
