#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
evidence="${repo_root}/docs/evidence/REFERENCE_SIGNER_FUNDED_ESCROW_2026-08-15.json"
primary="${BASE_SEPOLIA_RPC_URL_PRIMARY:-https://sepolia.base.org}"
secondary="${BASE_SEPOLIA_RPC_URL_SECONDARY:-https://base-sepolia-rpc.publicnode.com}"
tmp_dir="$(mktemp -d)"
trap 'rm -rf "${tmp_dir}"' EXIT

"${repo_root}/deploy/call-escrow/check-funded-reference-signer-evidence.sh" >/dev/null

rpc_host() {
  local rpc_url="$1"

  RPC_URL="${rpc_url}" jq -er -n '
    env.RPC_URL
    | capture("^[A-Za-z][A-Za-z0-9+.-]*://(?:[^@/?#]+@)?(?<host>\\[[^]]+\\]|[^:/?#]+)")
    | .host
    | ascii_downcase
  '
}

test "$(rpc_host "${primary}")" != "$(rpc_host "${secondary}")"

observe() {
  local rpc_url="$1"
  local output="$2"
  local fund_hash refund_hash asset buyer contract
  local fund_receipt refund_receipt fund_transaction refund_transaction
  local escrow_balance allowance

  test "$(ETH_RPC_URL="${rpc_url}" cast chain-id)" = "84532"
  fund_hash="$(jq -r '.transitions[0].transactionHash' "${evidence}")"
  refund_hash="$(jq -r '.transitions[1].transactionHash' "${evidence}")"
  asset="$(jq -r '.deployment.asset' "${evidence}")"
  buyer="$(jq -r '.actors.buyer' "${evidence}")"
  contract="$(jq -r '.deployment.contract' "${evidence}")"

  fund_receipt="$(ETH_RPC_URL="${rpc_url}" cast rpc eth_getTransactionReceipt "${fund_hash}")"
  refund_receipt="$(ETH_RPC_URL="${rpc_url}" cast rpc eth_getTransactionReceipt "${refund_hash}")"
  fund_transaction="$(ETH_RPC_URL="${rpc_url}" cast rpc eth_getTransactionByHash "${fund_hash}")"
  refund_transaction="$(ETH_RPC_URL="${rpc_url}" cast rpc eth_getTransactionByHash "${refund_hash}")"
  escrow_balance="$(ETH_RPC_URL="${rpc_url}" cast call "${asset}" 'balanceOf(address)(uint256)' "${contract}")"
  allowance="$(ETH_RPC_URL="${rpc_url}" cast call "${asset}" 'allowance(address,address)(uint256)' "${buyer}" "${contract}")"

  jq -n \
    --argjson fund_receipt "${fund_receipt}" \
    --argjson refund_receipt "${refund_receipt}" \
    --argjson fund_transaction "${fund_transaction}" \
    --argjson refund_transaction "${refund_transaction}" \
    --arg escrow_balance "${escrow_balance}" \
    --arg allowance "${allowance}" '
    def receipt($r): {
      transactionHash: $r.transactionHash,
      blockNumber: $r.blockNumber,
      blockHash: $r.blockHash,
      status: $r.status,
      from: $r.from,
      to: $r.to,
      logs: [$r.logs[] | {address, topics, data}]
    };
    def transaction($t): {
      hash: $t.hash,
      from: $t.from,
      to: $t.to,
      input: $t.input,
      value: $t.value
    };
    {
      fund: {receipt: receipt($fund_receipt), transaction: transaction($fund_transaction)},
      refund: {receipt: receipt($refund_receipt), transaction: transaction($refund_transaction)},
      terminal: {escrowBalanceAtomic: $escrow_balance, buyerAllowanceAtomic: $allowance}
    }
  ' >"${output}"
}

observe "${primary}" "${tmp_dir}/primary.json"
observe "${secondary}" "${tmp_dir}/secondary.json"
cmp -s "${tmp_dir}/primary.json" "${tmp_dir}/secondary.json"

jq -e \
  --arg fund_block_hex "$(cast to-hex "$(jq -r '.transitions[0].blockNumber' "${evidence}")")" \
  --arg refund_block_hex "$(cast to-hex "$(jq -r '.transitions[1].blockNumber' "${evidence}")")" \
  --slurpfile observed "${tmp_dir}/primary.json" '
  ($observed[0]) as $o
  | ($o.fund) as $f
  | ($o.refund) as $r
  | $f.receipt.transactionHash == .transitions[0].transactionHash
  and $f.receipt.blockNumber == $fund_block_hex
  and $f.receipt.blockHash == .transitions[0].blockHash
  and $f.receipt.status == "0x1"
  and $f.receipt.from == .actors.buyer
  and $f.receipt.to == .deployment.contract
  and ($f.receipt.logs | length == 2)
  and $f.receipt.logs[0].address == .deployment.asset
  and $f.receipt.logs[0].topics[0] == "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"
  and $f.receipt.logs[1].address == .deployment.contract
  and $f.receipt.logs[1].topics[0] == "0x7e04c416707d16b45b505415891eadc7f4d4386a1b582c6feac125744baf8838"
  and $f.receipt.logs[1].topics[1] == .call.callId
  and $f.transaction.hash == .transitions[0].transactionHash
  and $f.transaction.from == .actors.buyer
  and $f.transaction.to == .deployment.contract
  and $f.transaction.input == .transitions[0].calldata
  and $f.transaction.value == "0x0"
  and $r.receipt.transactionHash == .transitions[1].transactionHash
  and $r.receipt.blockNumber == $refund_block_hex
  and $r.receipt.blockHash == .transitions[1].blockHash
  and $r.receipt.status == "0x1"
  and $r.receipt.from == .actors.buyer
  and $r.receipt.to == .deployment.contract
  and ($r.receipt.logs | length == 2)
  and $r.receipt.logs[0].address == .deployment.asset
  and $r.receipt.logs[0].topics[0] == "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"
  and $r.receipt.logs[1].address == .deployment.contract
  and $r.receipt.logs[1].topics[0] == "0xd6edf0b889f4ff3b49ee288998e6efa15c9e6fcf822c49066e55723cd9164e8c"
  and $r.receipt.logs[1].topics[1] == .call.callId
  and $r.transaction.hash == .transitions[1].transactionHash
  and $r.transaction.from == .actors.buyer
  and $r.transaction.to == .deployment.contract
  and $r.transaction.input == .transitions[1].calldata
  and $r.transaction.value == "0x0"
  and $o.terminal.escrowBalanceAtomic == .terminalChecks.escrowUsdcAtomic
  and $o.terminal.buyerAllowanceAtomic == .terminalChecks.buyerAllowanceToEscrowAtomic
' "${evidence}" >/dev/null

printf 'two-RPC funded reference-signer evidence smoke passed; terminal escrow balance and allowance are zero\n'
