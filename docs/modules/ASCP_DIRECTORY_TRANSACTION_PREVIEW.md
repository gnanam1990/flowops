# ASCP directory relay transaction preview

## Purpose and authority boundary

`cmd/ascp-directory-transaction-preview` is the final local construction gate
before any wallet decision. It takes still-fresh, verified relay simulation
evidence and constructs one exact unsigned EIP-1559 Base Sepolia transaction.

The full calldata contains a permissionlessly relayable publisher signature.
It is therefore written only to a new owner-controlled `0600` file. The public
preview contains hashes, chain, sender, contract, nonce, gas and fee ceilings,
worst-case gas spend, expiry, and exact provider observations—but never the
signature or calldata.

The command has no sign, fund, submit, or broadcast mode. It performs no gas
estimation because sending the signed calldata to an RPC would disclose the
relay capability. `broadcastAuthorized` and `fundingEnabled` are always false.

## Current-state and fee checks

The gate uses the same two reviewed RPC origins as the relay simulation. Each
provider must independently report:

- Base Sepolia chain ID `84532`;
- the exact intended relayer's same pending nonce before and after the block
  metadata read;
- a fresh block at or after that provider's simulation block;
- an unchanged block hash, timestamp, and EIP-1559 base fee; and
- a base fee for which `2 * baseFee + priorityFee <= maxFeePerGas`.

Only `eth_chainId`, `eth_getTransactionCount`, and `eth_getBlockByNumber` are
used. No request contains calldata, a publisher signature, `eth_call`,
`eth_estimateGas`, a signing request, or a submission method. Pending nonce is
inherently mutable after observation, so a later wallet/submission boundary
must revalidate every transaction field and current nonce before signing.

## Transaction request

The strict request names the complete operator ceiling:

```json
{
  "schemaVersion": 1,
  "expectedNonce": "7",
  "gasLimit": 500000,
  "maxFeePerGasWei": "4000000000",
  "maxPriorityFeePerGasWei": "1000000000",
  "maxWorstCaseGasSpendWei": "2000000000000000",
  "validUntil": "2026-08-26T18:01:00Z"
}
```

The implementation accepts only a narrow `450000`–`500000` gas range, grounded
by a Solidity execution test for the fixed v1 proposal path. It also caps max
fee at 10 gwei, priority fee at 2 gwei, and worst-case gas spend at 0.005 ETH. The chosen
`gasLimit * maxFeePerGasWei` must also fit the request's lower explicit ceiling.
The preview lifetime is at most two minutes and ends strictly before the
publisher authorization expires.

## Commands

Prepare a public preview and one new private unsigned-transaction artifact:

```sh
go run ./cmd/ascp-directory-transaction-preview prepare \
  /secure/path/directory-relay-simulation.json \
  /secure/path/directory-presign-package.json \
  /secure/path/base-sepolia-directory-v1-artifact.json \
  deployments/base-sepolia-ascp-v4.json \
  /secure/path/private-publisher-signature.json \
  /secure/path/directory-relay-request.json \
  /secure/path/directory-transaction-request.json \
  /secure/path/private-unsigned-transaction.json \
  > /secure/path/directory-transaction-preview.json
```

The private output path must be absolute, symlink-free, owner-controlled, and
absent. Existing files are never overwritten.

Verify the public and private artifacts while every freshness window remains
open:

```sh
go run ./cmd/ascp-directory-transaction-preview verify \
  /secure/path/directory-transaction-preview.json \
  /secure/path/private-unsigned-transaction.json \
  /secure/path/directory-relay-simulation.json \
  /secure/path/directory-presign-package.json \
  /secure/path/base-sepolia-directory-v1-artifact.json \
  deployments/base-sepolia-ascp-v4.json \
  /secure/path/private-publisher-signature.json \
  /secure/path/directory-relay-request.json \
  /secure/path/directory-transaction-request.json
```

Run focused tests with:

```sh
make test-ascp-directory-transaction-preview
```

## Remaining external decision

This module does not authorize a real transaction. A later wallet boundary
must receive a fresh explicit decision for the exact preview hashes and fee
ceiling, independently revalidate nonce and chain state, show the transaction
in the relayer's wallet, and stop for human approval. Broadcasting and receipt
reconciliation remain separate consequential operations. The proposal's Safe
approval and minimum 24-hour activation delay remain later independent gates.
