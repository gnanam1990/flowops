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
`GET /v1/session` using the original bearer credential. It never accepts an
organization, agent, role, or scope as a substitute for that credential.

Each advertised tool uses the same authenticated REST route and forwards the
same idempotency key where a write occurs:

| MCP tool | REST route | Idempotency |
|---|---|---|
| `ascp.intent.create` | `POST /v1/intents` | Required |
| `ascp.intent.get` | `GET /v1/intents/{requestId}` | Read-only |
| `ascp.approval.list` | `GET /v1/approvals` | Read-only |
| `ascp.approval.get` | `GET /v1/approvals/{requestId}` | Read-only |
| `ascp.approval.decide` | `POST /v1/approvals/{requestId}/decision` | Required |

Therefore, the existing role/scope checks, policy evaluation, reservation,
approval digest, step-up, durable command, audit, and lifecycle semantics
remain authoritative. Backend failures become typed MCP tool errors; they are
not presented as a successful payment or settlement.

## Outputs and redaction

Tool results include a text JSON representation and `structuredContent`.
Signature bytes, private-key material, seeds, mnemonic phrases, access tokens,
raw approval tokens, and calldata fields are removed before an MCP result is
written. The gateway does not advertise any unavailable directory, quote,
payment-status, evidence, simulation, or workflow tool.

`schemas/tools.json` describes the advertised inputs. Its SHA-256 is pinned in
`schemas/manifest.sha256`; tests reject drift. The manifest is an implementation
contract for this prototype slice, not the v3.4 production approval manifest.

## Failure states and acceptance

- Unknown or malformed JSON-RPC requests fail without reaching the control
  plane.
- Invalid or unconfigured Origins fail with HTTP 403.
- Missing or invalid credentials return an MCP authentication error.
- Agent credentials cannot list, read, or decide human approvals.
- Cross-tenant intent and approval IDs use the existing not-found boundary.
- A repeated mutation with the same key returns the REST command result; a
  changed input with that key remains an idempotency conflict.
- MCP has no session state, signing method, arbitrary RPC method, or SSE
  stream. GET returns 405 as allowed by Streamable HTTP.

Run `go test -race ./internal/mcp ./internal/controlapi ./cmd/control-plane-api`
for the focused schema, protocol, parity, and runtime-routing suite.
