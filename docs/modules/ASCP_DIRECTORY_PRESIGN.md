# ASCP directory remote-content and pre-sign gate

## Purpose and hard boundary

`cmd/ascp-directory-presign` is the next gate after the funding-disabled
ServiceDirectory release compiler. It proves that the IPFS and Arweave gateway
responses both contain the exact canonical blob bytes, then emits the exact
unsigned EIP-712 publisher authorization package.

It never accepts or creates a private key, signature, raw transaction, gas
configuration, token approval, or funding instruction. Its output explicitly
sets `signatureRequired: true`, `broadcastAuthorized: false`, and
`fundingEnabled: false`. There is intentionally no `sign` or `submit` command.

## Inputs

The command consumes four reviewed files:

1. the compiler-produced release artifact;
2. `deployments/base-sepolia-ascp-v4.json`;
3. a strict gateway configuration containing two different canonical public
   HTTPS origins; and
4. a strict request containing one nonzero, unused directory-publisher admin
   nonce.

The gateway file has this closed shape:

```json
{
  "schemaVersion": 1,
  "ipfsGatewayOrigin": "https://<reviewed-public-ipfs-gateway>",
  "arweaveGatewayOrigin": "https://<reviewed-public-arweave-gateway>"
}
```

Origins cannot contain credentials, paths, queries, fragments, ports, IP
literals, single-label hosts, or local/internal suffixes. The HTTP transport
also disables proxies, redirects, and transparent compression; resolves DNS
again at connection time; rejects every non-public answer; requires TLS 1.2 or
newer; and bounds response headers, time, and bytes.

The request file contains no secret:

```json
{
  "schemaVersion": 1,
  "adminNonce": "<nonzero-unused-uint256>"
}
```

The operation ID is not caller-selected. It is deterministically derived from
the complete compiler artifact hash and admin nonce under the
`ASCP_DIRECTORY_PRESIGN_OPERATION_V1` domain.

## Exact verification

For IPFS the command requests `/ipfs/<cid>` from the reviewed IPFS gateway. For
Arweave it requests `/<transaction-id>` from the reviewed Arweave gateway. Both
responses must:

- return HTTP 200 without redirects or content encoding;
- use `application/octet-stream`, `application/json`, or `text/plain`;
- fit the canonical blob's exact byte length; and
- equal `canonicalBlob` byte-for-byte.

The evidence records the immutable location, exact gateway URL, UTC fetch time,
length, SHA-256, and Keccak-256 for each independent copy. The pre-sign package
refuses any individual copy older than two minutes or any evidence fetched
after package preparation. It rechecks every copy's freshness after the
two-provider live quorum finishes and whenever a saved package is verified.

The package then derives the shared ASCP v4 EIP-712 domain separator, struct
hash, and digest for `AdminActionAuthorization`. The authorization repeats the
organization domain, Base Sepolia chain, ServiceDirectory address, publisher
role, `proposeVersion` selector, full proposal payload hash, publisher epoch,
workflow ID, deterministic operation ID, and admin nonce. Its validity window
is exactly 600 seconds and is backdated by 30 seconds for small observer/chain
clock differences.

Before emitting that package, the command observes two distinct Base Sepolia
RPC origins. Each provider first returns a concrete latest block; every
ServiceDirectory read is then pinned to that exact block number, and the block
hash and timestamp are fetched again after all calls to reject an intervening
reorganization. Both
observations must independently confirm chain `84532`, the expected contract,
organization domain, publisher and epoch, the exact zero predecessor, no v1
proposal, and unused derived operation ID and role nonce. Both block timestamps
must fall inside the authorization window and cannot differ by more than two
minutes. Evidence is also rejected when a provider block timestamp, or the
local verification time, would leave less than two minutes before authorization
expiry. RPC disagreement or unavailability fails closed. Provider URLs are
read from `BASE_SEPOLIA_RPC_URL_PRIMARY` and
`BASE_SEPOLIA_RPC_URL_SECONDARY`, with the repository's two public defaults
used when they are unset; URLs are never written into the package.

Go mutation tests cover gateway, remote-byte, HTTP response, freshness,
artifact, proposal, operation, nonce, window, digest, call, and funding
substitution. The Solidity parity test independently recomputes the same
ServiceDirectory authorization digest.

## Commands

Inspect both remote copies without creating a signing package:

```sh
go run ./cmd/ascp-directory-presign verify-remote \
  /secure/path/base-sepolia-directory-v1-artifact.json \
  deployments/base-sepolia-ascp-v4.json \
  /secure/path/directory-gateways.json
```

Prepare the short-lived unsigned package. Run this only when the external
publisher is ready to inspect and sign immediately:

```sh
go run ./cmd/ascp-directory-presign prepare \
  /secure/path/base-sepolia-directory-v1-artifact.json \
  deployments/base-sepolia-ascp-v4.json \
  /secure/path/directory-gateways.json \
  /secure/path/directory-presign-request.json \
  > /secure/path/directory-presign-package.json
```

Verify the exact package while its window is still live:

```sh
go run ./cmd/ascp-directory-presign verify \
  /secure/path/directory-presign-package.json \
  /secure/path/base-sepolia-directory-v1-artifact.json \
  deployments/base-sepolia-ascp-v4.json
```

Run focused tests with:

```sh
make test-ascp-directory-presign
```

## Remaining consequential gates

This gate cannot run against production content until the real seller/resource
manifest has been compiled and its exact canonical blob has been uploaded to
both declared locations. The `prepare` command now includes the exact-block,
two-provider unused operation/nonce proof. Immediately before external signing,
rerun `prepare` rather than signing an older package; its 10-minute window and
two-minute remote evidence age are deliberately short.

The next module must accept only a valid external publisher signature, recover
the deployed publisher, reconstruct the complete `proposeVersion` calldata,
simulate it against current chain state, and still stop before broadcast unless
the user separately authorizes that exact transaction. Safe approval, the
minimum 24-hour activation delay, and funding remain later independent gates.
