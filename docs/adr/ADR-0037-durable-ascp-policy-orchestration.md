# ADR-0037: Durable ASCP policy and authorization orchestration

## Status

Accepted for the buildable Base pilot slice.

## Decision

FlowOps persists one append-only policy decision per durable ASCP operation.
The server reconstructs the active policy, canonical PurchaseSpec, SellerQuote,
current reservation spend, organization/agent state, and EIP-712
ExecutionCommitment. Agents may select only their own operation ID; they cannot
submit a policy outcome, review snapshot, approval, budget dimension, deadline,
or execution snapshot.

`REQUIRE_APPROVAL` creates one exact, expiring approval in the same serializable
transaction as the policy decision. `AUTO_APPROVE` records a distinct automatic
decision reference and never fabricates a human approval. `DENY` creates no
approval or reservation. A later authorization request replays an existing
authorization first; otherwise it rechecks the current immutable approval or
automatic decision, commitment window, policy, pause, directory, quote and
budget state before atomically inserting the authorization and reservation.

The server derives `acceptBy`, `deliverBy`, and `settleBy`. The configured
acceptance window is at most 15 minutes. Delivery includes declared work time,
at least the contract's 120-second verification buffer, and a two-minute
processing margin. Settlement uses the reviewed escrow release window and must
be between 30 minutes and 30 days. Missing that immutable window requires a new
operation; the system does not silently rewrite an approved commitment.

## Consequences

- Policy and approval history survives later policy removal or rotation.
- Automatic and human authority remain distinguishable in storage and signer
  evidence.
- Cross-tenant reads and decisions include the organization predicate in SQL.
- Existing authorizations replay without depending on current mutable policy or
  directory availability.
- This boundary produces no signature, transaction, settlement, ledger entry,
  delivery claim, or refund claim.
