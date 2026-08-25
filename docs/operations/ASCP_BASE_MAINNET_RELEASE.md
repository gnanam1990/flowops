# ASCP Base mainnet release

Status: structurally implemented and deliberately blocked; no deployment or funding is authorized.

## Required sequence

1. Complete external contract review and bind its SHA-256 digest into a reviewed release plan.
2. Designate the hardware deployer, production Safe, Safe owners/threshold, directory publisher, directory pauser, registry admin, spend authorizer, and organization domain in a separate promotion PR.
3. Replace the zero constants in `contracts/script/DeployASCPBaseMainnet.s.sol` only in that reviewed PR. The committed script must otherwise remain unable to broadcast.
4. Run the full script on a pinned Base mainnet fork and compare all constructor bindings and creation bytecode.
5. Obtain explicit zero-fund broadcast approval. Deploy the four contracts through the hardware-wallet ceremony. Do not enable the Safe module or transfer assets in the deployment transaction.
6. Verify source and runtime bytecode independently through every admitted paid RPC provider.
7. Execute and reconcile the separately approved Safe actions that enable the module, allowlist the exact escrow code hash, publish the initial directory root, and activate the verifier. Keep funding disabled.
8. Fill `deployments/base-mainnet-ascp-release.template.json` with canonical evidence. Sign it offline:

   ```sh
   FLOWOPS_BASE_MAINNET_RELEASE_PRIVATE_KEY_B64=... \
     go run ./cmd/release-manifest sign /secure/unsigned-release.json \
     > /secure/signed-release.json
   ```

9. Verify the signed file using only the production public key:

   ```sh
   FLOWOPS_BASE_MAINNET_RELEASE_PUBLIC_KEY_B64=... \
     go run ./cmd/release-manifest verify /secure/signed-release.json
   ```

10. Build the production image with `--build-arg FLOWOPS_SOURCE_COMMIT=<the exact 40-character reviewed commit>`. The trusted build pipeline bakes this value into `control-plane-api`; it is not a mutable runtime variable.
11. Configure the runtime with the exact manifest and matching ASCP and observer tuples. Base mainnet startup requires the baked build commit to equal the signed `sourceCommit`, rejects any quorum, confirmation, reorg, freshness, timeout, interval, or recovery setting that differs from the signed profile, then checks every contract and canonical USDC through the complete paid-RPC set before opening PostgreSQL or serving traffic.
12. Run a zero-fund soak. Funding requires a second signed manifest carrying the separately reviewed funded-pilot evidence digest and another explicit human approval.

## Required runtime variables

- `FLOWOPS_BASE_CHAIN_ID=8453`
- `FLOWOPS_BASE_RPC_PROVIDERS_JSON`
- `FLOWOPS_BASE_RPC_ADMISSION_JSON`
- `FLOWOPS_BASE_MAINNET_RELEASE_MANIFEST_JSON`
- `FLOWOPS_BASE_MAINNET_RELEASE_PUBLIC_KEY_B64`
- `FLOWOPS_ESCROW_CONTRACT` equal to the signed `ascp_call_escrow`
- `FLOWOPS_ESCROW_ASSET` equal to canonical Base USDC
- `FLOWOPS_ESCROW_RELEASE_WINDOW_SECONDS` equal to the signed settlement window
- `FLOWOPS_ASCP_DIRECTORY_CONTRACT`
- `FLOWOPS_ASCP_AGENT_REGISTRY_CONTRACT`
- `FLOWOPS_ASCP_CALL_ESCROW_CONTRACT`
- `FLOWOPS_ASCP_SPEND_MODULE_CONTRACT`
- `FLOWOPS_ASCP_GOVERNANCE_FROM_BLOCK`
- the exact initial pilot limit variables

The private release-signing key must never be present in the production runtime.
