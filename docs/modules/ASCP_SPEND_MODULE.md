# ASCP Spend Module

## Why

`ASCPSpendModule` is the on-chain least-authority boundary between a hardware-backed spend-authorizer signature and the operational Safe. It permits routine escrow purchases without Safe-owner signatures while preventing arbitrary Safe calls, value transfer, delegatecall, unbounded allowance, and cross-Safe replay.

## Entry points

- `executeLock(payload, authorization, signature)`: permissionless relay of one exact escrow lock.
- `executeAllowance(payload, authorization, signature)`: permissionless relay of one exact token approval.
- `lockAuthorizationDigest` / `allowanceAuthorizationDigest`: signer and conformance digest surfaces.
- Safe-only governance: `setSpendAuthorizer`, `setEscrowAllowlist`, `scheduleCaps`, `setEmergencyPause`, `invalidateNonces`.
- Delayed public finalizer: `activateCaps` after the Safe-approved one-hour delay.

## Inputs and bindings

Lock authorizations bind `orgDomain`, Safe, module, operation, commitment digest, exact calldata hash, escrow, amount, `uint256` nonce, validity window, leadership epoch, and authorizer epoch. Allowance authorizations bind `orgDomain`, Safe, module, admin operation, configured token, spender, exact old and new allowance, `uint256` nonce, validity window, leadership epoch, and authorizer epoch. Both use the EIP-712 domain `{name: ASCP, version: 4, chainId, verifyingContract: module}`.

The module accepts a bearer instrument only when:

- the module is not paused;
- action IDs, organization domain, and nonce are nonzero;
- `validBefore > validAfter`, the window is at most 600 seconds, and `validAfter <= chain time < validBefore`;
- the message's signed module is this contract and the leadership epoch is nonzero;
- the signed authorizer epoch exactly equals the current epoch;
- the nonce is neither consumed nor Safe-invalidated; and
- ECDSA recovery equals the current spend authorizer.

## Internal behavior

For a lock, the module verifies the payload hash, selector, full ABI decode/re-encode equality, allowlisted escrow runtime code hash, operation ID, escrow, configured token, amount, and the escrow's shared execution-commitment digest. It checks per-transaction and UTC-day caps, consumes the nonce, increments `executedPrincipal` and the day counter, and asks the Safe to execute the original exact payload with value zero and operation `CALL`. A false Safe result reverts every write.

For an allowance, the module reconstructs the only permitted `approve(spender,newAllowance)` calldata, checks the spender runtime code hash, reads the Safe's exact current allowance, and enforces `newAllowance <= allowanceCeiling`. It consumes the nonce and asks the Safe to call only the immutable token. Allowance actions do not affect spend counters.

## Outputs and interfaces

Successful actions emit `LockExecuted` or `AllowanceExecuted`; governance emits authorizer, allowlist, cap, pause, and nonce-invalidation events plus the exact `GovernanceWorkflowBound` receipt event. Every Safe-only governance function now requires `workflowId` and `workflowPayloadHash`; the hash binds chain, module, workflow ID, selector, current-state preconditions, and proposed values. Public state supports reconciliation of authorizer epoch, pause, allowlist code hashes, consumed nonces, pending/active caps, lifetime executed principal, and per-day executed principal.

The Safe must implement `execTransactionFromModule(address,uint256,bytes,uint8) returns (bool)` and enable this module through its owner-governed module lifecycle. The configured token must expose ERC-20 `allowance` and `approve`. Escrows must expose the reviewed `ASCPCallEscrow` ABI and exact runtime code hash.

The isolated signer uses `pkg/spendauthorization` to construct the byte-identical Go digests. `schemas/ascp-typed-data-v4.registry.json` pins all six normative v4 message types to their JSON Schemas and signed vectors. The dependency-free TypeScript SDK under `sdk/typescript` independently computes Ethereum Keccak, ABI words, domain separators, struct hashes, digests, and RFC 8785-compatible canonical JSON. Go, TypeScript, and Solidity tests prevent type-string, field-order, domain, module, nonce-width, or integer-encoding drift.

## UI and operations

The owner UI must render the exact current and proposed authorizer epoch, allowlist address and runtime code hash, active and pending caps with activation time, pause state, and every nonce selected for invalidation. Routine lock and allowance relays require no Safe-owner prompt. Safe governance always uses the normal 2-of-3 owner transaction flow.

Operations monitor every governance event, cap utilization, failed Safe execution, nonce consumption, unexplained Safe outflow, and allowance drift. Migration is deploy, verify bytecode, allowlist escrows, enable successor, freeze/drain the predecessor, and disable the predecessor; there is no proxy upgrade.

## Failure and recovery

- Invalid, expired, replayed, cross-Safe, cross-module, wrong-epoch, or wrong-signer authorizations revert before Safe execution.
- Selector, target, token, spender, payee/commitment, amount, calldata suffix, allowance-state, and code-hash mutations revert.
- Cap breaches consume no nonce or counter.
- A downstream Safe false result rolls back nonce and counters.
- Pause blocks execution but does not itself release off-chain budget.
- Finalized `NonceInvalidated` evidence or proven chain-time expiry with an unused nonce is required to retire an unconsumed bearer reservation.

## Acceptance evidence

- Focused Foundry mutation tests cover exact-call execution, replay, cross-Safe binding, epoch rotation A-to-B-to-A, pause/invalidation, code allowlisting, cap delay/breach, allowance drift, wrong signer, expiry, calldata suffix, and downstream rollback.
- Foundry fuzzing exercises cap boundaries; stateful invariants compare successful principal, lifetime/day counters, and zero-value `CALL` behavior.
- Go tests and shared signed vectors cover both authorization encoded bytes, canonical JSON, domain separators, digests, and recovered signers; TypeScript covers all six normative types.
- Full repository Forge, Go, dashboard, deployment, and readiness gates must pass before merge.
- Production-equivalent Safe/module/keeper testnet evidence and independent review remain explicit production gates, not claims made by the local suite.
