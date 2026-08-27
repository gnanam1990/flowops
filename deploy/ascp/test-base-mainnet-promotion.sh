#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
validator="${repo_root}/deploy/ascp/check-base-mainnet-promotion.sh"
record="${repo_root}/deployments/base-mainnet-ascp-promotion.json"
tmp_dir="$(mktemp -d)"
trap 'rm -rf "${tmp_dir}"' EXIT

bash -n "${validator}"
"${validator}" >/dev/null

expect_rejected() {
  local name="$1"
  local mutation="$2"
  local candidate="${tmp_dir}/${name}.json"
  jq "${mutation}" "${record}" >"${candidate}"
  if FLOWOPS_ASCP_MAINNET_PROMOTION_RECORD="${candidate}" "${validator}" >/dev/null 2>&1; then
    printf 'ASCP mainnet promotion validator accepted unsafe mutation: %s\n' "${name}" >&2
    exit 1
  fi
}

expect_rejected authorize-deployment '.deploymentAuthorized = true'
expect_rejected authorize-funding '.fundingAuthorized = true'
expect_rejected invent-safe '.safe.address = "0x1111111111111111111111111111111111111111"'
expect_rejected lower-threshold '.safe.threshold = 1'
expect_rejected duplicate-owner '.safe.owners[2] = .safe.owners[0]'
expect_rejected nonce-drift '.deployer.expectedNonce = 2'
expect_rejected predicted-address-substitution '.predictedContracts[1].address = "0x1111111111111111111111111111111111111111"'
expect_rejected enable-module '.activation.moduleEnabled = true'
expect_rejected enable-funding '.pilot.fundingEnabled = true'
expect_rejected waive-review '.unresolved -= ["ascp-independent-contract-review"]'

printf 'Base mainnet ASCP promotion draft rejected every unsafe mutation\n'
