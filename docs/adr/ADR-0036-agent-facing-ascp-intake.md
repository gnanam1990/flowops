# ADR-0036: Agent-facing durable ASCP intake

Status: Accepted
Date: 2026-08-21

## Decision

Mount the durable ASCP operation boundary separately from the legacy
`PaymentIntent` API:

- `POST /agent/v1/intents` and MCP `ascp.operation.create` create an immutable
  operation and atomically claim the SellerQuote nonce;
- `GET /agent/v1/intents/{operationId}` and MCP `ascp.operation.get` return only
  the redacted operation projection under an exact tenant-and-agent predicate;
- the authenticated bearer principal supplies organization and agent identity;
  those fields cannot appear in the request body;
- the adapter builds canonical PurchaseSpec bytes from untrusted request data,
  constrains chain, native-USDC asset and scheme from deployment configuration,
  and reads the configured directory's current finalized materialized head;
- the directory head, observation time, version and quote evidence are obtained
  in one SQL statement so a concurrent head advance cannot combine evidence
  from two versions; and
- the durable operation request has no signer, keeper, approval-decision,
  policy-snapshot, directory-evidence, or arbitrary chain-call input.

The legacy `ascp.intent.*` MCP names remain compatibility aliases for the
pre-ASCP `/v1/intents` lifecycle and are explicitly labeled as such. They are
not silently redirected because that would change their request and economic
semantics.

## Failure and rollout contract

The route is always registered. Without a configured
`FLOWOPS_ASCP_DIRECTORY_CONTRACT`, it returns a retriable fail-closed 503. A
missing or stale current observation also returns a retriable error. A stale
quote version, unknown current leaf, malformed quote, exact-body mismatch,
signature failure, idempotency conflict, or already-owned quote nonce is never
converted into a successful operation.

This decision wires intake and read status only. It does not claim policy
approval, execution authorization, signer activation, relay, settlement, or
accounting completion; those remain separate state-machine boundaries.
Adaptation-grant issuance and atomic consumption are also not implemented in
this slice; `adaptationGrantId` is therefore rejected as an unknown field
rather than accepted without enforcement.
