# ASCP governance workflow bindings

## Contract

The canonical payload is:

```text
keccak256(abi.encode(
  keccak256(domain),
  chainId,
  contractAddress,
  workflowId,
  functionSelector,
  keccak256(abi.encode(action fields...))
))
```

Domains are `ASCP_CALL_ESCROW_GOVERNANCE_V1`,
`ASCP_SPEND_MODULE_GOVERNANCE_V1`, and
`SERVICE_DIRECTORY_GOVERNANCE_V1`. Only Base mainnet and Base Sepolia are
accepted by the Go builder. Addresses and hashes are canonical lowercase hex;
ambiguous decimals, invalid state tuples, duplicate nonce invalidations, and
unsupported directory change classes are rejected. No-op allowlist/pause
changes, invalid caps, and empty or greater-than-100 nonce batches are also
rejected, so a binding event always accompanies an actual bounded mutation.
Verifier revocation binds and cancels both active and pending epoch state, and
retains the highest cancelled epoch as a revoked monotonic tombstone. A
mistakenly scheduled key can therefore be neutralized before permissionless
activation without allowing a later lower-epoch replacement. Revocation is
permanent for that verifier address; a later rotation must use a different key
address and cannot revive a revoked key during its activation delay.

The action fields include current-state preconditions as well as proposed
values. This makes an approval stale if verifier state, authorizer epoch,
allowlist code hash, active caps, or pause state changes before Safe execution.
Verifier additions also bind any already-pending verifier epoch and activation
time. A second cap schedule is rejected while one is pending.

## Receipt boundary

Every successful governed call emits:

```text
GovernanceWorkflowBound(workflowId, workflowPayloadHash, functionSelector)
```

in the same transaction as its action-specific event. A future independent
observer must retrieve a canonical finalized receipt from the configured RPC
quorum and verify chain, contract, transaction, both events, workflow ID,
payload hash, selector, and one-time receipt ownership. The public workflow API
still has no completion endpoint.

`addVerifier`, `scheduleCaps`, and `approveVersion` emit their binding when the
change is scheduled. Their later permissionless activation is a separate chain
state transition. A receipt observer must use action-specific rules: it may
close a workflow for the scheduled action, but must not advertise the verifier,
caps, or directory root as effective until finalized activation is observed.

This slice does not bind Safe module enable/disable, deployment ceremonies,
directory publisher/pauser rotation, or Agent Registry admin rotation. Those
surfaces need explicit workflow-kind mappings and receipt semantics; a generic
Safe receipt is not sufficient.

## Verification

```sh
go test -race ./pkg/governanceworkflow
forge test --match-contract 'ASCPCallEscrowTest|ServiceDirectoryTest|ASCPSpendModuleTest|ASCPSpendModuleInvariantTest' -vv
```

The suite covers cross-language golden vectors, chain/contract/state/value
mutations, workflow substitution, stale verifier state, pending-cap overwrite,
Safe/governor authorization, fuzzed caps, and stateful spend invariants.
