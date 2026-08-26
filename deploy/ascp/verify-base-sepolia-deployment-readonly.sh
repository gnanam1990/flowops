#!/usr/bin/env bash
set -euo pipefail

# Re-observes the committed ASCP v4 deployment through two independent public
# RPC providers. This script is read-only: it never signs, sends, retries, or
# changes contract state.
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
record="${FLOWOPS_ASCP_SEPOLIA_EVIDENCE_RECORD:-${repo_root}/deployments/base-sepolia-ascp-v4.json}"
primary_rpc="${BASE_SEPOLIA_RPC_URL_PRIMARY:-https://sepolia.base.org}"
secondary_rpc="${BASE_SEPOLIA_RPC_URL_SECONDARY:-https://base-sepolia-rpc.publicnode.com}"

cd "${repo_root}"
FLOWOPS_ASCP_SEPOLIA_EVIDENCE_RECORD="${record}" deploy/ascp/check-base-sepolia-deployment-evidence.sh >/dev/null

rpc_host() {
  sed -E 's#^[a-zA-Z]+://([^/:]+).*#\1#' <<<"$1" | tr '[:upper:]' '[:lower:]'
}

test "$(rpc_host "${primary_rpc}")" != "$(rpc_host "${secondary_rpc}")"

deployer="$(jq -er '.deployer.address' "${record}")"
safe="$(jq -er '.safe.address' "${record}")"
asset="$(jq -er '.asset.address' "${record}")"
asset_code_hash="$(jq -er '.asset.runtimeCodeHash' "${record}")"
directory="$(jq -er '.contracts[] | select(.name == "service_directory") | .address' "${record}")"
registry="$(jq -er '.contracts[] | select(.name == "agent_registry") | .address' "${record}")"
escrow="$(jq -er '.contracts[] | select(.name == "ascp_call_escrow") | .address' "${record}")"
module="$(jq -er '.contracts[] | select(.name == "ascp_spend_module") | .address' "${record}")"
snapshot_block="$(jq -er '[.contracts[].deploymentBlock] | max' "${record}")"
zero_digest='0x0000000000000000000000000000000000000000000000000000000000000000'

call_address() {
  local rpc_url="$1"
  local target="$2"
  local signature="$3"
  shift 3
  cast call --rpc-url "${rpc_url}" "${target}" "${signature}" "$@" | tr '[:upper:]' '[:lower:]'
}

call_snapshot() {
  local rpc_url="$1"
  local target="$2"
  local signature="$3"
  shift 3
  cast call --rpc-url "${rpc_url}" --block "${snapshot_block}" "${target}" "${signature}" "$@" \
    | tr '[:upper:]' '[:lower:]'
}

observe_provider() {
  local rpc_url="$1"
  local head asset_code asset_hash owners expected_owners threshold caps
  local name transaction expected_address expected_nonce expected_block expected_input_hash
  local expected_runtime_hash expected_runtime_bytes expected_gas expected_l2_fee expected_l1_fee expected_total_fee
  local tx_json receipt_json observed_from observed_to observed_nonce observed_value observed_input_hash
  local observed_address observed_tx observed_block observed_block_hash canonical_block_hash observed_status
  local observed_gas gas_price observed_l2_fee observed_l1_fee observed_total_fee code observed_runtime_hash observed_runtime_bytes

  test "$(cast chain-id --rpc-url "${rpc_url}")" = '84532'
  head="$(cast block-number --rpc-url "${rpc_url}")"

  while IFS= read -r contract; do
    name="$(jq -er '.name' <<<"${contract}")"
    transaction="$(jq -er '.deploymentTx' <<<"${contract}")"
    expected_address="$(jq -er '.address' <<<"${contract}")"
    expected_nonce="$(jq -er '.creationNonce' <<<"${contract}")"
    expected_block="$(jq -er '.deploymentBlock' <<<"${contract}")"
    expected_input_hash="$(jq -er '.creationInputHash' <<<"${contract}")"
    expected_runtime_hash="$(jq -er '.runtimeCodeHash' <<<"${contract}")"
    expected_runtime_bytes="$(jq -er '.runtimeCodeBytes' <<<"${contract}")"
    expected_gas="$(jq -er '.gasUsed' <<<"${contract}")"
    expected_l2_fee="$(jq -er '.l2ExecutionFeeWei' <<<"${contract}")"
    expected_l1_fee="$(jq -er '.l1DataFeeWei' <<<"${contract}")"
    expected_total_fee="$(jq -er '.actualTotalFeeWei' <<<"${contract}")"

    tx_json="$(cast tx --rpc-url "${rpc_url}" "${transaction}" --json)"
    receipt_json="$(cast receipt --rpc-url "${rpc_url}" "${transaction}" --json)"
    observed_from="$(jq -er '.from | ascii_downcase' <<<"${tx_json}")"
    observed_to="$(jq -r '.to' <<<"${tx_json}")"
    observed_nonce="$(cast to-dec "$(jq -er '.nonce' <<<"${tx_json}")")"
    observed_value="$(cast to-dec "$(jq -er '.value' <<<"${tx_json}")")"
    observed_input_hash="$(jq -er '.input' <<<"${tx_json}" | cast keccak)"
    observed_address="$(jq -er '.contractAddress | ascii_downcase' <<<"${receipt_json}")"
    observed_tx="$(jq -er '.transactionHash | ascii_downcase' <<<"${receipt_json}")"
    observed_block="$(cast to-dec "$(jq -er '.blockNumber' <<<"${receipt_json}")")"
    observed_block_hash="$(jq -er '.blockHash | ascii_downcase' <<<"${receipt_json}")"
    observed_status="$(jq -er '.status' <<<"${receipt_json}")"
    observed_gas="$(cast to-dec "$(jq -er '.gasUsed' <<<"${receipt_json}")")"
    gas_price="$(cast to-dec "$(jq -er '.effectiveGasPrice' <<<"${receipt_json}")")"
    observed_l1_fee="$(cast to-dec "$(jq -er '.l1Fee' <<<"${receipt_json}")")"
    observed_l2_fee="$((observed_gas * gas_price))"
    observed_total_fee="$((observed_l2_fee + observed_l1_fee))"

    test "${observed_from}" = "${deployer}"
    test "${observed_to}" = 'null'
    test "${observed_nonce}" = "${expected_nonce}"
    test "${observed_value}" = '0'
    test "${observed_input_hash}" = "${expected_input_hash}"
    test "${observed_address}" = "${expected_address}"
    test "${observed_tx}" = "${transaction}"
    test "${observed_block}" = "${expected_block}"
    test "${observed_status}" = '0x1'
    test "${observed_gas}" = "${expected_gas}"
    test "${observed_l2_fee}" = "${expected_l2_fee}"
    test "${observed_l1_fee}" = "${expected_l1_fee}"
    test "${observed_total_fee}" = "${expected_total_fee}"
    test "${head}" -ge "${expected_block}"

    canonical_block_hash="$(cast block --rpc-url "${rpc_url}" "${expected_block}" --json | jq -er '.hash | ascii_downcase')"
    test "${canonical_block_hash}" = "${observed_block_hash}"
    code="$(cast code --rpc-url "${rpc_url}" "${expected_address}")"
    test "${code}" != '0x'
    observed_runtime_hash="$(printf '%s' "${code}" | cast keccak)"
    observed_runtime_bytes="$(((${#code} - 2) / 2))"
    test "${observed_runtime_hash}" = "${expected_runtime_hash}"
    test "${observed_runtime_bytes}" = "${expected_runtime_bytes}"
    printf '%s:%s:%s:%s\n' "${name}" "${observed_tx}" "${observed_block_hash}" "${observed_runtime_hash}"
  done < <(jq -c '.contracts[]' "${record}")

  asset_code="$(cast code --rpc-url "${rpc_url}" "${asset}")"
  test "${asset_code}" != '0x'
  asset_hash="$(printf '%s' "${asset_code}" | cast keccak)"
  test "${asset_hash}" = "${asset_code_hash}"

  expected_owners="$(jq -r '.safe.owners | sort | join(",")' "${record}")"
  owners="$(cast call --rpc-url "${rpc_url}" --block "${snapshot_block}" "${safe}" 'getOwners()(address[])' --json | jq -er '.[0] | map(ascii_downcase) | sort | join(",")')"
  threshold="$(call_snapshot "${rpc_url}" "${safe}" 'getThreshold()(uint256)')"
  test "${owners}" = "${expected_owners}"
  test "${threshold}" = "$(jq -er '.safe.threshold' "${record}")"
  test "$(call_snapshot "${rpc_url}" "${safe}" 'isModuleEnabled(address)(bool)' "${module}")" = 'false'

  test "$(call_snapshot "${rpc_url}" "${directory}" 'governor()(address)')" = "${safe}"
  test "$(call_snapshot "${rpc_url}" "${directory}" 'orgDomain()(bytes32)')" = "$(jq -er '.organizationDomain' "${record}")"
  test "$(call_snapshot "${rpc_url}" "${directory}" 'directoryPublisher()(address)')" = "$(jq -er '.authorities.directoryPublisher' "${record}")"
  test "$(call_snapshot "${rpc_url}" "${directory}" 'pauser()(address)')" = "$(jq -er '.authorities.directoryPauser' "${record}")"
  test "$(call_snapshot "${rpc_url}" "${directory}" 'currentVersion()(uint64)')" = '0'
  test "$(call_snapshot "${rpc_url}" "${directory}" 'currentRoot()(bytes32)')" = "${zero_digest}"

  test "$(call_snapshot "${rpc_url}" "${registry}" 'governor()(address)')" = "${safe}"
  test "$(call_snapshot "${rpc_url}" "${registry}" 'orgDomain()(bytes32)')" = "$(jq -er '.organizationDomain' "${record}")"
  test "$(call_snapshot "${rpc_url}" "${registry}" 'registryAdmin()(address)')" = "$(jq -er '.authorities.registryAdmin' "${record}")"
  test "$(call_snapshot "${rpc_url}" "${registry}" 'agentCount()(uint256)')" = '0'

  test "$(call_snapshot "${rpc_url}" "${escrow}" 'usdc()(address)')" = "${asset}"
  test "$(call_snapshot "${rpc_url}" "${escrow}" 'serviceDirectory()(address)')" = "${directory}"
  test "$(call_snapshot "${rpc_url}" "${escrow}" 'safe()(address)')" = "${safe}"
  test "$(call_snapshot "${rpc_url}" "${escrow}" 'governor()(address)')" = "${safe}"
  test "$(call_snapshot "${rpc_url}" "${escrow}" 'totalLocked()(uint256)')" = '0'
  test "$(call_snapshot "${rpc_url}" "${escrow}" 'emergencyPaused()(bool)')" = 'false'

  test "$(call_snapshot "${rpc_url}" "${module}" 'safe()(address)')" = "${safe}"
  test "$(call_snapshot "${rpc_url}" "${module}" 'token()(address)')" = "${asset}"
  test "$(call_snapshot "${rpc_url}" "${module}" 'spendAuthorizer()(address)')" = "$(jq -er '.authorities.spendAuthorizer' "${record}")"
  test "$(call_snapshot "${rpc_url}" "${module}" 'executedPrincipal()(uint256)')" = '0'
  test "$(call_snapshot "${rpc_url}" "${module}" 'emergencyPaused()(bool)')" = 'false'
  test "$(call_snapshot "${rpc_url}" "${module}" 'escrowAllowlist(address)(bytes32)' "${escrow}")" = "${zero_digest}"
  caps="$(cast call --rpc-url "${rpc_url}" --block "${snapshot_block}" "${module}" 'caps()(uint256,uint256,uint256)' --json | jq -c '.')"
  test "${caps}" = '[1000000,10000000,10000000]'

  while IFS= read -r address; do
    test "$(cast balance --rpc-url "${rpc_url}" --block "${snapshot_block}" "${address}")" = '0'
    test "$(call_snapshot "${rpc_url}" "${asset}" 'balanceOf(address)(uint256)' "${address}")" = '0'
  done < <(jq -r '.contracts[].address' "${record}")
  test "$(call_snapshot "${rpc_url}" "${asset}" 'allowance(address,address)(uint256)' "${safe}" "${module}")" = '0'
  test "$(call_snapshot "${rpc_url}" "${asset}" 'allowance(address,address)(uint256)' "${safe}" "${escrow}")" = '0'
}

primary_observation="$(mktemp -t flowops-ascp-primary.XXXXXX)"
secondary_observation="$(mktemp -t flowops-ascp-secondary.XXXXXX)"
trap 'rm -f "${primary_observation}" "${secondary_observation}"' EXIT
observe_provider "${primary_rpc}" >"${primary_observation}"
observe_provider "${secondary_rpc}" >"${secondary_observation}"
cmp -s "${primary_observation}" "${secondary_observation}"

while IFS= read -r source_url; do
  curl --fail --silent --show-error "${source_url}" \
    | jq -e '.match == "exact_match" and .creationMatch == "exact_match" and .runtimeMatch == "exact_match"' >/dev/null
done < <(jq -r '.contracts[].sourceVerification.sourcifyUrl' "${record}")

printf 'ASCP v4 Base Sepolia deployment and write-inert post-deployment snapshot at block %s verified read-only through two RPC providers\n' "${snapshot_block}"
