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
Seller pause and quote-key revocation bind both current and requested state.
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
Directory approval additionally includes the proposal's `proposerNonce`. The
server recomputes `ServiceDirectory.hashProposal` from the proposal domain,
Base chain, directory address, exact workflow ID/payload hash, every proposal
field, and that nonce. The executable `approveVersion` calldata therefore
cannot select a different stored proposal hash.

Persisted JSONB is re-decoded under the closed action schema and rebound at
approval, execution-command creation, and receipt observation. Key ordering and
whitespace changes are harmless; unknown fields, mismatched payload/calldata,
extra variants, and explicit-null ambiguity fail closed.

## Receipt boundary

Every successful governed call emits:

```text
GovernanceWorkflowBound(workflowId, workflowPayloadHash, functionSelector)
```

in the same transaction as its action-specific event. The independent observer
discovers the binding with `eth_getLogs`, re-reads the successful receipt and
canonical block from the configured RPC quorum, and verifies chain, contract,
transaction, both events, workflow ID, payload hash, selector, finality, and
one-time `(chainId, transactionHash, bindingLogIndex)` ownership. The public
workflow API still has no completion endpoint.

`addVerifier`, `scheduleCaps`, and `approveVersion` emit their binding when the
change is scheduled. Their later permissionless activation is a separate chain
state transition. A receipt observer must use action-specific rules: it may
close a workflow for the scheduled action, but must not advertise the verifier,
caps, or directory root as effective until finalized activation is observed.

Every stored chain workflow also carries a server-derived immutable
`chainAction`. Optional deployment rules in
`FLOWOPS_ASCP_CHAIN_AUTHORITY_RULES_JSON` add exact runtime-code, on-chain
principal, relayer, role/quorum, timelock, selector and event constraints to
the observer's closed action allowlist. Safe module and owner operations use
distinct action names; Safe owner swap requires both `RemovedOwner` and
`AddedOwner` events, so a generic Safe receipt cannot stand in for an exact
action proof.

## Verification

```sh
go test -race ./pkg/governanceworkflow
forge test --match-contract 'ASCPCallEscrowTest|ServiceDirectoryTest|ASCPSpendModuleTest|ASCPSpendModuleInvariantTest' -vv
```

The suite covers cross-language golden vectors, chain/contract/state/value
mutations, workflow substitution, stale verifier state, pending-cap overwrite,
Safe/governor authorization, fuzzed caps, and stateful spend invariants.
