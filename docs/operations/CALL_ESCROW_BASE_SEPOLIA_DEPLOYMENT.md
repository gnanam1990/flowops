# CallEscrow Base Sepolia Deployment

Status: prepared; Wallet 1 is designated but unfunded on Base Sepolia

Mainnet: prohibited by this runbook

## Pinned configuration

- Network: Base Sepolia, chain ID `84532`
- Deployer: `0x079bDde909e28E437768A06d7001eb40896668d4`
- Asset: Circle native test USDC at `0x036CbD53842c5426634e7929541eC2318f3dCF7e`
- Optimistic release window: 3,600 seconds
- Contract posture: non-upgradeable and ownerless

The second MetaMask address is not an owner of this contract. `CallEscrow`
contains no owner or admin role. That address remains reserved for treasury and
future Safe-signing work.

## Preconditions

1. Work from a clean checkout of the reviewed commit.
2. Run `make check` and `make smoke-escrow-deployment`.
3. Confirm two independent RPCs report chain ID `84532` and non-empty bytecode
   at the pinned USDC address.
4. Fund only the designated deployer with a capped amount of Base Sepolia ETH.
   Testnet ETH has no monetary value. Do not send Base mainnet assets.
5. Import Wallet 1 interactively into Foundry's encrypted keystore. Never put a
   private key, recovery phrase, or keystore password in the repository, an
   environment variable, CI, command history, or chat.

## Rehearsal

The rehearsal reads live Base Sepolia state but does not broadcast:

```bash
export BASE_SEPOLIA_RPC_URL="https://sepolia.base.org"
forge script contracts/script/DeployCallEscrowBaseSepolia.s.sol:DeployCallEscrowBaseSepolia \
  --rpc-url "$BASE_SEPOLIA_RPC_URL" \
  --sender 0x079bDde909e28E437768A06d7001eb40896668d4
```

Inspect the simulated contract address, constructor arguments, and gas estimate.
Do not continue if the chain, asset, deployer, or release window differs from
the pinned configuration.

## Approved broadcast ceremony

Broadcast is a separate human-approved action:

```bash
forge script contracts/script/DeployCallEscrowBaseSepolia.s.sol:DeployCallEscrowBaseSepolia \
  --rpc-url "$BASE_SEPOLIA_RPC_URL" \
  --account flowops-deployer \
  --sender 0x079bDde909e28E437768A06d7001eb40896668d4 \
  --broadcast
```

Enter the Foundry keystore password only in its interactive prompt. After one
confirmed transaction, stop. Do not rerun blindly if the outcome is unknown.

## Canonical verification and evidence

Set the confirmed output values and run:

```bash
export CALL_ESCROW_ADDRESS="0x..."
export DEPLOYMENT_TX_HASH="0x..."
deploy/call-escrow/verify-base-sepolia.sh
```

Then update `deployments/base-sepolia.json` in a separate evidence commit with:

- contract address, transaction hash, and block number;
- exact constructor arguments and compiler version;
- verified-source URL;
- deployed runtime code hash; and
- evidence from both independent RPC providers.

The deployment is not complete until those reads agree and the repository-wide
checks pass. A successful Sepolia deployment does not authorize Base mainnet.
