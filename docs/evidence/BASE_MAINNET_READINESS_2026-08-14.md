# Base Mainnet CallEscrow Readiness Evidence

Status: blocked; no deployment and no mainnet fund movement
Observed: 2026-08-14

## Scope

This record proves that the preparation package identifies the intended Base
mainnet network and Circle native USDC contract while remaining structurally
unable to broadcast. It is pre-deployment evidence only. It is not a contract
audit, legal approval, production-RPC selection, or authorization to deploy.

Primary references:

- Base deployment documentation: <https://docs.base.org/get-started/deploy-smart-contracts>
- Circle USDC contract registry: <https://developers.circle.com/stablecoins/usdc-contract-addresses>

## Read-only network observation

Two public endpoints independently returned:

- chain ID `8453`;
- non-empty runtime code at Circle native USDC on Base;
- token symbol `USDC` and 6 decimals; and
- runtime code hash
  `0xa6705a10bb756b5dea144591118be77d7af0c3eee3bf2dfe2583dcb0364fefab`.

The Base public endpoint observed head `49959378`; PublicNode observed head
`49959379`. Both returned canonical anchor block `49959378` with hash
`0x1b1d9662ac4844e5d7b0b767648cd9adef4cf326194fd042ed8cd623943c4596`.

The observations are recorded in
`deployments/base-mainnet-readiness.json`. Both endpoints are explicitly
`productionEligible: false`; hostname diversity does not prove operational
independence or a production SLA.

## Structural broadcast refusal

The committed deployment entrypoint contains:

```text
DESIGNATED_DEPLOYER = address(0)
EXTERNAL_REVIEW_DIGEST = bytes32(0)
MAINNET_BROADCAST_ENABLED = false
```

A read-only Foundry rehearsal against the Base public endpoint reached the
canonical USDC check and then reverted with
`MainnetDeployerNotDesignated()`. It produced no transaction hash and no
contract address. The rehearsal intentionally returned a non-zero process exit
because refusal is the expected result.

No `--broadcast`, signing account, private key, keystore password, token
approval, or asset transfer was supplied.

## Deterministic verification

`make test-mainnet-readiness` completed with:

- 8 passing Solidity tests;
- wrong-chain and missing-USDC refusal;
- proof that the committed package cannot broadcast;
- independent negative tests for missing deployer, missing review, and disabled
  broadcast;
- a test-only promoted harness proving the pinned constructor path; and
- mutation rejection for invented deployment data, premature approval,
  premature funding, RPC substitution, chain substitution, anchor regression,
  and runtime-code disagreement.

`make smoke-escrow-mainnet-readiness` passed against the two public endpoints.
It was read-only and checked chain identity, USDC metadata/runtime, bounded head
skew, and a shared anchor. The same target ran the promoted test harness on a
Base mainnet fork and deployed the pinned constructor only inside the
ephemeral fork. Any address printed by that test is synthetic fork state, not a
Base mainnet deployment address and must never enter the deployment registry.

The smoke rejects identical URLs, two URLs resolving to the same normalized
hostname, and malformed URLs before making a network request. Credential-bearing
RPC URLs are passed to `cast` through its environment rather than command
arguments; the fork rehearsal uses the documented public Base endpoint.

`make smoke-rpc-admission` passed without making a network request. It proved
that the production-shaped split configuration accepts two distinct paid
operator and failure-domain declarations while rejecting known public
endpoints, shared operators, shared failure domains, free tiers, and ineligible
services. Its output contained no provider hostname, path, or credential. This
is evidence that the admission mechanism works, not that any real production
provider has been selected or paid for.

`make test-mainnet-deployer-verification` also passed. The deterministic source
rehearsal pins the exact `CallEscrow` source and Foundry configuration hashes,
dependency gitlinks, Solidity `0.8.26` compiler metadata, optimizer runs, Cancun
EVM target, canonical Base USDC constructor encoding, creation-input hash, and
contract-scoped production-import standard JSON compiler input. Test-only and
unrelated-script sources are excluded so they cannot create false production
source drift. The test mutates each load-bearing
field and requires rejection. It does not contact Base or submit source to an
explorer.

`make test-security-review-package` passed. It binds the contract and mainnet
deployment-ceremony scope to source commit
`808caa4c9905334c52d6f237863f5ff33b11ffb0`, the exact source/configuration
hashes, dependency commits, compiler settings, ABI, storage layout, method
identifiers, source rehearsal, threat model, reviewer brief, test commands, and
seven explicit known limitations. Independent mutations of the scope, source,
commit, compiler, ABI, limitations, reviewer, report, and completion state were
rejected. Focused high/medium Solidity lint, 22 unit/fuzz tests, and the
invariant suite passed. This evidence proves that a reproducible review package
exists and remains fail-closed; it is not evidence that an external review has
occurred.

The hardware ceremony record remains deliberately unassigned. Negative tests
prove that inventing a deployer, selecting a software keystore, inventing an
external review, approving source prematurely, or approving broadcast
prematurely is rejected. The future wrapper has no raw-private-key,
software-keystore, command-line RPC-secret, or explorer-credential path; it
accepts only Ledger or Trezor after a reviewed clean commit and fresh bounded
approval. The post-deployment verifier is read-only, and the source-submission
wrapper refuses both missing and explicitly false approval.

## Unresolved promotion gates

- the hardware-wallet ceremony is implemented and fail-closed, but no
  production deployer, ownership attestation, or approved recovery runbook
  exists;
- the exact external-review package is prepared, but no independent reviewer,
  completed review, retest, or bound report digest exists;
- no completed legal review;
- no durable escrow event registration and reorg correction integration;
- no funded reference-signer Base Sepolia proof;
- no selected independent paid production RPC providers;
- no measured and approved deployment-confirmation depth;
- production RPC admission is implemented and smoke-tested, but no provider
  identity, paid plan, or failure-domain evidence has been approved;
- deterministic source verification is rehearsed, but no live deployment or
  approved explorer submission exists;
- the proposed capped-pilot profile is implemented by the control plane and
  both signer rails, but escrow pilot admission remains open until a funded
  reference-signer Sepolia proof; funding remains disabled;
- no explicit mainnet broadcast approval.

Until all gates are independently evidenced in a separate promotion PR, the
correct outcome of the mainnet script is refusal.
