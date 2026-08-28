# ASCP Base mainnet zero-fund capability activation

Status: executed, indexed, and independently re-observed; funding remains
disabled.

On 2026-08-28, the existing 2-of-3 Base mainnet Safe executed one atomic
`MultiSendCallOnly` batch at Safe nonce `0`:

1. `ASCPSpendModule.setEscrowAllowlist` bound the exact deployed
   `ASCPCallEscrow` runtime code hash, workflow ID, and workflow-payload hash.
2. `Safe.enableModule` enabled the exact deployed `ASCPSpendModule`.

The Safe transaction hash is
`0x7d1a4bef7c2131ed3bd40fb61260d05731703371db8a22187429498bcb491b1f`.
The successful Base mainnet execution transaction is
`0x630c2a47e57013ae99a022725e648e4711a3f635bcefccaf34bda3f5b1735e8b`
in block `50562161`. The canonical record is
`deployments/base-mainnet-ascp-activation-v1.json`.

## Exact activated boundary

- Safe: `0x13e9fa8d49ee3e3b456db71d111da9b78fabd518`
- ASCP spend module: `0x942b83421c3ac4e1a04753e5e0208fd56cad649e`
- ASCP call escrow: `0x214cbbb2190075ba43fa6518560d37c09720e0c4`
- Escrow runtime code hash:
  `0x829518ab89788763954e964351e23a4bf8b08e5e5ad5d86aaa2ab185b82ac4c9`
- Safe nonce after execution: `1`

The batch transferred no ETH or token and created no token approval. It did not
publish a directory version, activate a verifier, register an agent, or change
spend caps. Re-observation through the Base public RPC and an independent Blast
public RPC confirmed:

- the transaction succeeded with all five expected events;
- the Safe module is enabled;
- the escrow allowlist equals the exact live runtime code hash;
- the Safe and every ASCP contract have zero ETH and zero USDC;
- both relevant Safe USDC allowances remain zero;
- the directory version/root, agent count, escrow locked principal, and spend
  executed principal remain zero; and
- neither the escrow nor the spend module is emergency-paused.

Capability activation is not a claim that the payment system is end-to-end
operational. A real payment remains fail-closed until a governed directory
version, active verifier, registered agent, production runtime bindings, and a
separately authorized capped USDC funding/allowance ceremony are complete.

## Evidence links

- [Base Blockscout transaction](https://base.blockscout.com/tx/0x630c2a47e57013ae99a022725e648e4711a3f635bcefccaf34bda3f5b1735e8b)
- [Safe transaction](https://app.safe.global/transactions/tx?id=multisig_0x13E9Fa8d49Ee3E3b456Db71d111Da9b78fABD518_0x7d1a4bef7c2131ed3bd40fb61260d05731703371db8a22187429498bcb491b1f&safe=base%3A0x13E9Fa8d49Ee3E3b456Db71d111Da9b78fABD518)

The record and dashboard state must continue to state that funding is disabled.
No later funding or runtime release may rewrite this zero-fund historical
snapshot.
