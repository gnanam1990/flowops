# ASCP v3.4 acceptance inventory

This directory is the machine-checked acceptance inventory for the consolidated ASCP v3.4 PRD. It measures delivery against the PRD's 88 acceptance criteria, rather than treating a code commit or a passing unit test as proof that an entire product module is complete.

The manifest pins specification SHA-256 `77722a0139c08c0755eb48b712aa4c3e3971016c4db4d948e325f49853ffbc8e`. It also pins digest `58377488c19e2dbe96498e3b61b58048aa236c588620b3810873640fdca3b3f9` over every exact AC identifier, scenario, expected outcome, and active/reserved classification. It contains 83 active criteria and five explicitly reserved direct-rail criteria: AC-20, AC-35, AC-39, AC-49, and AC-52.

## Status meaning

- `local_evidence`: named repository tests exist and resolve, but release-grade evidence is not yet complete.
- `partial`: useful implementation evidence exists, but part of the criterion remains unproved.
- `missing`: the required implementation or executable evidence is absent.
- `external_required`: completion depends on a testnet, production-like, multisig, operator, or external-review ceremony.
- `accepted`: every required artifact exists, the full criterion is proved, and no recorded gap remains.
- `reserved`: the PRD explicitly excludes the criterion from the active escrow-first release.

An `accepted` claim is intentionally stricter than “tests pass.” The manifest begins at the `inventory` stage with no accepted claims. Promotion must add durable evidence for every required artifact and close the recorded gap. `freeze` and `production` stages reject any active criterion that is not accepted.

## Run the gate

```sh
make acceptance-manifest-check
```

For machine-readable totals:

```sh
go run ./cmd/acceptance-report -json
```

The validator rejects duplicate or missing criteria, unauthorized reserved-state changes, incomplete ownership, path traversal, stale evidence file or test references, accepted claims without evidence, and release-stage promotion with open criteria.

## Ownership reconciliation

PRD section 36.1 did not assign AC-1, AC-6, AC-8, or AC-43. The manifest records explicit, reviewable ownership overrides for those four criteria; it does not silently omit them from delivery accounting.
