# ADR-0058: Chain governance receipts carry exact workflow bindings

## Status

Accepted for the pre-alpha contracts. Deployment and independent receipt
observation remain separate gates.

## Decision

The chain-backed proposal actions mapped in this slice must carry a nonzero
workflow ID and the exact payload hash approved by the control plane. They are
verifier add/revoke and emergency pause on `ASCPCallEscrow`; authorizer,
allowlist, cap, pause, and nonce governance on `ASCPSpendModule`; and directory
version approval/cancellation on `ServiceDirectory`. The contract recomputes
that hash from a versioned domain, chain ID, its own address, the exact workflow
ID, the concrete function selector, and ABI-encoded action values. A mismatch
reverts before state changes; a correct action payload cannot be relabelled as
a different workflow.

Successful calls emit the action-specific event and the shared
`GovernanceWorkflowBound(workflowId, workflowPayloadHash, functionSelector)`
event in the same receipt. `ServiceDirectory.approveVersion` emits the binding
stored in the immutable proposal; cancellation has its own workflow binding.

Mutable preconditions are part of the approved payload where they affect the
meaning of the change: verifier active/pending epochs, authorizer address and
epoch, allowlist code hash, active caps, and pause state. A scheduled cap change
cannot be overwritten before activation. Duplicate verifier revocation and
duplicate pause transactions fail closed.

Go computes the same hashes in `pkg/governanceworkflow`. Published
cross-language vectors prevent selector, enum, integer-width, field-order,
domain, chain, and contract-address drift.

The public workflow API accepts the caller-selected workflow ID and one typed
action, not a payload hash or calldata. The Go builder derives and persists the
canonical action, payload, selector, and full call. The same configured
chain/contract/selector map authorizes proposal creation and later receipt
discovery, preventing an arbitrary but ABI-compatible target from reaching the
approval outbox.
For directory approval, the builder also derives the exact immutable proposal
hash from the directory proposal domain, chain, contract, workflow binding,
proposal fields, and proposer nonce; a caller cannot pair approval semantics
with the hash of a different stored proposal.

## Consequences

- The changed governance function selectors are intentionally ABI-breaking.
  These pre-alpha contracts are non-upgradeable, so any earlier deployment is
  incompatible and must not be promoted; reviewed successors must be deployed,
  verified, and reconfigured through the Safe ceremony.
- The receipt observer requires both the exact action event and the shared
  binding event before finalizing a workflow. It obtains a
  canonical finalized receipt from the configured independent RPC quorum,
  verifies chain/contract/transaction/log identity and one-time receipt ownership,
  and rejects disagreement, reorg, or replay. No public route accepts a
  caller-supplied receipt or transaction hash.
- Verifier addition, cap scheduling, and directory approval receipts prove the
  scheduled mutation, not later activation. The observer must apply
  selector-specific completion semantics and separately reconcile activation
  before reporting the new authority/caps/root as effective.
- The Safe remains the on-chain quorum. This binding proves which approved
  FlowOps workflow a successful Safe action claims and prevents payload
  substitution; it does not turn a single control-plane credential into a Safe
  owner or bypass Safe signature policy.
- Safe module enable/disable, constructor/deployment ceremonies,
  `ServiceDirectory` publisher/pauser rotation, and `AgentRegistry` admin
  rotation are not covered by these selectors. They require separately mapped
  workflow payloads and receipt rules before the control plane can close their
  chain-backed workflows.
