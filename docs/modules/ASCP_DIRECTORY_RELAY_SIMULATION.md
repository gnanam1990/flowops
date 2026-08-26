# ASCP directory publisher relay simulation

## Purpose and authority boundary

`cmd/ascp-directory-relay` is the gate after the directory pre-sign package and
the external publisher's EIP-712 signature. It independently recovers the
deployed directory publisher, reconstructs the exact `proposeVersion` calldata
in local process memory, and commits its Keccak-256 hash and byte length to a
two-provider current-state simulation record.

The command has no signing, transaction-construction, gas, submission, or
broadcast mode. It never prints the signature or calldata. Every output pins
`calldataDisclosed: false`, `broadcastAuthorized: false`, and
`fundingEnabled: false`.

## Why full signed calldata is not sent to public RPCs

`proposeVersion` is permissionlessly relayable once its publisher signature is
known. Sending the complete signed call through a public `eth_call` endpoint
would disclose a live authorization capability to that RPC operator, who could
broadcast it independently.

The gate therefore performs a non-disclosing contract-semantic simulation:

1. it reconstructs and hashes the complete signed calldata locally;
2. it validates the canonical 65-byte low-S signature with contract-compatible
   recovery ID `27` or `28` and recovers the deployed publisher;
3. it pins every read to one concrete latest block from each of the same two
   reviewed RPC origins used by the pre-sign gate;
4. it reads every `proposeVersion` precondition at those blocks, including the
   predecessor, proposal slot, publisher epoch, admin operation, admin nonce,
   and proposer nonce;
5. it asks the deployed contract to recompute the authorization digest,
   proposal hash, and workflow payload hash; and
6. it re-reads each block hash and timestamp after all calls to reject an
   intervening reorganization.

No RPC request contains the publisher signature or uses the `proposeVersion`
selector. Provider disagreement, stale or future blocks, replayed nonces,
changed predecessor state, expired evidence, or less than two minutes remaining
in the authorization window all fail closed.

This semantic simulation is intentionally narrower than an EVM execution of
the signed call. For this fixed v1 release, the zero current version/root and
zero v1 proposal slot prove that no live successor can exist, while the local
signature recovery and exact on-chain hash calculations cover the remaining
private-call inputs without disclosing the relay capability.

## Inputs

The signature is a private, owner-controlled regular file. Its absolute path
and every ancestor are opened without following symlinks; the file must not be
group- or world-readable. Its strict JSON shape is:

```json
{
  "schemaVersion": 1,
  "digest": "0x<exact-presign-digest>",
  "signature": "0x<65-byte-low-s-signature-with-v-27-or-28>"
}
```

The non-secret relay request binds the address that would become the on-chain
`proposer` if a later, separately authorized transaction were submitted:

```json
{
  "schemaVersion": 1,
  "relayerAddress": "0x<canonical-lowercase-address>"
}
```

The remaining inputs are the still-fresh pre-sign package, compiler artifact,
and exact Base Sepolia deployment evidence used by the earlier gates.

## Commands

Create non-disclosing simulation evidence:

```sh
go run ./cmd/ascp-directory-relay simulate \
  /secure/path/directory-presign-package.json \
  /secure/path/base-sepolia-directory-v1-artifact.json \
  deployments/base-sepolia-ascp-v4.json \
  /secure/path/private-publisher-signature.json \
  /secure/path/directory-relay-request.json \
  > /secure/path/directory-relay-simulation.json
```

Verify saved evidence while the original remote and authorization windows are
still fresh. The private signature file is required again because the evidence
contains only its hash:

```sh
go run ./cmd/ascp-directory-relay verify \
  /secure/path/directory-relay-simulation.json \
  /secure/path/directory-presign-package.json \
  /secure/path/base-sepolia-directory-v1-artifact.json \
  deployments/base-sepolia-ascp-v4.json \
  /secure/path/private-publisher-signature.json \
  /secure/path/directory-relay-request.json
```

Run focused tests with:

```sh
make test-ascp-directory-relay
```

## Evidence and tests

The evidence binds the release/artifact, signing and signature hashes,
recovered publisher, intended relayer, calldata hash and length, expected
proposal hash, exact provider blocks, every replay/predecessor read, and the
three deployed-contract hash calculations. Go mutation tests cover signature,
recovery ID, relayer, calldata, provider, reorg/freshness, all nonce classes,
digest/proposal/workflow substitution, outage, duplicate JSON, insecure files,
symlinks, and absent sign/submit/broadcast modes. A Solidity parity test
independently reproduces the 900-byte calldata and its Keccak-256 hash.

## Remaining consequential gate

No real simulation can be produced until a real seller/resource release has
been compiled, uploaded to both immutable locations, pre-signed, and signed by
the external publisher. This module does not authorize `proposeVersion`.

The [transaction preview gate](ASCP_DIRECTORY_TRANSACTION_PREVIEW.md) now binds
the exact chain, contract, relayer, calldata hash, nonce, fee ceiling, gas-spend
ceiling, and expiry and creates the matching unsigned transaction only in an
owner-private file. It still cannot sign, fund, submit, or broadcast.
Safe approval of the resulting proposal is still a separate 2-of-3 governance
transaction, followed by the contract's minimum 24-hour activation delay.
