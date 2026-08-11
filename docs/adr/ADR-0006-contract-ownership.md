# ADR-0006: Contract Ownership and Upgrade Posture

Status: Provisional; security/legal review required  
Date: 2026-08-11

## Decision

The pilot prefers small, non-upgradeable Base contracts with constructor-bound dependencies and no FlowOps unilateral fund-movement role. Registry administration, if retained, belongs to the service provider for its own listing. Customer funds move only under contract-defined customer/provider/time conditions.

Deployment configuration, compiler settings, constructor arguments, source verification, runtime bytecode, ABI, owner/admin addresses, and deployment transaction hashes are recorded in a revisioned address registry.

If an emergency pause or resolver is introduced, its authority must be explicit, narrowly scoped, timelocked or multisig-controlled where compatible with incident response, and unable to redirect customer funds to an arbitrary address.

## Consequences

- Contract bugs require migration rather than proxy upgrade.
- FlowOps must design versioned registries and UI warnings for retired contracts.
- Adding dispute resolution or pause powers materially changes the legal/security posture and requires a new ADR.

## Acceptance gate

Verified Base source/runtime equivalence, immutable/config getter checks, role-abuse tests, deployment rehearsal, migration/runbook, external security review, and documented key ownership are required before mainnet.
