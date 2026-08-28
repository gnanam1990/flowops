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

expect_rejected authorize-deployment '.structuralGates.deploymentAuthorized = true'
expect_rejected enable-broadcast '.structuralGates.broadcastEnabled = true'
expect_rejected invent-review '.structuralGates.externalReviewDigest = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"'
expect_rejected change-source '.sourceBaseline.commit = "0000000000000000000000000000000000000000"'
expect_rejected substitute-source-map '.sourceBaseline.contractSources[0].path = .sourceBaseline.contractSources[1].path | .sourceBaseline.contractSources[0].sha256 = .sourceBaseline.contractSources[1].sha256'
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

printf 'Base mainnet ASCP deployment candidate rejected every unsafe mutation\n'
