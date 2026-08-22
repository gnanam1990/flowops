# Official Base MCP capability firewall

## Boundary

`internal/basemcp` is the only approved adapter boundary for the official
wallet-capable Base MCP endpoint `https://mcp.base.org`. It is not a wallet
integration and owns no OAuth credential, private key, session key, signer,
approval link, RPC client or transaction broadcaster. A caller supplies an
`Invoker`; the adapter evaluates the exact tool and arguments before that
transport can run.

The upstream service currently documents both read tools and wallet writes.
FlowOps pins only these advisory reads:

| Tool | Permitted fields | FlowOps use |
|---|---|---|
| `get_wallets` | none | Advisory wallet inventory |
| `get_portfolio` | `address`, `chain`, `query`, `includePnl`, `limit`, `offset` | Advisory balances only |
| `search_tokens` | required `query`; optional `chain` | Advisory token metadata only |
| `get_transaction_history` | required `chain`; optional `address`, `asset`, `limit` (1–200), `cursor` | Advisory history only |
| `get_request_status` | required `requestId` | Advisory status of an externally created Base request only |

Every result is tagged `ADVISORY_SINGLE_PROVIDER`. It cannot satisfy chain
finality, payment, receipt, balance, settlement, refund, policy, directory,
allowance or reconciliation evidence. Those decisions still require the
existing independent Base observers and canonical reconciliation path.

## Production denials

The adapter denies unknown tools and therefore denies `sign`, `send`, `swap`,
`send_calls`, x402 payment/write tools, generic `chain_rpc_request`, arbitrary
contract calls and allowance calls. It also denies private-key, provider/RPC,
identity, destination or single-provider override fields on allowed read tools.
The official endpoint is an exact HTTPS pin: alternate hosts, schemes, paths,
userinfo, queries and fragments fail startup.

Base Account approval is not FlowOps approval. Even if the upstream tool would
return a user approval link, the production adapter never calls that write tool
and cannot convert it into an ASCP authorization or economic effect.

## Data and drift behavior

Arguments are bounded strict JSON objects with exact field/type policies,
required fields and documented history limits enforced, duplicate keys
rejected, and no nested arbitrary payload. Results must be duplicate-free JSON
objects under 1 MiB and remain untrusted advisory data. A newly advertised or
renamed upstream tool is denied until a reviewed source/schema update changes
the pinned allowlist.

The pinned names were reconciled on 2026-08-22 against the official Base MCP
guides at `https://docs.base.org/agents/quickstart`,
`/agents/guides/check-balance`, and `/agents/guides/view-history`. The same
official documentation identifies `send`, `send_calls`, signing, swaps,
contract calls and x402 as wallet-capable operations; they are outside this
adapter by design.

## Verification

```sh
go test -race ./internal/basemcp ./internal/mcp
```

The suite proves every pinned read, every prohibited capability class,
endpoint/argument/result substitution, duplicate keys, fail-closed upstream
errors, and a dependency meta-test that catches a planted wallet, signer,
keeper, chain, x402 or reconciliation import.
