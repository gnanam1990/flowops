# FlowOps Proposal Anchor Base Mainnet Runbook

Status: blocked; no deployment exists

This runbook covers only the evidence-only `FlowOpsProposalAnchor`. It does not
authorize `CallEscrow`, a factory, a vault, USDC approval, token funding, or any
production payment path.

## Current structural stop

`contracts/script/DeployFlowOpsProposalAnchorBaseMainnet.s.sol` currently pins:

- Base mainnet chain ID `8453`;
- `DESIGNATED_DEPLOYER = address(0)`;
- `PROPOSAL_DIGEST = bytes32(0)`;
- `SOURCE_COMMIT = bytes20(0)`;
- `DEPLOYMENT_APPROVAL_DIGEST = bytes32(0)`; and
- `MAINNET_BROADCAST_ENABLED = false`.

The final five fields force every committed broadcast attempt to revert. Do not
bypass the package with `forge create`, a raw transaction, a copied script, a
software keystore, or an environment-only override.

## Promotion prerequisites

Before a separate promotion commit may fill the blocked fields:

1. Freeze and SHA-256 hash
   `docs/proposals/FLOWOPS_BASE_MAINNET_EXPERIMENTAL_ANCHOR_V1.md`.
2. Record the exact reviewed Git commit containing the contract source and
   compiler configuration.
3. Review the deployed runtime surface and confirm no payable, factory, vault,
   funding, token, upgrade, admin, arbitrary-call, delegatecall, or self-destruct
   path exists.
4. Designate a dedicated hardware-backed Base mainnet deployer. Do not reuse a
   Base Sepolia MetaMask wallet or import a production key into Foundry.
5. Rehearse the exact deployment on a pinned Base mainnet fork and record the
   predicted constructor values, creation bytecode, runtime hash, deployer
   nonce, and a strict gas ceiling.
6. Prepare explorer source-verification input for the exact compiler and
   optimizer settings.
7. Obtain fresh human approval naming the chain, deployer, source commit,
   proposal digest, expected nonce, and maximum gas spend.

## Broadcast boundary

After the promotion commit passes full CI and review, broadcast once from the
designated hardware wallet. If the result is unknown, stop and reconcile the
expected address and nonce through independent observers; never retry blindly.

No token approval or funding transaction may be bundled with or follow from
this deployment ceremony. The deployment record must remain
`fundingEnabled: false`, `vaultCreationEnabled: false`, and
`productionReady: false` permanently.

## Post-deployment evidence

Before the public UI may show an address:

1. confirm the canonical receipt and contract-creation sender;
2. compare the exact creation input and runtime bytecode with the reviewed
   build;
3. read and confirm the proposal digest, source commit, deployer, status, and
   three permanent `false` capability getters;
4. verify source code on a Base-supported explorer;
5. update the deployment record in a separate evidence PR; and
6. configure the UI address only after that evidence PR is merged.

The address must always remain labelled experimental, unaudited, evidence-only,
not production, no funds, and no vault creation.
