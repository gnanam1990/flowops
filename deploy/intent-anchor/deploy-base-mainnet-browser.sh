#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
record="${repo_root}/deployments/base-mainnet-intent-anchor-promotion.json"
cd "${repo_root}"

: "${FLOWOPS_EXPLICIT_INTENT_ANCHOR_APPROVAL_SHA256:?set the exact approved digest}"
: "${BASE_MAINNET_RPC_URL_PRIMARY:?set the primary Base mainnet RPC URL}"
: "${BASE_MAINNET_RPC_URL_SECONDARY:?set the secondary Base mainnet RPC URL}"

test -z "$(git status --short)"
test "$(git rev-parse HEAD)" = "$(git rev-parse origin/main)"
"${repo_root}/deploy/intent-anchor/check-base-mainnet-intent-anchor-promotion.sh"

jq -e --arg approval "${FLOWOPS_EXPLICIT_INTENT_ANCHOR_APPROVAL_SHA256}" '
  .status == "approved-zero-value"
  and .approval.sha256 == $approval
  and .gasCeilings.transactionValueWei == "0"
  and .fundingEnabled == false
  and .executesPayments == false
' "${record}" >/dev/null

deployer="$(jq -r '.deployer' "${record}")"
expected_nonce="$(jq -r '.expectedDeployerNonce' "${record}")"
expected_address="$(jq -r '.expectedContractAddress' "${record}")"
maximum_gas="$(jq -r '.gasCeilings.maxGasLimit' "${record}")"
maximum_fee="$(jq -r '.gasCeilings.maxFeePerGasWei' "${record}")"
maximum_priority="$(jq -r '.gasCeilings.maxPriorityFeePerGasWei' "${record}")"

require_chain_state() {
  local rpc_url="$1"
  test "$(cast chain-id --rpc-url "${rpc_url}")" = "8453"
  test "$(cast nonce "${deployer}" --block latest --rpc-url "${rpc_url}")" = "${expected_nonce}"
  test "$(cast nonce "${deployer}" --block pending --rpc-url "${rpc_url}")" = "${expected_nonce}"
  test "$(cast code "${expected_address}" --rpc-url "${rpc_url}")" = "0x"
}

require_chain_state "${BASE_MAINNET_RPC_URL_PRIMARY}"
require_chain_state "${BASE_MAINNET_RPC_URL_SECONDARY}"

simulation="$(FOUNDRY_ETH_RPC_URL="${BASE_MAINNET_RPC_URL_PRIMARY}" forge script \
  contracts/script/DeployFlowOpsIntentAnchorBaseMainnet.s.sol:DeployFlowOpsIntentAnchorBaseMainnet \
  --sender "${deployer}" \
  --gas-estimate-multiplier 120 \
  --json)"
jq -s -e --argjson maximum "${maximum_gas}" '
  ([.[] | select(has("success"))] | length == 1)
  and ([.[] | select(has("estimated_total_gas_used"))] | length == 1)
  and ([.[] | select(has("success"))][0].success == true)
  and ([.[] | select(has("estimated_total_gas_used"))][0].chain == 8453)
  and (([.[] | select(has("estimated_total_gas_used"))][0].estimated_total_gas_used | tonumber) <= $maximum)
' <<<"${simulation}" >/dev/null

require_chain_state "${BASE_MAINNET_RPC_URL_PRIMARY}"
require_chain_state "${BASE_MAINNET_RPC_URL_SECONDARY}"

attempt_dir="$(git rev-parse --git-path flowops-intent-anchor-mainnet-ceremony)"
attempt_file="${attempt_dir}/${FLOWOPS_EXPLICIT_INTENT_ANCHOR_APPROVAL_SHA256}.attempted"
mkdir -p "${attempt_dir}"
umask 077
if ! (set -o noclobber; printf '%s\t%s\t%s\t%s\n' \
  "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" \
  "$(git rev-parse HEAD)" \
  "${deployer}" \
  "${expected_nonce}" >"${attempt_file}") 2>/dev/null; then
  printf 'approval already attempted; inspect chain state and issue a new approval\n' >&2
  exit 1
fi

FOUNDRY_ETH_RPC_URL="${BASE_MAINNET_RPC_URL_PRIMARY}" forge script \
  contracts/script/DeployFlowOpsIntentAnchorBaseMainnet.s.sol:DeployFlowOpsIntentAnchorBaseMainnet \
  --browser \
  --sender "${deployer}" \
  --gas-estimate-multiplier 120 \
  --with-gas-price "${maximum_fee}" \
  --priority-gas-price "${maximum_priority}" \
  --broadcast
