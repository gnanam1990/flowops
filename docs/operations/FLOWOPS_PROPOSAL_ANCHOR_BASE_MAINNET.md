# FlowOps Proposal Anchor Base Mainnet Runbook

Status: deployed and independently verified; one-time broadcast consumed

This runbook covers only the evidence-only `FlowOpsProposalAnchor`. It does not
authorize `CallEscrow`, a factory, a vault, USDC approval, token funding, or any
production payment path.

## Consumed deployment package

`contracts/script/DeployFlowOpsProposalAnchorBaseMainnet.s.sol` now pins:

- Base mainnet chain ID `8453`;
- deployer `0xEEC526F6555dD43536F712D5c978CbC13CB4517f`;
- proposal digest `0x35476d70f7c33d19bb8fc1fa3484e289f0a42aac43e2beca7f941f5340132362`;
- source commit `bd9292d0f916b1e3d828443b41e31a8e635b2b3e`;
- nonce `0` and predicted address
  `0x149D03Ec527Ad8667d47e7b6a2d316Dd54033250`;
- initcode/runtime hashes and a maximum gas spend of `5000000000000` wei;
- deployment-approval digest
  `0x19b2ec0dad4ae81c0ec838d04285301618f670aa581bda4f218c52dbbd8b5377`; and
- `MAINNET_BROADCAST_ENABLED = false` after the successful one-time broadcast.

The record preserves the promotion approval, activation approval, exact
one-time broadcast statement, and canonical receipt evidence. The authorization
has been consumed and the committed package is structurally blocked again. It
does not authorize any other transaction. Do not bypass the package with
`forge create`, a raw transaction, a copied script, or an environment-only
override.

## Completed promotion evidence

The package was activated only after recording the following evidence:

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
   proposal digest, expected nonce, exact contract, gas ceilings, no-funds
   posture, and `broadcast=true`.

## Broadcast boundary

The designated proposal-only wallet broadcast exactly once. The canonical Base
receipt is transaction
`0x7fe3986c45a1c4de2c9ca421222569ba8e41cc6b7fe9173340a3954c9306a76b`,
block `50008264`, creating
`0x149D03Ec527Ad8667d47e7b6a2d316Dd54033250` from nonce `0` with zero
transaction value. The package is disabled after recording that evidence. This
signer posture does not satisfy any production deployment gate.

The accepted candidate is
`0xEEC526F6555dD43536F712D5c978CbC13CB4517f`. At the recorded read-only
preflight, both admitted public observers reported Base chain ID `8453`, empty
runtime code, latest nonce `0`, pending nonce `0`, and balance
`159318862860265` wei. The
expected CREATE address at nonce `0` was
`0x149D03Ec527Ad8667d47e7b6a2d316Dd54033250`. Independent post-deployment
observers now report nonce `1` and the exact reviewed runtime hash at that
address. Any future attempt must fail closed; there is no remaining broadcast
authorization.

No token approval or funding transaction may be bundled with or follow from
this deployment ceremony. The deployment record must remain
`fundingEnabled: false`, `vaultCreationEnabled: false`, and
`productionReady: false` permanently.

## Verified post-deployment evidence

The deployment evidence records and validators confirm:

1. canonical receipt status `0x1`, creation sender, nonce, zero value, gas
   ceilings, block hash, and contract address through independent observers;
2. creation-input hash and runtime-code hash match the reviewed build exactly;
3. proposal digest, source commit, deployer, status, and the three permanent
   `false` capability getters match through independent RPC reads;
4. the single `ProposalAnchored` event binds the expected deployer, proposal,
   and source revision; and
5. source is fully verified on Base Blockscout with Solidity `0.8.26`, Cancun,
   and 200 optimizer runs.

The public UI may show the address only after the evidence PR is merged. It must
remain a read-only proposal proof and must never expose payment controls for this
contract.

The address must always remain labelled experimental, unaudited, evidence-only,
not production, no funds, and no vault creation.
