# Base Sepolia observer smoke evidence — 2026-08-11

Status: passed read-only quorum smoke

## Scope

The `cmd/base-observer` process was run without any wallet key, signer, settlement request, or broadcast capability. It queried standard Ethereum JSON-RPC methods only.

Base's documentation identifies chain ID `84532` and `https://sepolia.base.org` for Base Sepolia, notes that the public Base endpoint is rate limited, and recommends a node partner for production. The same Base quickstart names PublicNode as an alternative public endpoint.

References:

- <https://docs.base.org/base-chain/api-reference/rpc-overview>
- <https://docs.base.org/base-chain/quickstart/connecting-to-base>
- <https://base-sepolia.publicnode.com/>
- <https://drpc.org/docs/base-api>

## Degraded observation

The first run used Base's public endpoint plus PublicNode:

```sh
go run ./cmd/base-observer \
  -chain-id 84532 \
  -timeout 20s \
  -rpc base_public=https://sepolia.base.org \
  -rpc publicnode=https://base-sepolia-rpc.publicnode.com
```

At `2026-08-11T15:56:14Z`, PublicNode returned Base Sepolia head and anchor block `45347742`, while `sepolia.base.org` returned HTTP 503 for `eth_getBlockByNumber`. The command returned exit status 2 with one observation and one named failure. It did not manufacture a quorum from the surviving endpoint.

## Passing independent-host observation

The second run used dRPC plus PublicNode:

```sh
go run ./cmd/base-observer \
  -chain-id 84532 \
  -timeout 20s \
  -rpc drpc=https://base-sepolia.drpc.org \
  -rpc publicnode=https://base-sepolia-rpc.publicnode.com
```

At `2026-08-11T15:56:36Z`, both providers returned:

- chain ID: `84532`;
- head and common anchor block: `45347753`;
- block hash: `0xa7acf894776067fdad74ca1c5433150c8d63822ac5d08d04c24a67ebe471b2c4`;
- block timestamp: `2026-08-11T15:56:34Z`.

The command exited successfully with two provider-specific observations and no failures.

## Interpretation

This proves the read-only observer can:

- validate Base Sepolia chain identity;
- keep provider observations separate;
- find and compare an exact common anchor;
- fail visibly when a reachable configured dependency does not produce usable block data;
- reach two-provider agreement on live Sepolia data.

It does not select production providers, establish operational independence beyond distinct provider hosts, set mainnet thresholds, prove a transaction receipt, or satisfy the later signed end-to-end halt/recovery drill.
