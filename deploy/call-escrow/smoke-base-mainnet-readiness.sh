#!/usr/bin/env bash
set -euo pipefail

: "${BASE_MAINNET_RPC_URL_PRIMARY:?set BASE_MAINNET_RPC_URL_PRIMARY}"
: "${BASE_MAINNET_RPC_URL_SECONDARY:?set BASE_MAINNET_RPC_URL_SECONDARY}"

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
expected_chain_id="8453"
expected_asset="0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913"
expected_code_hash="$(jq -r '.canonicalUsdc.runtimeCodeHash' "${repo_root}/deployments/base-mainnet-readiness.json")"
max_head_skew="${FLOWOPS_MAINNET_MAX_HEAD_SKEW:-12}"

rpc_host() {
  local rpc_url="$1"

  RPC_URL="${rpc_url}" jq -er -n '
    env.RPC_URL
    | capture("^[A-Za-z][A-Za-z0-9+.-]*://(?:[^@/?#]+@)?(?<host>\\[[^]]+\\]|[^:/?#]+)")
    | .host
    | ascii_downcase
  '
}

primary_host="$(rpc_host "${BASE_MAINNET_RPC_URL_PRIMARY}")"
secondary_host="$(rpc_host "${BASE_MAINNET_RPC_URL_SECONDARY}")"
test "${primary_host}" != "${secondary_host}"

observe() {
  local rpc_url="$1"
  local chain_id code code_hash decimals symbol head

  chain_id="$(ETH_RPC_URL="${rpc_url}" cast chain-id)"
  test "${chain_id}" = "${expected_chain_id}"

  code="$(ETH_RPC_URL="${rpc_url}" cast code "${expected_asset}")"
  test "${code}" != "0x"
  code_hash="$(printf '%s' "${code}" | cast keccak)"
  decimals="$(ETH_RPC_URL="${rpc_url}" cast call "${expected_asset}" 'decimals()(uint8)')"
  symbol="$(ETH_RPC_URL="${rpc_url}" cast call "${expected_asset}" 'symbol()(string)' | tr -d '"')"
  head="$(ETH_RPC_URL="${rpc_url}" cast block-number)"

  test "${decimals}" = "6"
  test "${symbol}" = "USDC"
  printf '%s\t%s\n' "${head}" "${code_hash}"
}

IFS=$'\t' read -r primary_head primary_code_hash < <(observe "${BASE_MAINNET_RPC_URL_PRIMARY}")
IFS=$'\t' read -r secondary_head secondary_code_hash < <(observe "${BASE_MAINNET_RPC_URL_SECONDARY}")

test "${primary_code_hash}" = "${secondary_code_hash}"
test "${primary_code_hash}" = "${expected_code_hash}"

if (( primary_head < secondary_head )); then
  anchor_head="${primary_head}"
  head_skew=$((secondary_head - primary_head))
else
  anchor_head="${secondary_head}"
  head_skew=$((primary_head - secondary_head))
fi
test "${head_skew}" -le "${max_head_skew}"

primary_anchor_hash="$(ETH_RPC_URL="${BASE_MAINNET_RPC_URL_PRIMARY}" cast block "${anchor_head}" --json | jq -r '.hash')"
secondary_anchor_hash="$(ETH_RPC_URL="${BASE_MAINNET_RPC_URL_SECONDARY}" cast block "${anchor_head}" --json | jq -r '.hash')"
test -n "${primary_anchor_hash}"
test "${primary_anchor_hash}" != "null"
test "${primary_anchor_hash}" = "${secondary_anchor_hash}"

printf 'Base mainnet read-only preflight passed: asset=%s heads=%s/%s anchor=%s codeHash=%s\n' \
  "${expected_asset}" "${primary_head}" "${secondary_head}" "${anchor_head}" "${primary_code_hash}"
