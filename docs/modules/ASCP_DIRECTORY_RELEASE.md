# ASCP Directory release compiler

## Purpose and current boundary

`cmd/ascp-directory-release` compiles real ServiceDirectory seller/resource
content into the exact Base Sepolia v1 release artifact. It never creates a
wallet, reads a private key, signs, submits, retries, funds, or activates an
onchain transaction.

Seller origins are supplied as canonical HTTPS origins with no credentials,
path, query, fragment, mixed-case host, or redundant `:443`. The compiler
derives `baseURLOriginHash`; callers cannot provide an unrelated opaque hash.
IP literals, local/internal host suffixes, single-label hosts, and nonstandard
ports are rejected. Runtime HTTP clients still require DNS-rebinding and SSRF
protection because a public hostname can later resolve to a private address.

The compiler deliberately refuses illustrative or incomplete directory data.
The first release must bind the canonical ASCP v4 Base Sepolia deployment,
version `1` over the zero predecessor, the deployed test-USDC asset, the current
publisher at epoch `1`, a nonzero workflow ID and proposer nonce, active
escrow-capable seller/resource leaves, and two distinct content-addressed blob
locations (`ipfs://` and `ar://`). `fundingEnabled` must remain `false`.
The asset binding includes the deployment-recorded runtime code hash, not only
the token address, symbol, and decimals.
The IPFS location must be the exact raw-block CIDv1 derived from the canonical
blob's SHA-256 digest. The Arweave location must decode to a canonical 32-byte
transaction ID; its availability and returned bytes still require an external
fetch check after upload and before signing.

No production manifest is committed yet. The repository does not contain a
truthful real seller payout address, acknowledgement authority, quote-signing
key, paid resource definition, price, verification specification, or uploaded
content-addressed blob. Inventing those values would create an apparently valid
but unauthorized payment directory.

## Deterministic output

The compiler:

1. validates the manifest against
   `deployments/base-sepolia-ascp-v4.json`;
2. canonicalizes sellers by `sellerId` and resources by
   `(sellerId, resourceId)`;
3. hashes the exact Solidity `SellerLeaf` and `ResourceLeaf` layouts;
4. constructs a sorted-pair Merkle tree and a proof for every leaf, including
   odd-sized trees;
5. hashes the canonical directory blob and ordered content-address locations;
6. derives the exact `approveVersion(uint64,bytes32)` workflow payload,
   proposal hash, and Safe calldata; and
7. emits the immutable publisher-authorization binding required before a
   short-lived external signature can be requested.

The publisher binding repeats the organization domain, chain, directory
contract, current signer/epoch, authority role, proposal selector, full
proposal payload hash, workflow ID, and maximum authorization window. This
prevents a signing surface from silently borrowing fields from another
deployment.

The artifact contains no signature and always reports
`fundingEnabled: false`. The first version is classified as
`PayoutOrAuthorityAffecting`, so the contract applies its minimum 24-hour
approval-to-activation delay. `requestedActivatesAt` must be zero; an arbitrary
wall-clock value cannot be smuggled into a reusable release artifact.
The compiler caps a v1 release at 256 sellers, 1,024 resources, and a 1 MiB
canonical blob; every active seller must own at least one resource.

## Commands

Compile a reviewed real manifest:

```sh
go run ./cmd/ascp-directory-release compile \
  /secure/path/base-sepolia-directory-v1-manifest.json \
  deployments/base-sepolia-ascp-v4.json
```

Save that output outside the repository, then independently verify it:

```sh
go run ./cmd/ascp-directory-release verify \
  /secure/path/base-sepolia-directory-v1-artifact.json \
  deployments/base-sepolia-ascp-v4.json
```

Run the focused mutation and CLI tests with:

```sh
make test-ascp-directory-release
```

Before compiling or requesting a publisher signature, re-observe the exact
live predecessor through two independent RPC providers:

```sh
make verify-ascp-sepolia-directory-v1-readiness
```

That read-only gate also reuses the activation verifier, so the module-enabled,
allowlisted, zero-balance, zero-allowance, and exact Safe-confirmation boundary
must still hold. It additionally requires directory version/root `0`, no v1
proposal, and the original publisher/pauser epochs.

## Remaining consequential gates

After real content is supplied and reviewed, a separate short-lived publisher
authorization must bind the compiler's exact `functionSelector`,
`payloadHash`, workflow ID, epoch, operation ID, nonce, and at-most-10-minute
window. Only then may an untrusted relayer submit `proposeVersion`. The Safe
approval is a second transaction, activation cannot become effective for at
least 24 hours, and funding remains a later independent gate.

Both uploaded locations must be fetched and compared byte-for-byte with
`canonicalBlob` immediately before that authorization is signed. The compiler
proves the IPFS CID derivation and Arweave transaction-ID shape; it does not
claim remote availability without that network observation.

Directory publication does not authorize verifier activation, agent
registration, test-USDC approval/funding, or Base mainnet use.
