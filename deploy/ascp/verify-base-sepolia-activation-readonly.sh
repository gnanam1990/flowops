#!/usr/bin/env bash
set -euo pipefail

# Re-observes the executed activation through two independent public RPC
# providers. This script is read-only: it never signs, sends, retries, or
# changes contract state.
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
record="${FLOWOPS_ASCP_SEPOLIA_ACTIVATION_RECORD:-${repo_root}/deployments/base-sepolia-ascp-activation-v1.json}"
deployment_record="${repo_root}/deployments/base-sepolia-ascp-v4.json"
primary_rpc="${BASE_SEPOLIA_RPC_URL_PRIMARY:-https://sepolia.base.org}"
secondary_rpc="${BASE_SEPOLIA_RPC_URL_SECONDARY:-https://base-sepolia-rpc.publicnode.com}"

cd "${repo_root}"
FLOWOPS_ASCP_SEPOLIA_ACTIVATION_RECORD="${record}" deploy/ascp/check-base-sepolia-activation-evidence.sh >/dev/null

rpc_host() {
  sed -E 's#^[a-zA-Z]+://([^/:]+).*#\1#' <<<"$1" | tr '[:upper:]' '[:lower:]'
}

test "$(rpc_host "${primary_rpc}")" != "$(rpc_host "${secondary_rpc}")"

safe="$(jq -er '.safe.address' "${record}")"
module="$(jq -er '.contracts.spendModule' "${record}")"
escrow="$(jq -er '.contracts.escrow' "${record}")"
directory="$(jq -er '.contracts.serviceDirectory' "${record}")"
registry="$(jq -er '.contracts.agentRegistry' "${record}")"
asset="$(jq -er '.contracts.asset' "${record}")"
transaction="$(jq -er '.execution.transactionHash' "${record}")"
safe_tx_hash="$(jq -er '.safe.safeTxHash' "${record}")"
safe_service_url="$(jq -er '.verification.safeTransactionServiceUrl' "${record}")"
safe_tx_to="$(jq -er '.safeTransaction.to' "${record}")"
safe_tx_data="$(jq -er '.safeTransaction.data' "${record}")"
expected_code_hash="$(jq -er '.actions[0].escrowRuntimeCodeHash' "${record}")"
workflow_id="$(jq -er '.actions[0].workflowId' "${record}")"
workflow_payload_hash="$(jq -er '.actions[0].workflowPayloadHash' "${record}")"
execution_success_topic="$(jq -er '.eventEvidence.executionSuccessTopic' "${record}")"
enabled_module_topic="$(jq -er '.eventEvidence.enabledModuleTopic' "${record}")"
allowlist_topic="$(jq -er '.eventEvidence.escrowAllowlistSetTopic' "${record}")"
workflow_topic="$(jq -er '.eventEvidence.governanceWorkflowBoundTopic' "${record}")"
zero_digest='0x0000000000000000000000000000000000000000000000000000000000000000'

topic_address() {
  printf '0x%064s' "${1#0x}" | tr ' ' '0'
}

topic_selector() {
  printf '0x%-64s' "${1#0x}" | tr ' ' '0'
}

call_address() {
  local rpc_url="$1"
  local target="$2"
  local signature="$3"
  shift 3
  cast call --rpc-url "${rpc_url}" "${target}" "${signature}" "$@" | tr '[:upper:]' '[:lower:]'
}

observe_provider() {
  local rpc_url="$1"
  local head tx_json receipt_json block_json input code block_timestamp
  local observed_from observed_to observed_nonce observed_value observed_type observed_chain
  local observed_status observed_hash observed_block observed_block_hash observed_index observed_gas
  local observed_gas_price observed_l1_fee observed_input_hash observed_block_timestamp
  local module_topic escrow_topic selector_topic expected_owners owners threshold computed_safe_tx_hash address

  test "$(cast chain-id --rpc-url "${rpc_url}")" = '84532'
  head="$(cast block-number --rpc-url "${rpc_url}")"

  tx_json="$(cast tx --rpc-url "${rpc_url}" "${transaction}" --json)"
  receipt_json="$(cast receipt --rpc-url "${rpc_url}" "${transaction}" --json)"
  observed_from="$(jq -er '.from | ascii_downcase' <<<"${tx_json}")"
  observed_to="$(jq -er '.to | ascii_downcase' <<<"${tx_json}")"
  observed_nonce="$(cast to-dec "$(jq -er '.nonce' <<<"${tx_json}")")"
  observed_value="$(cast to-dec "$(jq -er '.value' <<<"${tx_json}")")"
  observed_type="$(cast to-dec "$(jq -er '.type' <<<"${tx_json}")")"
  observed_chain="$(cast to-dec "$(jq -er '.chainId' <<<"${tx_json}")")"
  input="$(jq -er '.input' <<<"${tx_json}")"
  observed_input_hash="$(printf '%s' "${input}" | cast keccak)"

  observed_status="$(jq -er '.status' <<<"${receipt_json}")"
  observed_hash="$(jq -er '.transactionHash | ascii_downcase' <<<"${receipt_json}")"
  observed_block="$(cast to-dec "$(jq -er '.blockNumber' <<<"${receipt_json}")")"
  observed_block_hash="$(jq -er '.blockHash | ascii_downcase' <<<"${receipt_json}")"
  observed_index="$(cast to-dec "$(jq -er '.transactionIndex' <<<"${receipt_json}")")"
  observed_gas="$(cast to-dec "$(jq -er '.gasUsed' <<<"${receipt_json}")")"
  observed_gas_price="$(cast to-dec "$(jq -er '.effectiveGasPrice' <<<"${receipt_json}")")"
  observed_l1_fee="$(cast to-dec "$(jq -er '.l1Fee' <<<"${receipt_json}")")"

  test "${observed_from}" = "$(jq -er '.execution.outerFrom' "${record}")"
  test "${observed_to}" = "$(jq -er '.execution.outerTo' "${record}")"
  test "${observed_nonce}" = "$(jq -er '.execution.outerNonce' "${record}")"
  test "${observed_value}" = '0'
  test "${observed_type}" = "$(jq -er '.execution.transactionType' "${record}")"
  test "${observed_chain}" = '84532'
  test "${observed_input_hash}" = "$(jq -er '.execution.outerInputHash' "${record}")"
  test "${observed_status}" = '0x1'
  test "${observed_hash}" = "${transaction}"
  test "${observed_block}" = "$(jq -er '.execution.blockNumber' "${record}")"
  test "${observed_block_hash}" = "$(jq -er '.execution.blockHash' "${record}")"
  test "${observed_index}" = "$(jq -er '.execution.transactionIndex' "${record}")"
  test "${observed_gas}" = "$(jq -er '.execution.gasUsed' "${record}")"
  test "${observed_gas_price}" = "$(jq -er '.execution.effectiveGasPriceWei' "${record}")"
  test "${observed_l1_fee}" = "$(jq -er '.execution.l1FeeWei' "${record}")"
  test "${head}" -ge "${observed_block}"

  block_json="$(cast block --rpc-url "${rpc_url}" "${observed_block}" --json)"
  test "$(jq -er '.hash | ascii_downcase' <<<"${block_json}")" = "${observed_block_hash}"
  block_timestamp="$(cast to-dec "$(jq -er '.timestamp' <<<"${block_json}")")"
  observed_block_timestamp="$(jq -nr --argjson timestamp "${block_timestamp}" '$timestamp | todateiso8601')"
  test "${observed_block_timestamp}" = "$(jq -er '.execution.blockTimestamp' "${record}")"

  module_topic="$(topic_address "${module}")"
  escrow_topic="$(topic_address "${escrow}")"
  selector_topic="$(topic_selector "$(cast sig 'setEscrowAllowlist(address,bytes32,bytes32,bytes32)')")"
  jq -e \
    --arg safe "${safe}" \
    --arg module "${module}" \
    --arg safeTxHash "${safe_tx_hash}" \
    --arg executionTopic "${execution_success_topic}" \
    --arg enabledTopic "${enabled_module_topic}" \
    --arg moduleTopic "${module_topic}" \
    --arg allowlistTopic "${allowlist_topic}" \
    --arg escrowTopic "${escrow_topic}" \
    --arg codeHash "${expected_code_hash}" \
    --arg workflowTopic "${workflow_topic}" \
    --arg workflowId "${workflow_id}" \
    --arg workflowPayloadHash "${workflow_payload_hash}" \
    --arg selectorTopic "${selector_topic}" '
      any(.logs[]; (.address | ascii_downcase) == $safe and .topics[0] == $executionTopic and .topics[1] == $safeTxHash)
      and any(.logs[]; (.address | ascii_downcase) == $safe and .topics[0] == $enabledTopic and .topics[1] == $moduleTopic)
      and any(.logs[]; (.address | ascii_downcase) == $module and .topics[0] == $allowlistTopic and .topics[1] == $escrowTopic and .topics[2] == $codeHash)
      and any(.logs[]; (.address | ascii_downcase) == $module and .topics[0] == $workflowTopic and .topics[1] == $workflowId and .topics[2] == $workflowPayloadHash and .topics[3] == $selectorTopic)
    ' <<<"${receipt_json}" >/dev/null

  computed_safe_tx_hash="$(cast call --rpc-url "${rpc_url}" "${safe}" \
    'getTransactionHash(address,uint256,bytes,uint8,uint256,uint256,uint256,address,address,uint256)(bytes32)' \
    "${safe_tx_to}" \
    "$(jq -er '.safeTransaction.value' "${record}")" \
    "${safe_tx_data}" \
    "$(jq -er '.safeTransaction.operation' "${record}")" \
    "$(jq -er '.safeTransaction.safeTxGas' "${record}")" \
    "$(jq -er '.safeTransaction.baseGas' "${record}")" \
    "$(jq -er '.safeTransaction.gasPrice' "${record}")" \
    "$(jq -er '.safeTransaction.gasToken' "${record}")" \
    "$(jq -er '.safeTransaction.refundReceiver' "${record}")" \
    "$(jq -er '.safeTransaction.nonce' "${record}")")"
  test "${computed_safe_tx_hash}" = "${safe_tx_hash}"

  expected_owners="$(jq -r '.safe.owners | sort | join(",")' "${deployment_record}")"
  owners="$(cast call --rpc-url "${rpc_url}" "${safe}" 'getOwners()(address[])' --json | jq -er '.[0] | map(ascii_downcase) | sort | join(",")')"
  threshold="$(call_address "${rpc_url}" "${safe}" 'getThreshold()(uint256)')"
  test "${owners}" = "${expected_owners}"
  test "${threshold}" = "$(jq -er '.safe.threshold' "${record}")"
  test "$(call_address "${rpc_url}" "${safe}" 'nonce()(uint256)')" = "$(jq -er '.postState.safeNonce' "${record}")"
  test "$(call_address "${rpc_url}" "${safe}" 'isModuleEnabled(address)(bool)' "${module}")" = 'true'

  code="$(cast code --rpc-url "${rpc_url}" "${escrow}")"
  test "${code}" != '0x'
  test "$(printf '%s' "${code}" | cast keccak)" = "${expected_code_hash}"
  test "$(call_address "${rpc_url}" "${module}" 'escrowAllowlist(address)(bytes32)' "${escrow}")" = "${expected_code_hash}"
  test "$(call_address "${rpc_url}" "${directory}" 'currentVersion()(uint64)')" = '0'
  test "$(call_address "${rpc_url}" "${directory}" 'currentRoot()(bytes32)')" = "${zero_digest}"
  test "$(call_address "${rpc_url}" "${registry}" 'agentCount()(uint256)')" = '0'
  test "$(call_address "${rpc_url}" "${escrow}" 'totalLocked()(uint256)')" = '0'
  test "$(call_address "${rpc_url}" "${escrow}" 'emergencyPaused()(bool)')" = 'false'
  test "$(call_address "${rpc_url}" "${module}" 'executedPrincipal()(uint256)')" = '0'
  test "$(call_address "${rpc_url}" "${module}" 'emergencyPaused()(bool)')" = 'false'
  test "$(cast balance --rpc-url "${rpc_url}" "${safe}")" = '0'
  test "$(call_address "${rpc_url}" "${asset}" 'balanceOf(address)(uint256)' "${safe}")" = '0'
  for address in "${directory}" "${registry}" "${escrow}" "${module}"; do
    test "$(cast balance --rpc-url "${rpc_url}" "${address}")" = '0'
    test "$(call_address "${rpc_url}" "${asset}" 'balanceOf(address)(uint256)' "${address}")" = '0'
  done
  test "$(call_address "${rpc_url}" "${asset}" 'allowance(address,address)(uint256)' "${safe}" "${module}")" = '0'
  test "$(call_address "${rpc_url}" "${asset}" 'allowance(address,address)(uint256)' "${safe}" "${escrow}")" = '0'

  printf '%s:%s:%s:%s:%s:%s\n' \
    "${observed_hash}" "${observed_block_hash}" "${safe_tx_hash}" "${module_topic}" "${expected_code_hash}" "${head}"
}

primary_observation="$(mktemp -t flowops-ascp-activation-primary.XXXXXX)"
secondary_observation="$(mktemp -t flowops-ascp-activation-secondary.XXXXXX)"
trap 'rm -f "${primary_observation}" "${secondary_observation}"' EXIT
observe_provider "${primary_rpc}" >"${primary_observation}"
observe_provider "${secondary_rpc}" >"${secondary_observation}"

# Provider heads may advance independently. Compare every canonical field and
# omit the trailing observed head from the equality check.
cut -d: -f1-5 "${primary_observation}" >"${primary_observation}.canonical"
cut -d: -f1-5 "${secondary_observation}" >"${secondary_observation}.canonical"
cmp -s "${primary_observation}.canonical" "${secondary_observation}.canonical"
rm -f "${primary_observation}.canonical" "${secondary_observation}.canonical"

# A successful Safe event proves threshold authorization but does not identify
# the individual confirming owners. Bind that separately to Safe's indexed
# transaction record, including the exact payload and execution transaction.
safe_service_json="$(curl --fail --silent --show-error --location "${safe_service_url}")"
expected_confirming_owners="$(jq -r '.safe.confirmedOwners | sort | join(",")' "${record}")"
observed_confirming_owners="$(jq -er '[.confirmations[].owner | ascii_downcase] | sort | join(",")' <<<"${safe_service_json}")"
jq -e \
  --arg safe "${safe}" \
  --arg safeTxHash "${safe_tx_hash}" \
  --arg executionTx "${transaction}" \
  --arg to "${safe_tx_to}" \
  --arg data "${safe_tx_data}" \
  --argjson nonce "$(jq -er '.safeTransaction.nonce' "${record}")" \
  --argjson threshold "$(jq -er '.safe.threshold' "${record}")" '
    (.safe | ascii_downcase) == $safe
    and .safeTxHash == $safeTxHash
    and (.transactionHash | ascii_downcase) == $executionTx
    and .isExecuted == true
    and .isSuccessful == true
    and .nonce == $nonce
    and .confirmationsRequired == $threshold
    and (.confirmations | length) == $threshold
    and (.confirmations | all(.signature != null and (.signatureType == "EOA" or .signatureType == "APPROVED_HASH" or .signatureType == "CONTRACT_SIGNATURE")))
    and (.to | ascii_downcase) == $to
    and .value == "0"
    and .operation == 1
    and .data == $data
  ' <<<"${safe_service_json}" >/dev/null
test "${observed_confirming_owners}" = "${expected_confirming_owners}"

printf 'ASCP v4 Base Sepolia activation, exact Safe confirmations, and funding-disabled post-state verified read-only\n'
