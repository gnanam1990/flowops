#!/usr/bin/env bash
set -euo pipefail

# Re-observes the historical zero-fund activation at its pinned block through
# two independent RPC providers. It never signs or sends a transaction.
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
record="${FLOWOPS_ASCP_MAINNET_ACTIVATION_RECORD:-${repo_root}/deployments/base-mainnet-ascp-activation-v1.json}"
deployment_record="${repo_root}/deployments/base-mainnet-ascp-experimental-v1.json"
primary_rpc="${BASE_MAINNET_RPC_URL_PRIMARY:-https://mainnet.base.org}"
secondary_rpc="${BASE_MAINNET_RPC_URL_SECONDARY:-https://base-mainnet.public.blastapi.io}"

FLOWOPS_ASCP_MAINNET_ACTIVATION_RECORD="${record}" "${repo_root}/deploy/ascp/check-base-mainnet-activation-evidence.sh" >/dev/null

rpc_host() { sed -E 's#^[a-zA-Z]+://([^/:]+).*#\1#' <<<"$1" | tr '[:upper:]' '[:lower:]'; }
test "$(rpc_host "${primary_rpc}")" != "$(rpc_host "${secondary_rpc}")"

safe="$(jq -er '.safe.address' "${record}")"
module="$(jq -er '.contracts.spendModule' "${record}")"
escrow="$(jq -er '.contracts.escrow' "${record}")"
directory="$(jq -er '.contracts.serviceDirectory' "${record}")"
registry="$(jq -er '.contracts.agentRegistry' "${record}")"
asset="$(jq -er '.contracts.asset' "${record}")"
transaction="$(jq -er '.execution.transactionHash' "${record}")"
activation_block="$(jq -er '.execution.blockNumber' "${record}")"
safe_tx_hash="$(jq -er '.safe.safeTxHash' "${record}")"
expected_code_hash="$(jq -er '.actions[0].escrowRuntimeCodeHash' "${record}")"
zero_digest='0x0000000000000000000000000000000000000000000000000000000000000000'

topic_address() { printf '0x%064s' "${1#0x}" | tr ' ' '0'; }
topic_selector() { printf '0x%-64s' "${1#0x}" | tr ' ' '0'; }
call_at() {
  local rpc_url="$1" target="$2" signature="$3"; shift 3
  cast call --block "${activation_block}" --rpc-url "${rpc_url}" "${target}" "${signature}" "$@" | tr '[:upper:]' '[:lower:]'
}

observe_provider() {
  local rpc_url="$1" head tx_json receipt_json code input_hash owners module_topic escrow_topic selector_topic
  test "$(cast chain-id --rpc-url "${rpc_url}")" = '8453'
  head="$(cast block-number --rpc-url "${rpc_url}")"
  test "${head}" -ge "${activation_block}"
  tx_json="$(cast tx --rpc-url "${rpc_url}" "${transaction}" --json)"
  receipt_json="$(cast receipt --rpc-url "${rpc_url}" "${transaction}" --json)"
  test "$(jq -er '.from | ascii_downcase' <<<"${tx_json}")" = "$(jq -er '.execution.outerFrom' "${record}")"
  test "$(jq -er '.to | ascii_downcase' <<<"${tx_json}")" = "${safe}"
  test "$(cast to-dec "$(jq -er '.nonce' <<<"${tx_json}")")" = "$(jq -er '.execution.outerNonce' "${record}")"
  test "$(cast to-dec "$(jq -er '.value' <<<"${tx_json}")")" = '0'
  test "$(cast to-dec "$(jq -er '.chainId' <<<"${tx_json}")")" = '8453'
  input_hash="$(printf '%s' "$(jq -er '.input' <<<"${tx_json}")" | cast keccak)"
  test "${input_hash}" = "$(jq -er '.execution.outerInputHash' "${record}")"
  test "$(jq -er '.status' <<<"${receipt_json}")" = '0x1'
  test "$(jq -er '.transactionHash | ascii_downcase' <<<"${receipt_json}")" = "${transaction}"
  test "$(cast to-dec "$(jq -er '.blockNumber' <<<"${receipt_json}")")" = "${activation_block}"
  test "$(jq -er '.blockHash | ascii_downcase' <<<"${receipt_json}")" = "$(jq -er '.execution.blockHash' "${record}")"

  module_topic="$(topic_address "${module}")"
  escrow_topic="$(topic_address "${escrow}")"
  selector_topic="$(topic_selector "$(cast sig 'setEscrowAllowlist(address,bytes32,bytes32,bytes32)')")"
  jq -e \
    --arg safe "${safe}" --arg module "${module}" --arg safeTxHash "${safe_tx_hash}" \
    --arg multi "$(jq -er '.eventEvidence.safeMultiSigTransactionTopic' "${record}")" \
    --arg success "$(jq -er '.eventEvidence.executionSuccessTopic' "${record}")" \
    --arg enabled "$(jq -er '.eventEvidence.enabledModuleTopic' "${record}")" --arg moduleTopic "${module_topic}" \
    --arg allowlist "$(jq -er '.eventEvidence.escrowAllowlistSetTopic' "${record}")" --arg escrowTopic "${escrow_topic}" \
    --arg codeHash "${expected_code_hash}" --arg workflow "$(jq -er '.eventEvidence.governanceWorkflowBoundTopic' "${record}")" \
    --arg workflowId "$(jq -er '.actions[0].workflowId' "${record}")" --arg workflowHash "$(jq -er '.actions[0].workflowPayloadHash' "${record}")" \
    --arg selectorTopic "${selector_topic}" '
      (.logs | length) == 5
      and any(.logs[]; (.address|ascii_downcase) == $safe and .topics[0] == $multi)
      and any(.logs[]; (.address|ascii_downcase) == $safe and .topics[0] == $success and .topics[1] == $safeTxHash)
      and any(.logs[]; (.address|ascii_downcase) == $safe and .topics[0] == $enabled and .topics[1] == $moduleTopic)
      and any(.logs[]; (.address|ascii_downcase) == $module and .topics[0] == $allowlist and .topics[1] == $escrowTopic and .topics[2] == $codeHash)
      and any(.logs[]; (.address|ascii_downcase) == $module and .topics[0] == $workflow and .topics[1] == $workflowId and .topics[2] == $workflowHash and .topics[3] == $selectorTopic)
    ' <<<"${receipt_json}" >/dev/null

  owners="$(cast call --block "${activation_block}" --rpc-url "${rpc_url}" "${safe}" 'getOwners()(address[])' --json | jq -er '.[0] | map(ascii_downcase) | sort | join(",")')"
  test "${owners}" = "$(jq -r '.safe.owners | sort | join(",")' "${deployment_record}")"
  test "$(call_at "${rpc_url}" "${safe}" 'getThreshold()(uint256)')" = '2'
  test "$(call_at "${rpc_url}" "${safe}" 'nonce()(uint256)')" = '1'
  test "$(call_at "${rpc_url}" "${safe}" 'isModuleEnabled(address)(bool)' "${module}")" = 'true'
  code="$(cast code --block "${activation_block}" --rpc-url "${rpc_url}" "${escrow}")"
  test "$(printf '%s' "${code}" | cast keccak)" = "${expected_code_hash}"
  test "$(call_at "${rpc_url}" "${module}" 'escrowAllowlist(address)(bytes32)' "${escrow}")" = "${expected_code_hash}"
  test "$(call_at "${rpc_url}" "${module}" 'emergencyPaused()(bool)')" = 'false'
  test "$(call_at "${rpc_url}" "${module}" 'executedPrincipal()(uint256)')" = '0'
  test "$(call_at "${rpc_url}" "${escrow}" 'emergencyPaused()(bool)')" = 'false'
  test "$(call_at "${rpc_url}" "${escrow}" 'totalLocked()(uint256)')" = '0'
  test "$(call_at "${rpc_url}" "${directory}" 'currentVersion()(uint64)')" = '0'
  test "$(call_at "${rpc_url}" "${directory}" 'currentRoot()(bytes32)')" = "${zero_digest}"
  test "$(call_at "${rpc_url}" "${registry}" 'agentCount()(uint256)')" = '0'
  test "$(cast balance --block "${activation_block}" --rpc-url "${rpc_url}" "${safe}")" = '0'
  test "$(call_at "${rpc_url}" "${asset}" 'balanceOf(address)(uint256)' "${safe}")" = '0'
  for address in "${directory}" "${registry}" "${escrow}" "${module}"; do
    test "$(cast balance --block "${activation_block}" --rpc-url "${rpc_url}" "${address}")" = '0'
    test "$(call_at "${rpc_url}" "${asset}" 'balanceOf(address)(uint256)' "${address}")" = '0'
  done
  test "$(call_at "${rpc_url}" "${asset}" 'allowance(address,address)(uint256)' "${safe}" "${module}")" = '0'
  test "$(call_at "${rpc_url}" "${asset}" 'allowance(address,address)(uint256)' "${safe}" "${escrow}")" = '0'
  printf '%s:%s:%s:%s\n' "${transaction}" "$(jq -er '.blockHash' <<<"${receipt_json}")" "${safe_tx_hash}" "${expected_code_hash}"
}

primary_observation="$(mktemp -t flowops-mainnet-activation-primary.XXXXXX)"
secondary_observation="$(mktemp -t flowops-mainnet-activation-secondary.XXXXXX)"
trap 'rm -f "${primary_observation}" "${secondary_observation}"' EXIT
observe_provider "${primary_rpc}" >"${primary_observation}"
observe_provider "${secondary_rpc}" >"${secondary_observation}"
cmp -s "${primary_observation}" "${secondary_observation}"

safe_service_json="$(curl --fail --silent --show-error --location --max-time 15 --retry 2 "$(jq -er '.verification.safeTransactionServiceUrl' "${record}")")"
observed_confirmers="$(jq -er '[.confirmations[].owner | ascii_downcase] | sort | join(",")' <<<"${safe_service_json}")"
test "${observed_confirmers}" = "$(jq -r '.safe.confirmedOwners | sort | join(",")' "${record}")"
jq -e --arg safe "${safe}" --arg safeTxHash "${safe_tx_hash}" --arg transaction "${transaction}" --arg data "$(jq -er '.safeTransaction.data' "${record}")" '
  (.safe|ascii_downcase) == $safe and .safeTxHash == $safeTxHash and (.transactionHash|ascii_downcase) == $transaction
  and .isExecuted == true and .isSuccessful == true and .nonce == 0 and .confirmationsRequired == 2
  and (.confirmations|length) == 2 and .operation == 1 and .value == "0" and .data == $data
' <<<"${safe_service_json}" >/dev/null

printf 'Base mainnet ASCP activation and zero-fund post-state verified read-only through two RPC providers\n'
