# ADR-0024: Fail-closed CallEscrow security-review package

- Status: accepted
- Date: 2026-08-14

## Context

Base mainnet promotion requires an independent security review, but no review
has occurred. A generic checklist could be mistaken for evidence of completion,
could drift from the actual source, or could silently imply that offchain
signer and reconciliation systems were reviewed with the contract.

## Decision

Commit a machine-checked package that binds the exact contract, deployment
script, build configuration, dependency commits, compiler settings, ABI,
storage layout, method identifiers, source rehearsal, threat model, tests, and
known limitations. Bind the package to the last commit containing the exact
production source under review.

Keep external-review completion, reviewer identity, report digest, completion
date, retest status, and unresolved-severity counts empty. The readiness record
may state only that the package is prepared. The contract keeps its explicit
`UNAUDITED` mainnet prohibition. The hardware deployment wrapper requires both
the prepared canonical package and, later, a real completed report digest.

Contract/deployment-ceremony review does not clear customer signer, control
plane, reconciliation, RPC operations, legal, provider, or frontend gates.

## Consequences

- An external reviewer receives a reproducible, immutable scope instead of a
  floating branch.
- ABI, storage, compiler, dependency, source, or ceremony drift fails CI.
- Passing local tests and automated checks cannot be confused with an external
  audit.
- A future remediation changes the reviewed commit and requires manifest and
  report bindings to be regenerated and independently retested.
- Mainnet remains blocked until a separate reviewed promotion PR records the
  real reviewer and final report digest and clears all other gates.
