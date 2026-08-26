#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
validator="${repo_root}/deploy/ascp/check-base-sepolia-activation-evidence.sh"
canonical_record="${repo_root}/deployments/base-sepolia-ascp-activation-v1.json"
tmp_dir="$(mktemp -d)"
trap 'rm -rf "${tmp_dir}"' EXIT

FLOWOPS_ASCP_SEPOLIA_ACTIVATION_RECORD="${canonical_record}" "${validator}" >/dev/null

expect_rejected() {
  local name="$1"
  local mutation="$2"
  local mutated_record="${tmp_dir}/${name}.json"

  jq "${mutation}" "${canonical_record}" >"${mutated_record}"
  if FLOWOPS_ASCP_SEPOLIA_ACTIVATION_RECORD="${mutated_record}" "${validator}" >/dev/null 2>&1; then
    printf 'validator accepted invalid activation evidence mutation: %s\n' "${name}" >&2
    exit 1
  fi
}

expect_rejected wrong-chain '.chainId = 8453'
expect_rejected wrong-deployment-release '.sourceDeployment.releaseId = "substituted"'
expect_rejected wrong-safe '.safe.address = .execution.outerFrom'
expect_rejected wrong-threshold '.safe.threshold = 1'
expect_rejected duplicate-confirmation '.safe.confirmedOwners[1] = .safe.confirmedOwners[0]'
expect_rejected non-owner-confirmation '.safe.confirmedOwners[1] = .execution.outerFrom'
expect_rejected wrong-safe-nonce '.safe.nonceAfter = 2'
expect_rejected value-transfer '.safeTransaction.value = "1"'
expect_rejected substituted-multisend-target '.safeTransaction.to = .contracts.escrow'
expect_rejected substituted-safe-data '.safeTransaction.data = (.safeTransaction.data[0:-1] + "1")'
expect_rejected wrong-action-order '.actions[0].order = 2'
expect_rejected wrong-allowlisted-escrow '.actions[0].escrow = .contracts.serviceDirectory'
expect_rejected wrong-allowlist-code-hash '.actions[0].escrowRuntimeCodeHash = .safe.safeTxHash'
expect_rejected substituted-workflow '.actions[0].workflowId = .actions[0].workflowPayloadHash'
expect_rejected wrong-enabled-module '.actions[1].module = .contracts.escrow'
expect_rejected failed-receipt '.execution.receiptStatus = false'
expect_rejected malformed-outer-input-hash '.execution.outerInputHash = "0x00"'
expect_rejected activation-before-deployment '.execution.blockNumber = 45974069'
expect_rejected module-disabled '.postState.spendModuleEnabled = false'
expect_rejected wrong-post-allowlist '.postState.escrowAllowlistCodeHash = .safe.safeTxHash'
expect_rejected directory-published '.postState.directoryVersion = 1'
expect_rejected escrow-paused '.postState.callEscrowEmergencyPaused = true'
expect_rejected module-paused '.postState.spendModuleEmergencyPaused = true'
expect_rejected safe-native-funded '.postState.safeNativeBalanceWei = "1"'
expect_rejected safe-funded '.postState.safeUsdcBalance = "1"'
expect_rejected contract-native-funded '.postState.allContractNativeBalancesWei = "1"'
expect_rejected contract-usdc-funded '.postState.allContractUsdcBalances = "1"'
expect_rejected allowance-created '.postState.safeToSpendModuleUsdcAllowance = "1"'
expect_rejected runtime-disabled '.postState.runtimeEnabled = false'
expect_rejected funding-enabled '.postState.fundingEnabled = true'
expect_rejected duplicate-rpc '.verification.rpcEvidence[1].url = .verification.rpcEvidence[0].url'
expect_rejected head-before-activation '.verification.rpcEvidence[0].observedHead = (.execution.blockNumber - 1)'
expect_rejected substituted-safe-service-evidence '.verification.safeTransactionServiceUrl = .verification.baseScanTransactionUrl'
expect_rejected mainnet-approved '.mainnetApproved = true'

printf 'ASCP Base Sepolia activation validator rejected all invalid mutations\n'
