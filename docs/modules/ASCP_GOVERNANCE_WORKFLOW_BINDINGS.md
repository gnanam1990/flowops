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
`SERVICE_DIRECTORY_GOVERNANCE_V1`; AgentRegistry admin rotation uses
`AGENT_REGISTRY_GOVERNANCE_V1`. Only Base mainnet and Base Sepolia are
accepted by the Go builder. Addresses and hashes are canonical lowercase hex;
ambiguous decimals, invalid state tuples, duplicate nonce invalidations, and
unsupported directory change classes are rejected. No-op allowlist/pause
changes, invalid caps, and empty or greater-than-100 nonce batches are also
rejected, so a binding event always accompanies an actual bounded mutation.
Directory publisher, directory pauser, and registry-admin rotations bind the
current authority and epoch as well as the next authority, and reject no-ops.
Seller pause and quote-key revocation bind both current and requested overlay
state.
Verifier revocation binds and cancels both active and pending epoch state, and
retains the highest cancelled epoch as a revoked monotonic tombstone. A
mistakenly scheduled key can therefore be neutralized before permissionless
activation without allowing a later lower-epoch replacement. Revocation is
permanent for that verifier address; a later rotation must use a different key
address and cannot revive a revoked key during its activation delay.
The Go verifier builders require the caller's exact revoked-state snapshot and
refuse to construct either add or revoke payloads when that snapshot is true.

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

in the same transaction as its action-specific event. `AuthorityVerifier`
closes the approved workflow only from at least two distinct finalized-provider
observations that agree on transaction/block identity and exactly match the
deployment-owned rule for chain, contract, runtime code hash, on-chain
principal, workflow roles/quorum, relayer, selector, action event, workflow
event, workflow ID, payload hash, and minimum timelock. The public workflow API
has no completion endpoint; only the internal receipt observer can call the
completion boundary.

`addVerifier`, `scheduleCaps`, and `approveVersion` emit their binding when the
change is scheduled. Their later permissionless activation is a separate chain
state transition. A receipt observer must use action-specific rules: it may
close a workflow for the scheduled action, but must not advertise the verifier,
caps, or directory root as effective until finalized activation is observed.

Every chain-changing workflow now carries an immutable `chainAction`. The
control-plane runtime accepts such actions only when
`FLOWOPS_ASCP_CHAIN_AUTHORITY_RULES_JSON` supplies an exact reviewed rule.
Unmapped actions, a reused provider identity, provider disagreement, wrong
principal/relayer/code hash, selector or event substitution, insufficient
timelock, and same-principal approval all fail before completion. Safe module
enable/disable and owner rotation have distinct action names; a generic Safe
receipt cannot stand in for their configured receipt semantics. The action
matrix fixes selectors and event topics in code, and Safe owner swap requires
both `RemovedOwner` and `AddedOwner` logs.

The authority proof is normalized evidence, not an Owner-supplied claim. RPC
credentials and raw responses stay in the independent observer. The observer
must derive the on-chain principal from the verified contract/Safe execution
path and the relayer from the outer transaction, then retain the raw receipt,
block, code, and trace evidence behind each provider record.

## Verification

```sh
go test -race ./pkg/governanceworkflow
forge test --match-contract 'ASCPCallEscrowTest|ServiceDirectoryTest|ASCPSpendModuleTest|ASCPSpendModuleInvariantTest' -vv
```

The suite covers cross-language golden vectors, chain/contract/state/value
mutations, workflow substitution, stale verifier state, pending-cap overwrite,
Safe/governor authorization, fuzzed caps, and stateful spend invariants.
