#!/usr/bin/env bash
set -euo pipefail

# Re-observes the production Safe through two independent Base mainnet RPCs.
# This script is read-only: it never signs, sends, retries, or changes state.
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
record="${FLOWOPS_ASCP_MAINNET_PROMOTION_RECORD:-${repo_root}/deployments/base-mainnet-ascp-promotion.json}"
primary_rpc="${BASE_MAINNET_RPC_URL_PRIMARY:-https://mainnet.base.org}"
secondary_rpc="${BASE_MAINNET_RPC_URL_SECONDARY:-https://base-mainnet.public.blastapi.io}"

cd "${repo_root}"
FLOWOPS_ASCP_MAINNET_PROMOTION_RECORD="${record}" deploy/ascp/check-base-mainnet-promotion.sh >/dev/null

rpc_host() {
  sed -E 's#^[a-zA-Z]+://([^/:]+).*#\1#' <<<"$1" | tr '[:upper:]' '[:lower:]'
}

test "$(rpc_host "${primary_rpc}")" != "$(rpc_host "${secondary_rpc}")"

safe="$(jq -er '.safe.address' "${record}")"
asset="$(jq -er '.asset.address' "${record}")"
transaction="$(jq -er '.safe.deploymentTransaction' "${record}")"
deployment_block="$(jq -er '.safe.deploymentBlock' "${record}")"
deployment_block_hash="$(jq -er '.safe.deploymentBlockHash' "${record}")"
runtime_hash="$(jq -er '.safe.runtimeCodeHash' "${record}")"
expected_version="$(jq -er '.safe.version' "${record}")"
expected_threshold="$(jq -er '.safe.threshold' "${record}")"
expected_owners="$(jq -c '.safe.owners' "${record}")"

observe_provider() {
  local rpc_url="$1"
  local receipt canonical_hash code observed_hash finalized_hex finalized_block
  local version owners threshold nonce modules native_balance usdc_balance

  test "$(cast chain-id --rpc-url "${rpc_url}")" = '8453'

  receipt="$(cast receipt --rpc-url "${rpc_url}" "${transaction}" --json)"
  test "$(jq -er '.status' <<<"${receipt}")" = '0x1'
  test "$(cast to-dec "$(jq -er '.blockNumber' <<<"${receipt}")")" = "${deployment_block}"
  test "$(jq -er '.blockHash | ascii_downcase' <<<"${receipt}")" = "${deployment_block_hash}"

  canonical_hash="$(cast block --rpc-url "${rpc_url}" "${deployment_block}" --json | jq -er '.hash | ascii_downcase')"
  test "${canonical_hash}" = "${deployment_block_hash}"
  finalized_hex="$(cast block --rpc-url "${rpc_url}" finalized --json | jq -er '.number')"
  finalized_block="$(cast to-dec "${finalized_hex}")"
  if test "${finalized_block}" -lt "${deployment_block}"; then
    printf 'Safe deployment block %s is not finalized by %s (finalized block %s)\n' \
      "${deployment_block}" "$(rpc_host "${rpc_url}")" "${finalized_block}" >&2
    return 1
  fi

  code="$(cast code --rpc-url "${rpc_url}" "${safe}")"
  test "${code}" != '0x'
  observed_hash="$(printf '%s' "${code}" | cast keccak)"
  test "${observed_hash}" = "${runtime_hash}"

  version="$(cast call --rpc-url "${rpc_url}" "${safe}" 'VERSION()(string)' --json | jq -er '.[0]')"
  owners="$(cast call --rpc-url "${rpc_url}" "${safe}" 'getOwners()(address[])' --json | jq -c '.[0] | map(ascii_downcase)')"
  threshold="$(cast call --rpc-url "${rpc_url}" "${safe}" 'getThreshold()(uint256)' --json | jq -er '.[0]')"
  nonce="$(cast call --rpc-url "${rpc_url}" "${safe}" 'nonce()(uint256)' --json | jq -er '.[0]')"
  modules="$(cast call --rpc-url "${rpc_url}" "${safe}" 'getModulesPaginated(address,uint256)(address[],address)' 0x0000000000000000000000000000000000000001 10 --json | jq -c '.')"
  native_balance="$(cast balance --rpc-url "${rpc_url}" "${safe}")"
  usdc_balance="$(cast call --rpc-url "${rpc_url}" "${asset}" 'balanceOf(address)(uint256)' "${safe}" --json | jq -er '.[0]')"

  test "${version}" = "${expected_version}"
  test "${owners}" = "${expected_owners}"
  test "${threshold}" = "${expected_threshold}"
  test "${nonce}" = '0'
  test "${modules}" = '[[],"0x0000000000000000000000000000000000000001"]'
  test "${native_balance}" = '0'
  test "${usdc_balance}" = '0'

  printf '%s:%s:%s:%s:%s:%s:%s\n' \
    "${transaction}" "${deployment_block_hash}" "${runtime_hash}" "${version}" \
    "${owners}" "${threshold}" "${nonce}"
}

primary_observation="$(mktemp -t flowops-mainnet-safe-primary.XXXXXX)"
secondary_observation="$(mktemp -t flowops-mainnet-safe-secondary.XXXXXX)"
trap 'rm -f "${primary_observation}" "${secondary_observation}"' EXIT
observe_provider "${primary_rpc}" >"${primary_observation}"
observe_provider "${secondary_rpc}" >"${secondary_observation}"
cmp -s "${primary_observation}" "${secondary_observation}"

printf 'Base mainnet Safe %s is finalized and matches the committed zero-fund 2-of-3 state through two RPC providers\n' "${safe}"
