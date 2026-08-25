# ASCP Base Sepolia deployment

Status: deployment package implemented; no ASCP Sepolia deployment, Safe action,
funding, approval, or mainnet action is authorized by this document.

`contracts/script/DeployASCPBaseSepolia.s.sol` deploys the complete ASCP v4
contract graph against Circle test USDC on Base Sepolia:

- `ServiceDirectory`
- `AgentRegistry`
- `ASCPCallEscrow`
- `ASCPSpendModule`

The deployment is deliberately write-inert. It does not enable the module on
the Safe, allowlist escrow bytecode, publish a directory root, register an
agent, activate a verifier, approve test USDC, or move funds. Each of those is
a separately reviewed and authorized operation.

## Required identities and evidence

Before simulation, designate:

1. a funded Base Sepolia deployer;
2. its exact next transaction nonce, agreed through both RPC providers;
3. an existing testnet Safe contract, not an EOA;
4. distinct directory publisher, directory pauser, registry admin, and spend
   authorizer addresses;
5. a nonzero organization-domain digest; and
6. the SHA-256 digest of a reviewed deployment-plan JSON kept outside the
   repository if it contains operational identities.

The deployer, Safe, and four operational authorities must all be distinct. The
script rejects the wrong chain, missing or substituted canonical test USDC
bytecode, a Safe without a valid nonempty unique owner set and threshold, or a
Safe that does not route `execTransactionFromModule` to the standard `GS104`
disabled-module guard.
It also rejects any zero or overlapping authority, a zero organization or plan
digest, and a missing or substituted broadcast guard. After creation it proves
that the new spend module is not already enabled on the Safe. The reviewed
Circle test USDC runtime code hash is pinned to
`0xedc5281a85c0efecd49999a1ef668390c59b88702f2d4a07029d7f5d63059d6c`.
Before the first transaction, all four nonce-derived deployment addresses must
have no bytecode, account nonce, native balance, or test USDC balance.
Post-deployment checks repeat the zero-balance assertions and prove the
directory version/root, agent count, locked principal, executed principal, and
escrow allowlist are empty.

## Environment contract

Set the following only in the trusted deployment terminal. Do not commit them:

```text
FLOWOPS_ASCP_SEPOLIA_DEPLOYER
FLOWOPS_ASCP_SEPOLIA_EXPECTED_DEPLOYER_NONCE
FLOWOPS_ASCP_SEPOLIA_SAFE
FLOWOPS_ASCP_SEPOLIA_DIRECTORY_PUBLISHER
FLOWOPS_ASCP_SEPOLIA_DIRECTORY_PAUSER
FLOWOPS_ASCP_SEPOLIA_REGISTRY_ADMIN
FLOWOPS_ASCP_SEPOLIA_SPEND_AUTHORIZER
FLOWOPS_ASCP_SEPOLIA_ORGANIZATION_DOMAIN
FLOWOPS_ASCP_SEPOLIA_DEPLOYMENT_PLAN_DIGEST
FLOWOPS_ASCP_SEPOLIA_BROADCAST_GUARD
```

The final guard must equal:

```sh
cast keccak 'FLOWOPS_ASCP_BASE_SEPOLIA_BROADCAST_V1'
```

The guard is an explicit ceremony acknowledgement, not an authorization by
itself. A wallet signature and `--broadcast` still require separate human
approval at action time.

## Verification sequence

1. Run the focused package gate:

   ```sh
   make smoke-ascp-sepolia-deployment
   ```

   This first confirms that the pinned test-USDC runtime code hash still agrees
   with both the Base public RPC and an independent PublicNode RPC, then runs
   the deterministic deployment suite. An unavailable or disagreeing provider
   fails the gate closed.

2. Simulate against two Base Sepolia RPC providers without `--broadcast`.
   Compare predicted addresses, constructor bindings, runtime code hashes, and
   gas requirements. A successful simulation moves no funds and deploys
   nothing.
3. Obtain explicit approval for the exact deployer and next nonce, Safe,
   authorities, organization domain, plan digest, source commit, RPC set,
   predicted addresses, four-transaction sequence, and total gas ceiling.
4. Broadcast the four creations through the approved wallet ceremony. The
   nonce gate prevents a stale plan or blind rerun, but the four transactions
   are not atomic: if a later creation fails, preserve all receipts, do not
   rerun, and produce a replacement plan that accounts for the orphaned
   no-funds contracts. Do not use a customer
   signer key or put a private key in the command line, environment, repository,
   or logs.
5. Confirm the receipt and exact runtime bytecode through both RPC providers,
   submit source verification, and commit a machine-readable evidence record in
   a separate evidence-only change.
6. Only after the evidence record passes its checker, configure the control
   plane with the exact directory, registry, escrow, spend-module, deployment
   block, test USDC, and observer bindings. Keep funding disabled.

Base Sepolia evidence never authorizes a Base mainnet deployment or real-money
pilot.
