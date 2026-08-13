#!/usr/bin/env bash
set -euo pipefail

: "${BASE_SEPOLIA_RPC_URL:?set BASE_SEPOLIA_RPC_URL}"
: "${CALL_ESCROW_ADDRESS:?set CALL_ESCROW_ADDRESS}"
: "${DEPLOYMENT_TX_HASH:?set DEPLOYMENT_TX_HASH}"

expected_chain_id="84532"
expected_deployer="0x079bdde909e28e437768a06d7001eb40896668d4"
expected_asset="0x036cbd53842c5426634e7929541ec2318f3dcf7e"
expected_window="3600"

actual_chain_id="$(cast chain-id --rpc-url "${BASE_SEPOLIA_RPC_URL}")"
test "${actual_chain_id}" = "${expected_chain_id}"

runtime_code="$(cast code "${CALL_ESCROW_ADDRESS}" --rpc-url "${BASE_SEPOLIA_RPC_URL}")"
test "${runtime_code}" != "0x"

actual_asset="$(cast call "${CALL_ESCROW_ADDRESS}" 'asset()(address)' --rpc-url "${BASE_SEPOLIA_RPC_URL}" | tr '[:upper:]' '[:lower:]')"
actual_window="$(cast call "${CALL_ESCROW_ADDRESS}" 'optimisticReleaseWindow()(uint256)' --rpc-url "${BASE_SEPOLIA_RPC_URL}")"
test "${actual_asset}" = "${expected_asset}"
test "${actual_window}" = "${expected_window}"

receipt_status="$(cast receipt "${DEPLOYMENT_TX_HASH}" status --rpc-url "${BASE_SEPOLIA_RPC_URL}")"
receipt_from="$(cast receipt "${DEPLOYMENT_TX_HASH}" from --rpc-url "${BASE_SEPOLIA_RPC_URL}" | tr '[:upper:]' '[:lower:]')"
receipt_contract="$(cast receipt "${DEPLOYMENT_TX_HASH}" contractAddress --rpc-url "${BASE_SEPOLIA_RPC_URL}" | tr '[:upper:]' '[:lower:]')"
expected_contract="$(printf '%s' "${CALL_ESCROW_ADDRESS}" | tr '[:upper:]' '[:lower:]')"
test "${receipt_status}" = "true"
test "${receipt_from}" = "${expected_deployer}"
test "${receipt_contract}" = "${expected_contract}"

printf 'verified Base Sepolia CallEscrow %s from %s\n' "${CALL_ESCROW_ADDRESS}" "${DEPLOYMENT_TX_HASH}"
