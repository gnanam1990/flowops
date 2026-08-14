#!/usr/bin/env bash
set -euo pipefail

# Canonically checks a future zero-fund deployment through both admitted
# production observers. It never signs, sends, retries, or submits source.
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
record="${repo_root}/deployments/base-mainnet-readiness.json"
cd "${repo_root}"

: "${BASE_MAINNET_RPC_URL_PRIMARY:?load the primary production RPC secret}"
: "${BASE_MAINNET_RPC_URL_SECONDARY:?load the secondary production RPC secret}"
: "${FLOWOPS_BASE_RPC_PROVIDERS_JSON:?load the production provider secret JSON}"
: "${FLOWOPS_BASE_RPC_ADMISSION_JSON:?load the reviewed production provider admission JSON}"

FLOWOPS_BASE_CHAIN_ID=8453 go run ./cmd/rpc-admission-check >/dev/null
env -u FLOWOPS_MAINNET_SOURCE_REHEARSAL_RECORD \
  deploy/call-escrow/check-base-mainnet-source-rehearsal.sh >/dev/null

address="$(jq -er '.callEscrow.address | ascii_downcase | select(test("^0x[0-9a-f]{40}$"))' "${record}")"
transaction="$(jq -er '.callEscrow.deploymentTransaction | ascii_downcase | select(test("^0x[0-9a-f]{64}$"))' "${record}")"
deployment_block="$(jq -er '.callEscrow.deploymentBlock | select(type == "number" and . > 0)' "${record}")"
minimum_confirmations="$(jq -er '.callEscrow.minimumDeploymentConfirmations | select(type == "number" and floor == . and . > 0 and . <= 10000)' "${record}")"
deployer="$(jq -er '.gates.designatedDeployer | ascii_downcase | select(test("^0x[0-9a-f]{40}$"))' "${record}")"
expected_runtime_hash="$(jq -er '.callEscrow.runtimeCodeHash | select(test("^0x[0-9a-f]{64}$"))' "${record}")"
asset="$(jq -er '.canonicalUsdc.address' "${record}")"
release_window="$(jq -er '.callEscrow.optimisticReleaseWindowSeconds' "${record}")"
constructor_args="$(cast abi-encode 'constructor(address,uint256)' "${asset}" "${release_window}")"
creation_bytecode="$(forge inspect contracts/src/CallEscrow.sol:CallEscrow bytecode)"
expected_input="$(tr '[:upper:]' '[:lower:]' <<<"${creation_bytecode}${constructor_args#0x}")"

observe() {
  local rpc_url="$1"
  local chain_id tx_json receipt_json code from to input value receipt_address receipt_transaction receipt_block receipt_block_hash canonical_block_hash head status observed_runtime_hash observed_asset observed_window observed_balance observed_locked
  chain_id="$(ETH_RPC_URL="${rpc_url}" cast chain-id)"
  test "${chain_id}" = '8453'
  tx_json="$(ETH_RPC_URL="${rpc_url}" cast tx "${transaction}" --json)"
  receipt_json="$(ETH_RPC_URL="${rpc_url}" cast receipt "${transaction}" --json)"
  from="$(jq -er '.from | ascii_downcase' <<<"${tx_json}")"
  to="$(jq -r '.to' <<<"${tx_json}")"
  input="$(jq -er '.input | ascii_downcase' <<<"${tx_json}")"
  value="$(jq -er '.value' <<<"${tx_json}")"
  receipt_address="$(jq -er '.contractAddress | ascii_downcase' <<<"${receipt_json}")"
  receipt_transaction="$(jq -er '.transactionHash | ascii_downcase' <<<"${receipt_json}")"
  receipt_block="$(jq -er '.blockNumber' <<<"${receipt_json}")"
  receipt_block_hash="$(jq -er '.blockHash | ascii_downcase | select(test("^0x[0-9a-f]{64}$"))' <<<"${receipt_json}")"
  status="$(jq -er '.status' <<<"${receipt_json}")"
  test "${from}" = "${deployer}"
  test "${to}" = 'null'
  test "${input}" = "${expected_input}"
  test "$(cast to-dec "${value}")" = '0'
  test "${receipt_address}" = "${address}"
  test "${receipt_transaction}" = "${transaction}"
  test "$(cast to-dec "${receipt_block}")" = "${deployment_block}"
  test "${status}" = '0x1'
  head="$(ETH_RPC_URL="${rpc_url}" cast block-number)"
  test "${head}" -ge "$((deployment_block + minimum_confirmations))"
  canonical_block_hash="$(ETH_RPC_URL="${rpc_url}" cast block "${deployment_block}" --json | jq -er '.hash | ascii_downcase')"
  test "${canonical_block_hash}" = "${receipt_block_hash}"
  code="$(ETH_RPC_URL="${rpc_url}" cast code "${address}")"
  test "${code}" != '0x'
  observed_runtime_hash="$(printf '%s' "${code}" | cast keccak)"
  test "${observed_runtime_hash}" = "${expected_runtime_hash}"
  observed_asset="$(ETH_RPC_URL="${rpc_url}" cast call "${address}" 'asset()(address)' | tr '[:upper:]' '[:lower:]')"
  observed_window="$(ETH_RPC_URL="${rpc_url}" cast call "${address}" 'optimisticReleaseWindow()(uint256)')"
  observed_balance="$(ETH_RPC_URL="${rpc_url}" cast call "${asset}" 'balanceOf(address)(uint256)' "${address}")"
  observed_locked="$(ETH_RPC_URL="${rpc_url}" cast call "${address}" 'totalLocked()(uint256)')"
  test "${observed_asset}" = "$(tr '[:upper:]' '[:lower:]' <<<"${asset}")"
  test "${observed_window}" = "${release_window}"
  test "${observed_balance}" = '0'
  test "${observed_locked}" = '0'
  printf '%s\t%s\t%s\n' "${receipt_address}" "${observed_runtime_hash}" "${receipt_block_hash}"
}

primary="$(observe "${BASE_MAINNET_RPC_URL_PRIMARY}")"
secondary="$(observe "${BASE_MAINNET_RPC_URL_SECONDARY}")"
test "${primary}" = "${secondary}"

printf 'Base mainnet deployment and exact creation input verified read-only through both production observers\n'
