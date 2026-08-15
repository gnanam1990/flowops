# FlowOps Proposal Anchor Base Mainnet Runbook

Status: promotion package approved; final broadcast remains blocked; no deployment exists

This runbook covers only the evidence-only `FlowOpsProposalAnchor`. It does not
authorize `CallEscrow`, a factory, a vault, USDC approval, token funding, or any
production payment path.

## Current structural stop

`contracts/script/DeployFlowOpsProposalAnchorBaseMainnet.s.sol` now pins:

- Base mainnet chain ID `8453`;
- deployer `0xEEC526F6555dD43536F712D5c978CbC13CB4517f`;
- proposal digest `0x35476d70f7c33d19bb8fc1fa3484e289f0a42aac43e2beca7f941f5340132362`;
- source commit `bd9292d0f916b1e3d828443b41e31a8e635b2b3e`;
- nonce `0` and predicted address
  `0x149D03Ec527Ad8667d47e7b6a2d316Dd54033250`;
- initcode/runtime hashes and a maximum gas spend of `5000000000000` wei;
- `DEPLOYMENT_APPROVAL_DIGEST = bytes32(0)`; and
- `MAINNET_BROADCAST_ENABLED = false`.

The final two fields force every committed broadcast attempt to revert. The
recorded promotion approval authorizes only this prepared package and explicitly
does not authorize broadcast. Do not bypass the package with `forge create`, a
raw transaction, a copied script, or an environment-only override.

## Promotion prerequisites

Before a separate promotion commit may fill the blocked fields:

1. Freeze and SHA-256 hash
   `docs/proposals/FLOWOPS_BASE_MAINNET_EXPERIMENTAL_ANCHOR_V1.md`.
2. Record the exact reviewed Git commit containing the contract source and
   compiler configuration.
3. Review the deployed runtime surface and confirm no payable, factory, vault,
   funding, token, upgrade, admin, arbitrary-call, delegatecall, or self-destruct
   path exists.
4. Designate a dedicated Base mainnet deployer. A software EOA is permitted
   only for this authority-free proposal anchor when it has a minimal gas
   balance, is prohibited from every production role, and passes independent
   code/latest-nonce/pending-nonce checks. Never paste its private key into
   chat, an environment variable, a command, or the repository. If it is
   imported interactively into a local encrypted keystore, that keystore is
   proposal-only and must never hold a production key.
5. Rehearse the exact deployment on a pinned Base mainnet fork and record the
   predicted constructor values, creation bytecode, runtime hash, deployer
   nonce, and a strict gas ceiling.
6. Prepare explorer source-verification input for the exact compiler and
   optimizer settings.
7. Obtain fresh human approval naming the chain, deployer, source commit,
   proposal digest, expected nonce, and maximum gas spend.

## Broadcast boundary

After the promotion commit passes full CI and review, broadcast once from the
designated proposal-only wallet. If the result is unknown, stop and reconcile
the expected address and nonce through independent observers; never retry
blindly. This signer posture does not satisfy any production deployment gate.

The accepted candidate is
`0xEEC526F6555dD43536F712D5c978CbC13CB4517f`. At the recorded read-only
preflight, both admitted public observers reported Base chain ID `8453`, empty
runtime code, latest nonce `0`, pending nonce `0`, and balance
`159318862860265` wei. The
expected CREATE address at nonce `0` is
`0x149D03Ec527Ad8667d47e7b6a2d316Dd54033250`. Every value must be refreshed
immediately before final approval and broadcast; this record is not permission
to send.

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
