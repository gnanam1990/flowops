#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
validator="${repo_root}/deploy/ascp/check-base-mainnet-activation-evidence.sh"
canonical_record="${repo_root}/deployments/base-mainnet-ascp-activation-v1.json"
tmp_dir="$(mktemp -d)"
trap 'rm -rf "${tmp_dir}"' EXIT

FLOWOPS_ASCP_MAINNET_ACTIVATION_RECORD="${canonical_record}" "${validator}" >/dev/null

expect_rejected() {
  local name="$1" mutation="$2" mutated_record="${tmp_dir}/${1}.json"
  jq "${mutation}" "${canonical_record}" >"${mutated_record}"
  if FLOWOPS_ASCP_MAINNET_ACTIVATION_RECORD="${mutated_record}" "${validator}" >/dev/null 2>&1; then
    printf 'validator accepted invalid activation evidence mutation: %s\n' "${name}" >&2
    exit 1
  fi
}

expect_rejected wrong-chain '.chainId = 84532'
expect_rejected wrong-safe '.safe.address = .execution.outerFrom'
expect_rejected duplicate-confirmation '.safe.confirmedOwners[1] = .safe.confirmedOwners[0]'
expect_rejected unapproved-activation '.authorization.moduleActivationAuthorized = false'
expect_rejected funding-authorized '.authorization.fundingAuthorized = true'
expect_rejected value-transfer '.safeTransaction.value = "1"'
expect_rejected substituted-safe-data '.safeTransaction.data = (.safeTransaction.data[0:-1] + "1")'
expect_rejected wrong-allowlisted-escrow '.actions[0].escrow = .contracts.serviceDirectory'
expect_rejected wrong-code-hash '.actions[0].escrowRuntimeCodeHash = .safe.safeTxHash'
expect_rejected wrong-enabled-module '.actions[1].module = .contracts.escrow'
expect_rejected failed-receipt '.execution.receiptStatus = false'
expect_rejected module-disabled '.postState.spendModuleEnabled = false'
expect_rejected directory-published '.postState.directoryVersion = 1'
expect_rejected safe-funded '.postState.safeUsdcBalance = "1"'
expect_rejected allowance-created '.postState.safeToSpendModuleUsdcAllowance = "1"'
expect_rejected runtime-disabled '.postState.runtimeEnabled = false'
expect_rejected duplicate-rpc '.verification.rpcEvidence[1].url = .verification.rpcEvidence[0].url'

printf 'Base mainnet ASCP activation validator rejected all invalid mutations\n'
