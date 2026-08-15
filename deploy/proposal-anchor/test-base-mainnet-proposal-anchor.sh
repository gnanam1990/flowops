#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
validator="${repo_root}/deploy/proposal-anchor/check-base-mainnet-proposal-anchor.sh"
canonical="${repo_root}/deployments/base-mainnet-proposal-anchor.json"
tmp_dir="$(mktemp -d)"
trap 'rm -rf "${tmp_dir}"' EXIT

expect_rejected() {
  local name="$1"
  local mutation="$2"
  local candidate="${tmp_dir}/${name}.json"
  jq "${mutation}" "${canonical}" >"${candidate}"
  if FLOWOPS_PROPOSAL_ANCHOR_RECORD="${candidate}" "${validator}" >/dev/null 2>&1; then
    printf 'proposal anchor validator accepted unsafe mutation: %s\n' "${name}" >&2
    exit 1
  fi
}

"${validator}" >/dev/null
expect_rejected blocked-status-restored '.status = "blocked-no-deployment"'
expect_rejected deployed-status '.status = "experimental-deployed"'
expect_rejected proposal-digest-drift '.proposalDigest = ("0x" + ("1" * 64))'
expect_rejected source-commit-drift '.sourceCommit = ("1" * 40)'
expect_rejected designated-deployer-drift '.designatedDeployer = "0x1111111111111111111111111111111111111111"'
expect_rejected contract-address '.contractAddress = "0x1111111111111111111111111111111111111111"'
expect_rejected transaction-hash '.transactionHash = ("0x" + ("1" * 64))'
expect_rejected source-verified '.sourceVerified = true'
expect_rejected broadcast-authorized '.broadcastAuthorized = true'
expect_rejected production-ready '.productionReady = true'
expect_rejected funding-enabled '.fundingEnabled = true'
expect_rejected vault-creation-enabled '.vaultCreationEnabled = true'
expect_rejected audit-completed '.externalAuditCompleted = true'
expect_rejected warning-removed '.warnings = []'
expect_rejected candidate-substituted '.candidateDeployer.address = "0x1111111111111111111111111111111111111111"'
expect_rejected candidate-made-production '.candidateDeployer.productionUseProhibited = false'
expect_rejected candidate-not-eoa '.candidateDeployer.observedCode = "0x01"'
expect_rejected candidate-latest-nonce '.candidateDeployer.observedLatestNonce = 1'
expect_rejected candidate-pending-nonce '.candidateDeployer.observedPendingNonce = 1'
expect_rejected candidate-address-drift '.candidateDeployer.expectedCreateAddressAtObservedNonce = "0x1111111111111111111111111111111111111111"'
expect_rejected candidate-observer-collapsed '.candidateDeployer.observers = ["mainnet.base.org", "mainnet.base.org"]'
expect_rejected package-approval-removed 'del(.promotionPackageApproval)'
expect_rejected package-approval-scope-broadened '.promotionPackageApproval.scope = "broadcast"'
expect_rejected expected-nonce-drift '.ceremony.expectedDeployerNonce = 1'
expect_rejected expected-contract-drift '.ceremony.expectedContractAddress = "0x1111111111111111111111111111111111111111"'
expect_rejected initcode-hash-drift '.ceremony.initCodeHash = ("0x" + ("1" * 64))'
expect_rejected runtime-hash-drift '.ceremony.expectedRuntimeCodeHash = ("0x" + ("1" * 64))'
expect_rejected gas-limit-raised '.ceremony.maxGasLimit = 250001'
expect_rejected max-fee-raised '.ceremony.maxFeePerGasWei = "20000001"'
expect_rejected max-spend-raised '.ceremony.maxGasSpendWei = "5000000000001"'

printf 'proposal anchor package validator rejected all unsafe mutations; broadcast remains blocked\n'
