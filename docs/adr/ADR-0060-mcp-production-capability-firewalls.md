# ADR-0060: MCP production capability firewalls

Status: Accepted
Date: 2026-08-22

## Context

The first ASCP MCP slice reused REST authorization but treated any successful
session response as authentication, advertised human and agent tools together,
did not enforce its nested JSON Schemas at the gateway, and emitted arbitrary
backend strings without an explicit data-trust label. Its recursive redaction
matched only a few exact key spellings. The separately promised official Base
MCP connector firewall did not exist.

The official Base MCP now documents wallet reads alongside signing, sends,
swaps, raw batch contract calls and x402 operations. Base Account transaction
approval is a useful upstream safety step but cannot replace FlowOps policy,
approval, signer, Safe-module, finality or reconciliation boundaries.

## Decision

FlowOps adopts two independent fail-closed capability policies:

1. The ASCP MCP gateway strictly decodes the credential-derived principal,
   filters discovery by audience, and checks one schema-to-policy registry
   before its existing REST delegation. Agent, human-read and human-decision
   capabilities are distinct. Read-only state suppresses mutations.
2. `internal/basemcp` pins the exact official endpoint and a small set of
   advisory read tools. It has no network or credential implementation of its
   own; an injected invoker runs only after the firewall accepts the exact
   tool and arguments. All wallet writes and generic RPC are absent.

MCP input is duplicate-free strict JSON. Backend output is bounded,
duplicate-free JSON objects, preserves exact JSON numbers, is recursively
redacted, marked untrusted/non-actionable, and never interpreted as a tool
call. Official Base MCP reads are single-provider advisory metadata and cannot
advance economic state.

## Consequences

- A newly planted schema tool, upstream Base tool, signer/keeper/wallet import,
  owner parameter, provider override or secret-key spelling fails a test or
  fails closed at runtime.
- Humans cannot invoke agent tools through MCP and agents cannot discover or
  invoke approvals. REST remains a second authorization boundary.
- FlowOps does not gain a new signing or wallet custody surface.
- Upstream tool-name drift requires a reviewed allowlist change; availability
  is preferred below authority or economic safety.

## Verification

- `TestEveryAdvertisedToolIsBoundToTheAuthenticatedPrincipalClass`
- `TestOwnerAdminAndDuplicateParametersFailBeforeToolDelegation`
- `TestHostileBackendContentRemainsMarkedDataAndSecretsAreRedacted`
- `TestProductionFirewallDeniesEveryWalletAndOverrideCapability`
- `TestMCPProductionDependencyGraphHasNoWalletChainOrSignerPath`
- `TestDependencyGraphMetaTestDetectsPlantedViolation`
- dashboard rendered-HTML hostile-content escaping coverage
