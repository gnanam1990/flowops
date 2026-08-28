#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
validator="${repo_root}/deploy/ascp/check-base-mainnet-deployment-candidate.sh"
record="${repo_root}/deployments/base-mainnet-ascp-deployment-candidate-v1.json"
tmp_dir="$(mktemp -d)"
trap 'rm -rf "${tmp_dir}"' EXIT

bash -n "${validator}"
"${validator}" >/dev/null

expect_rejected() {
  local name="$1"
  local mutation="$2"
  local candidate="${tmp_dir}/${name}.json"
  local checksum="${tmp_dir}/${name}.sha256"
  jq "${mutation}" "${record}" >"${candidate}"
  shasum -a 256 "${candidate}" | awk -v name="$(basename "${candidate}")" '{print $1 "  " name}' >"${checksum}"
  if FLOWOPS_ASCP_MAINNET_CANDIDATE_RECORD="${candidate}" \
    FLOWOPS_ASCP_MAINNET_CANDIDATE_CHECKSUM="${checksum}" \
    "${validator}" >/dev/null 2>&1; then
    printf 'ASCP mainnet candidate validator accepted unsafe mutation: %s\n' "${name}" >&2
    exit 1
  fi
}

expect_rehashed_constructor_rejected() {
  local candidate="${tmp_dir}/rehashed-constructor.json"
  local checksum="${tmp_dir}/rehashed-constructor.sha256"
  local bytecode encoded initcode arguments_hash initcode_hash
  bytecode="$(forge inspect contracts/src/ASCPSpendModule.sol:ASCPSpendModule bytecode)"
  encoded="$(cast abi-encode 'f(address,address,address,(uint256,uint256,uint256))' \
    0x13e9fa8d49ee3e3b456db71d111da9b78fabd518 \
    0x833589fcd6edb6e08f4c7c32d4f71b54bda02913 \
    0x1111111111111111111111111111111111111111 \
    '(1000000,10000000,10000000)')"
  initcode="0x${bytecode#0x}${encoded#0x}"
  arguments_hash="$(printf '%s' "${encoded}" | cast keccak)"
  initcode_hash="$(printf '%s' "${initcode}" | cast keccak)"
  jq --arg arguments_hash "${arguments_hash}" --arg initcode_hash "${initcode_hash}" '
    .contracts[3].constructorArguments[2] = "0x1111111111111111111111111111111111111111"
    | .contracts[3].constructorArgumentsKeccak = $arguments_hash
    | .contracts[3].initCodeKeccak = $initcode_hash
  ' "${record}" >"${candidate}"
  shasum -a 256 "${candidate}" | awk -v name="$(basename "${candidate}")" '{print $1 "  " name}' >"${checksum}"
  if FLOWOPS_ASCP_MAINNET_CANDIDATE_RECORD="${candidate}" \
    FLOWOPS_ASCP_MAINNET_CANDIDATE_CHECKSUM="${checksum}" \
    "${validator}" >/dev/null 2>&1; then
    printf 'ASCP mainnet candidate validator accepted a rehashed unsafe constructor\n' >&2
    exit 1
  fi
}

expect_rejected authorize-deployment '.structuralGates.deploymentAuthorized = true'
expect_rejected enable-broadcast '.structuralGates.broadcastEnabled = true'
expect_rejected invent-review '.structuralGates.externalReviewDigest = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"'
expect_rejected change-source '.sourceBaseline.commit = "0000000000000000000000000000000000000000"'
expect_rejected substitute-source-map '.sourceBaseline.contractSources[0].path = .sourceBaseline.contractSources[1].path | .sourceBaseline.contractSources[0].sha256 = .sourceBaseline.contractSources[1].sha256'
expect_rejected substitute-artifact '.contracts[3].artifact = "contracts/src/ServiceDirectory.sol:ServiceDirectory"'
expect_rejected substitute-deployer '.deployer.address = "0x1111111111111111111111111111111111111111"'
expect_rejected substitute-safe '.safe.address = "0x1111111111111111111111111111111111111111"'
expect_rejected nonce-drift '.deployer.expectedNonce = 2'
expect_rejected lower-threshold '.safe.threshold = 1'
expect_rejected substitute-authority '.authorities.spendAuthorizer = .authorities.registryAdmin'
expect_rejected substitute-predicted-address '.contracts[2].predictedAddress = "0x1111111111111111111111111111111111111111"'
expect_rejected substitute-initcode '.contracts[3].initCodeKeccak = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"'
expect_rejected enable-module '.requiredInitialState.moduleEnabled = true'
expect_rejected waive-review '.unresolved -= ["ascp-independent-contract-review"]'
expect_rejected claim-production-rpc '.readOnlyEvidence.productionRpcAdmissionComplete = true'
expect_rehashed_constructor_rejected

printf 'Base mainnet ASCP deployment candidate rejected every unsafe mutation\n'
