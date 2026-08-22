# FlowOps ASCP MCP Gateway

## Why

The MCP gateway gives agents one structured entry point to the FlowOps control
plane without granting them signing keys, policy authority, direct chain
access, or an administrative bypass.

## Entry and configuration

The control-plane executable mounts one Streamable HTTP endpoint at `/mcp`.
Every POST must contain one JSON-RPC 2.0 request, `Content-Type:
application/json`, and an `Accept` header with both `application/json` and
`text/event-stream`.

`FLOWOPS_MCP_ALLOWED_ORIGINS` is an optional comma-separated list of exact
HTTP(S) origins. If an `Origin` header is present, it must exactly match this
list. With no configured origins, browser-origin requests fail closed while
non-browser MCP clients without an Origin header remain supported. The same
trusted-proxy HTTPS enforcement as the REST API applies.

## Inputs and internal behavior

The gateway authenticates each request by asking the REST control plane for
`GET /v1/session` using the original bearer credential. It strictly decodes the
credential-derived principal kind, role, organization and read-only state,
filters `tools/list` to that principal, and enforces a second independent tool
audience/effect policy before delegation. It never accepts an organization,
agent, role or scope as a substitute for that credential.

Each advertised tool uses the same authenticated REST route and forwards the
same idempotency key where a write occurs:

| MCP tool | REST route | Idempotency |
|---|---|---|
| `ascp.operation.create` | `POST /agent/v1/intents` | Required |
| `ascp.operation.get` | `GET /agent/v1/intents/{operationId}` | Read-only |
| `ascp.operation.evaluate` | `POST /agent/v1/intents/{operationId}/evaluate` | One immutable decision per operation |
| `ascp.operation.decision.get` | `GET /agent/v1/intents/{operationId}/decision` | Read-only |
| `ascp.operation.authorize` | `POST /agent/v1/intents/{operationId}/authorization` | One immutable authorization per operation |
| `ascp.operation.authorization.get` | `GET /agent/v1/intents/{operationId}/authorization` | Read-only |
| `ascp.operation.activation.create` | `POST /agent/v1/intents/{operationId}/activation` | One server-derived activation request per operation |
| `ascp.operation.activation.get` | `GET /agent/v1/intents/{operationId}/activation` | Read-only |
| `ascp.intent.create` | `POST /v1/intents` | Required |
| `ascp.intent.get` | `GET /v1/intents/{requestId}` | Read-only |
| `ascp.approval.list` | `GET /v1/approvals` | Read-only |
| `ascp.approval.get` | `GET /v1/approvals/{requestId}` | Read-only |
| `ascp.approval.decide` | `POST /v1/approvals/{requestId}/decision` | Required |

The `ascp.operation.*` tools are the durable ASCP path. Their REST adapter
derives tenant/agent identity, configured deployment terms, and current
finalized directory evidence. Evaluation and authorization accept only the
owned operation ID; policy, commitment, approval and reservation inputs are
derived by the REST application boundary. These tools cannot invoke signer,
keeper, human approval-decision, or database
internals. Nested create and activation objects are strict JSON at the gateway:
unknown or duplicate identity, owner, admin, signer, keeper, evidence or chain
fields fail before delegation. The separately advertised human approval tools
remain protected by both the gateway role policy and their REST role and
step-up gates; agent credentials cannot discover or invoke them. The
`ascp.intent.*` names remain explicitly documented legacy compatibility tools
and do not create rows in `ascp_intents`.

Therefore, the existing role/scope checks, policy evaluation, reservation,
approval digest, step-up, durable command, audit, and lifecycle semantics
remain authoritative. Backend failures become typed MCP tool errors; they are
not presented as a successful payment or settlement.

## Outputs and redaction

Tool results include REST-equivalent `structuredContent`. The text block starts
with `FLOWOPS_UNTRUSTED_DATA_V1`, and `_meta` marks the complete backend result
as non-actionable untrusted data. Seller content, quote text and fake tool-call
shapes are never interpreted or dispatched by the gateway. Signature-key
variants, private-key material, seeds, mnemonic phrases, access/refresh tokens,
raw approval tokens, prepared signer handles, canonical payloads, evidence
bundles, raw transactions and calldata are removed recursively before either
result surface is written. Backend content type and a 1 MiB response ceiling
fail closed; duplicate JSON keys and non-object results are rejected, and JSON
numbers retain their exact lexical value rather than passing through binary
floating-point conversion. SellerQuote signatures are accepted only as create input and are
redacted from results. The gateway does not advertise signer, payment-status,
evidence, simulation, keeper, arbitrary RPC, chain-write or workflow tools.

`schemas/tools.json` describes the advertised inputs. Its SHA-256 is pinned in
`schemas/manifest.sha256`; tests reject drift. The manifest is an implementation
contract for this prototype slice, not the v3.4 production approval manifest.

## Failure states and acceptance

- Unknown or malformed JSON-RPC requests fail without reaching the control
  plane.
- Invalid or unconfigured Origins fail with HTTP 403.
- Missing or invalid credentials return an MCP authentication error.
- Malformed session claims fail authentication even when the backend returned
  HTTP 200.
- Agent credentials cannot list, read or decide human approvals; human
  credentials cannot invoke agent tools; read-only humans cannot decide.
- Every schema tool must have one explicit capability policy, and dependency
  graph tests reject a planted signer, keeper, wallet, chain or x402 path.
- Hostile seller strings remain escaped/untrusted data and cannot change the
  server-derived payee, request digest or approval action.
- Cross-tenant intent and approval IDs use the existing not-found boundary.
- A repeated mutation with the same key returns the REST command result; a
  changed input with that key remains an idempotency conflict.
- MCP has no session state, signing method, arbitrary RPC method, or SSE
  stream. GET returns 405 as allowed by Streamable HTTP.

The separately scoped official Base MCP adapter is documented in
`docs/modules/BASE_MCP_FIREWALL.md`.

Run `go test -race ./internal/mcp ./internal/basemcp ./internal/controlapi
./cmd/control-plane-api` for the focused schema, protocol, capability,
dependency, parity and runtime-routing suite.
