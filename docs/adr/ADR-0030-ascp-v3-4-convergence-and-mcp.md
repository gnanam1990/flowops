# ADR-0030: ASCP v3.4 convergence and MCP first slice

Status: Accepted
Date: 2026-08-20

## Context

The Agent Spend Control Plane PRD v3.4 is the proposed next protocol profile
for FlowOps. Its reviewed source is retained outside this repository at the
following immutable content digest:

```text
SHA-256 77722a0139c08c0755eb48b712aa4c3e3971016c4db4d948e325f49853ffbc8e
agent-spend-control-plane-prd-v3-4-consolidated.md
```

The specification introduces an ASCP MCP server, governed directory, Safe
module, escrow-only v1 rail, schema manifest, and additional production gates.
Existing FlowOps already has a PostgreSQL-backed control plane, deterministic
policy, approval lifecycle, customer-managed reference signer, x402 adapter,
escrow reconciliation, and customer dashboard. Replacing those working
boundaries wholesale would discard their existing isolation, idempotency,
freeze, and chain-evidence tests.

## Decision

FlowOps remains the canonical product and repository. ASCP v3.4 is adopted as
the FlowOps v2 protocol and assurance target, not as a second product or a
greenfield rewrite.

The following current FlowOps boundaries remain in force until an explicit,
separately reviewed migration ADR replaces them:

- PostgreSQL remains the control-plane source of truth; SQLite is not adopted.
- The customer-managed signer remains the execution boundary. A Safe/module
  path is an additive, audited execution adapter, not an unreviewed swap.
- Direct x402 stays separately gated; escrow-only is the v2 protected-payment
  default, not a claim that existing direct flows are removed.
- Existing tenant, policy, approval, command, journal, and reconciliation
  contracts are reused by all new interfaces.

The first v2 implementation slice is the ASCP MCP server. It is a thin,
authenticated Streamable HTTP JSON-RPC façade over the existing control-plane
handler. It adds no signing capability, bypass route, identity override, or
economic state. Its mutating tools forward the original bearer credential and
idempotency key to the REST route, so the same authorization, policy,
reservation, approval, command, audit, and error paths apply.

The initial tool set is deliberately limited to lifecycle capabilities that
already exist and can be proven equivalent: `ascp.intent.create`,
`ascp.intent.get`, `ascp.approval.list`, `ascp.approval.get`, and
`ascp.approval.decide`. Directory, quote, evidence, chain simulation, and
workflow-proposal tools are not advertised until their backing application
services and schemas exist. An absent tool must fail as unsupported; no MCP
surface may fabricate a quote, payment, finality state, or signing result.

## Consequences

- MCP is shipped with JSON Schemas and a checked SHA-256 manifest before it is
  advertised. A source/schema mismatch is a build failure.
- Streamable HTTP uses one `/mcp` endpoint, validates Origin, accepts only
  JSON-RPC 2.0, bounds bodies, and requires bearer authentication for every
  request. It does not require or create an MCP session.
- Tool outputs carry structured REST-equivalent data and stable error metadata;
  a transaction hash is never presented as payment success.
- The first slice does not authorize production use. Safe/module,
  ServiceDirectory, escrow-call/1, Base MCP connector firewall, and the v3.4
  production gate remain separately scoped migrations.

## Verification gates

- Tool schemas and the manifest hash are tested.
- Tests prove malformed JSON-RPC, missing/invalid Origin, unauthenticated
  calls, unknown tools, unknown tool fields, cross-tenant reads, role misuse,
  idempotency replay/conflict, and HTTP/MCP outcome parity.
- `go test -race ./...`, `go vet ./...`, and formatting checks remain required
  before a change is shared.
