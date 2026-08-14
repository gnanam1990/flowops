# ADR-0022: Base Mainnet Production RPC Admission

Status: Accepted for implementation; provider selection remains open
Date: 2026-08-14

## Context

The Base observer already required two distinct HTTPS hostnames. Hostname
diversity is necessary but does not prove that two endpoints have different
operators, infrastructure failure domains, paid capacity, or production
support. The public Base and PublicNode endpoints are appropriate for read-only
preflight, but Base documents public RPCs as rate limited and unsuitable for
production. Accidentally promoting those endpoints would make the chain-health
quorum fail through one shared operational assumption.

Credential-bearing RPC URLs are secrets. Provider independence metadata is not
secret and must remain reviewable without copying API keys into a readiness
record, log, command argument, or status response.

## Decision

Production configuration is split into two exact JSON documents:

- `FLOWOPS_BASE_RPC_PROVIDERS_JSON` contains only provider names and secret
  HTTPS URLs. The complete value is injected by the deployment secret manager.
- `FLOWOPS_BASE_RPC_ADMISSION_JSON` contains schema version 1 and one reviewed
  record for every provider name: operator, failure domain, `paid` service tier,
  and an explicit production-eligibility decision. It contains no URL or token.

For Base mainnet, startup fails before observer construction unless:

- every secret provider has exactly one admission record;
- two to five provider names and HTTPS hosts are unique;
- operator identities are unique;
- reviewed failure domains are unique;
- every tier is exactly `paid` and every eligibility flag is `true`; and
- no endpoint uses a known Base or PublicNode public mainnet hostname.

Base Sepolia rejects production-admission metadata so a test declaration cannot
be mistaken for mainnet evidence. A standalone `rpc-admission-check` command and
smoke test exercise the same parser without network access. They receive URLs
only through environment variables and print only chain ID, count, and the
boolean admission result.

The customer-owned reference signer applies the same rule before constructing
its observer or wallet adapter. Its strict configuration advances to version 4
and carries the URL-free admission object beside the secret provider list;
version 3 is rejected so an older mainnet signer cannot bypass the new gate.

The allow/deny decision is a deployment assertion, not automatic proof of a
commercial relationship or true infrastructure separation. Promotion evidence
must still name the selected operators, document the reviewed failure domains
and paid plans, and record a live dual-provider drill. The canonical readiness
record therefore marks the admission mechanism implemented while leaving
`independentPaidRpcProviders` false.

## Consequences

- Distinct aliases or hostnames cannot conceal one declared operator or failure
  domain.
- Known public endpoints cannot satisfy production startup even if mislabeled
  as paid.
- Secret URLs remain outside committed admission and readiness evidence.
- Adding a new provider requires both a secret endpoint and a reviewed metadata
  change; partial or stale bindings fail closed.
- A dishonest metadata declaration is still possible and is controlled by the
  promotion review and provider evidence, not by hostname heuristics.

## Acceptance gate

- exact canonical configuration passes;
- duplicate fields, unknown fields, missing bindings, public endpoints, free
  tiers, ineligible services, shared operators, and shared failure domains fail;
- errors and successful output never contain a credential-bearing URL;
- the current mainnet promotion gate still refuses startup after valid RPC
  admission; and
- the mainnet readiness record does not claim providers have been selected.
