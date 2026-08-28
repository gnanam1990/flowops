# ASCP Base mainnet external gates

Status: repository preparation complete; external evidence remains required.

## Safe owner-control proof

This ceremony proves control of two reviewed Safe owner addresses without
creating a Safe transaction or authorizing deployment, funding, approvals,
module activation, or asset movement. Generate a fresh one-hour challenge:

```sh
go run ./cmd/ascp-safe-owner-proof template \
  deployments/base-mainnet-safe-owner-control-profile-v1.json \
  owner-control-<reviewed-run-id> > /secure/safe-owner-proof.json

go run ./cmd/ascp-safe-owner-proof digest /secure/safe-owner-proof.json
```

Two distinct owners must sign the exact visible EIP-191 message. Append each
lowercase owner address and `0x`-prefixed 65-byte signature, then verify before
expiry:

```sh
go run ./cmd/ascp-safe-owner-proof verify \
  /secure/safe-owner-proof.json \
  deployments/base-mainnet-safe-owner-control-profile-v1.json
```

Never provide a private key, seed phrase, wallet session, or signature for a
different message to this command. A valid proof completes owner control only;
it is not broadcast approval.

## Independent contract review

Give the reviewer the exact source commit and candidate SHA-256 recorded in the
owner-control profile. Review scope must include all four ASCP contracts,
`ASCPTypeHashes`, the Base mainnet deployment script, constructor bindings,
Safe governance boundaries, state machines, accounting, pause/recovery,
signature replay, and the tests. The reviewer must return an immutable report
digest and explicitly identify unresolved findings. A FlowOps-authored review
is first-party evidence and must not populate `EXTERNAL_REVIEW_DIGEST`.
The bounded first-party findings and independent-review checklist are in
`security/ascp-mainnet/FIRST_PARTY_REVIEW_HANDOFF.md`.

## Production RPC admission

Select two to five paid providers with distinct operators and failure domains.
Keep credential-bearing URLs only in `FLOWOPS_BASE_RPC_PROVIDERS_JSON`; record
only non-secret reviewed metadata in `FLOWOPS_BASE_RPC_ADMISSION_JSON`. Validate:

```sh
FLOWOPS_BASE_CHAIN_ID=8453 \
FLOWOPS_BASE_RPC_PROVIDERS_JSON='[...]' \
FLOWOPS_BASE_RPC_ADMISSION_JSON='{"schemaVersion":1,"providers":[...]}' \
  go run ./cmd/rpc-admission-check
```

Start from `deployments/base-mainnet-rpc-admission.template.json`. Its placeholder
rows deliberately say `unreviewed` and `false`, so production validation fails
until real paid-provider contracts and failure domains have been reviewed.

Public endpoints used for read-only preparation are not production admissions.

## Promotion and signed runtime release

Only after owner control, independent review and production RPC admission are
complete may a separate promotion change replace the deployment script's zero
constants. That review must bind the external-review digest, exact candidate,
Safe state, deployer nonce and a release-plan digest while retaining zero-fund
scope. Run the pinned fork and full repository checks again.

The runtime release manifest remains post-deployment evidence: contract
addresses, deployment receipts, source verification, immutable image and
executable digests do not exist before deployment. Fill and sign the schema-v2
template only after those facts exist. The offline Ed25519 release key must
never enter the repository or production runtime.

## Final approval boundary

Fresh zero-fund broadcast approval must name the reviewed promotion commit and
candidate digest. It authorizes only deployment of the four unfunded contracts.
Safe module enablement, escrow allowlisting, directory publication, verifier
activation, token approval, funding and payments are separate later ceremonies.
