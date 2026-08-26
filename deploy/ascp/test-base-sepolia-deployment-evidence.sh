#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
validator="${repo_root}/deploy/ascp/check-base-sepolia-deployment-evidence.sh"
canonical_record="${repo_root}/deployments/base-sepolia-ascp-v4.json"
tmp_dir="$(mktemp -d)"
trap 'rm -rf "${tmp_dir}"' EXIT

FLOWOPS_ASCP_SEPOLIA_EVIDENCE_RECORD="${canonical_record}" "${validator}" >/dev/null

expect_rejected() {
  local name="$1"
  local mutation="$2"
  local mutated_record="${tmp_dir}/${name}.json"

  jq "${mutation}" "${canonical_record}" >"${mutated_record}"
  if FLOWOPS_ASCP_SEPOLIA_EVIDENCE_RECORD="${mutated_record}" "${validator}" >/dev/null 2>&1; then
    printf 'validator accepted invalid ASCP evidence mutation: %s\n' "${name}" >&2
    exit 1
  fi
}

expect_rejected wrong-chain '.chainId = 8453'
expect_rejected mainnet-approved '.mainnetApproved = true'
expect_rejected runtime-enabled '.runtimeEnabled = true'
expect_rejected funding-enabled '.fundingEnabled = true'
expect_rejected module-enabled '.safe.spendModuleEnabled = true'
expect_rejected duplicate-contract-address '.contracts[1].address = .contracts[0].address'
expect_rejected constructor-safe '.contracts[0].constructorArguments.governor = .deployer.address'
expect_rejected source-not-exact '.contracts[2].sourceVerification.status = "partial_match"'
expect_rejected runtime-code-hash '.contracts[3].runtimeCodeHash = "0x00"'
expect_rejected duplicate-nonce '.contracts[3].creationNonce = .contracts[2].creationNonce'
expect_rejected swapped-nonces '(.contracts[0].creationNonce = 7) | (.contracts[1].creationNonce = 6)'
expect_rejected reordered-blocks '(.contracts[0].deploymentBlock = 45974262)'
expect_rejected contract-fee-mismatch '.contracts[0].actualTotalFeeWei = "18454698238464"'
expect_rejected aggregate-gas-mismatch '.fees.actualGasUsed = 11023834'
expect_rejected fee-over-ceiling '(.fees.l2ExecutionFeeWei = "1000000000000001") | (.fees.l1DataFeeWei = "0") | (.fees.actualTotalFeeWei = "1000000000000001")'
expect_rejected gas-over-ceiling '.fees.actualGasUsed = 18000001'
expect_rejected non-inert-directory '.initialState.directoryVersion = 1'
expect_rejected duplicate-rpc '.verification.rpcEvidence[1].url = .verification.rpcEvidence[0].url'
expect_rejected head-before-deployment '(.contracts | map(.deploymentBlock) | max) as $last | .verification.rpcEvidence[0].observedHead = ($last - 1)'
expect_rejected unknown-source-commit '.sourceCommit = "0000000000000000000000000000000000000000"'

printf 'ASCP Base Sepolia evidence validator rejected all invalid mutations\n'
