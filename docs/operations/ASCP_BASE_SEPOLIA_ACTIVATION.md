# ASCP Base Sepolia capability activation

Status: executed, indexed, and independently re-observed; funding remains
disabled.

On 2026-08-26, the existing 2-of-3 Base Sepolia Safe executed one atomic
`MultiSendCallOnly` batch at Safe nonce `0`:

1. `ASCPSpendModule.setEscrowAllowlist` bound the exact deployed
   `ASCPCallEscrow` runtime code hash, workflow ID, and workflow-payload hash.
2. `Safe.enableModule` enabled the exact deployed `ASCPSpendModule`.

The Safe transaction hash is
`0x067e9a25bb5cf3586ede0678b0dad7b5c5c4144d5b4e055586ea056d83562e9e`.
The successful Base Sepolia execution transaction is
`0x46bf7bade9900bc551d326b3c4214f101b41df4cce02428586b9fdb8988d751e`
in block `45978991`. The canonical record is
`deployments/base-sepolia-ascp-activation-v1.json`.

## Exact activated boundary

- Safe: `0xf6ac2af2c441ff8886b250233a7adfc206ab0b57`
- ASCP spend module: `0xac81e7abf6114209ad1357dabf1ae606b793f7b4`
- ASCP call escrow: `0x4122414fe2398ea2504eb6e35208f3b8d67e711d`
- Escrow runtime code hash:
  `0x0bf34c89d599a4bbbb94fb793e0c83efa50575f39e9cd342db5a4db5a863b531`
- Safe nonce after execution: `1`

The batch transferred no native asset or token, created no token approval, and
did not publish a directory version, activate a verifier, register an agent, or
change spend caps. Re-observation through the Base public RPC and PublicNode
confirmed:

- the Safe module is enabled;
- the escrow allowlist equals the exact runtime code hash;
- the Safe native/test-USDC balances, every ASCP contract native/test-USDC
  balance, and both relevant allowances remain zero;
- the directory version/root, agent count, escrow locked principal, and spend
  executed principal remain zero; and
- neither the escrow nor the spend module is emergency-paused; and
- the Safe execution-success, module-enabled, allowlist-set, and workflow-bound
  events are present in the canonical receipt.

Capability activation is not funding authorization. The module can be called
only through its existing signed, nonce-bound authorization contracts, but no
end-to-end payment path is admitted until the separately governed directory,
verifier, agent, control-plane, and capped test-USDC funding gates are complete.

## Verification

The deterministic evidence gate is:

```sh
make test-ascp-sepolia-activation-evidence
```

It validates schema, deployment cross-links, Safe quorum evidence, action/data
binding, event topics, funding-disabled postconditions, and mutation rejection.
It reconstructs the packed MultiSend payload from the two recorded inner calls
and requires byte-for-byte equality with the Safe transaction data.

The network-dependent, read-only gate is:

```sh
make verify-ascp-sepolia-activation
```

It re-observes the transaction, canonical block, exact event topics, runtime
code hash, Safe nonce/module state, directory/registry state, principal totals,
balances, and allowances through two independent RPC providers. It also binds
the exact confirming-owner set and Safe payload to Safe's indexed transaction
record. It never signs or sends a transaction.

The original deployment evidence remains a historical snapshot and is checked
separately with:

```sh
make test-ascp-sepolia-evidence
make verify-ascp-sepolia-deployment
```

Base Sepolia evidence never authorizes Base mainnet deployment or real-money
funding.
