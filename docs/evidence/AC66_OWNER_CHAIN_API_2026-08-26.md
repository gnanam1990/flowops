# AC-66 Owner chain-changing API evidence

Date: 2026-08-26
Scope: local executable evidence; no mainnet transaction was broadcast

## Result

FlowOps now has one executable inventory for all 26 reviewed chain-mutation
surfaces. Ten actions are enabled through `POST /v1/workflows` and its separate
approval route. Sixteen actions remain explicit fail-closed rows because they
do not yet have both a closed typed request/calldata schema and an independent
receipt lifecycle. Deployment configuration cannot turn a disabled row into
an Owner API action.

Every enabled row binds:

- one typed action to exact contract calldata and a server-derived payload;
- two different human principals, kind-specific roles, and fresh step-up;
- one immutable `ascp.governance.execute` command consumed as a Safe CALL;
- independent finalized receipt quorum and atomic receipt ownership; and
- append-only workflow, outbox, relay, and receipt audit records.

The exhaustive test walks all 26 rows. It proves that all ten enabled action
types are unique and installable, rejects a same-principal approval for each,
and proves that all 16 disabled rows cannot be installed as authority rules.
The observer coverage test also requires the number of independent receipt
rules to equal the number of enabled Owner actions.

The strict observer test exercises the runtime completion path: two providers
independently return the finalized receipt, historical target bytecode,
`safe()` principal, and outer transaction sender. The authority verifier
accepts the agreeing proof and rejects runtime-code, principal, and relayer
substitution. Strict authority configuration is also wired into the observer;
it cannot run as a proposal-only gate whose workflows can never finalize.
Startup also rejects either half of an incomplete strict configuration: the
governance observer tuple and authority rules are required together.

## Machine artifact

- Manifest: `docs/evidence/AC66_OWNER_CHAIN_API_INVENTORY_2026-08-26.json`
- SHA-256: `1f3f7efb6513555a509e6daf43a088aeac647efc998276ec38a1430b5156fd54`
- Schema version: `1`
- Enabled rows: `10`
- Disabled rows: `16`

## Reproduction

```sh
go test -race ./internal/ascpworkflow -run 'TestOwnerChainAction'
go test -race ./internal/ascpgovernanceobserver -run 'Test(ObserverDiscoversFinalizedReceiptWithoutCallerEvidence|ObserverRuleMapCoversEveryChainWorkflowKind)'
shasum -a 256 docs/evidence/AC66_OWNER_CHAIN_API_INVENTORY_2026-08-26.json
```

## Remaining release boundary

This evidence does not claim a production/mainnet Safe execution. Release
still requires the signed deployment authority manifest, reviewed runtime code
hashes and principals, real owner signatures, independent provider receipts,
and the release evidence bundle. Enabling any currently disabled row is a new
module: it must first add its typed preconditions/calldata and independent
receipt proof, then update this inventory and its locked manifest together.
