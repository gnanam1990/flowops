#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
record="${FLOWOPS_ASCP_SEPOLIA_ACTIVATION_RECORD:-${repo_root}/deployments/base-sepolia-ascp-activation-v1.json}"
deployment_record="${repo_root}/deployments/base-sepolia-ascp-v4.json"

jq -e --slurpfile deployment "${deployment_record}" '
  def address: type == "string" and test("^0x[0-9a-f]{40}$") and . != "0x0000000000000000000000000000000000000000";
  def digest: type == "string" and test("^0x[0-9a-f]{64}$") and . != "0x0000000000000000000000000000000000000000000000000000000000000000";
  def timestamp: type == "string" and test("^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$");
  $deployment[0] as $d
  | . as $activation
  | ($d.contracts | map({key: .name, value: .address}) | from_entries) as $contracts
  | .schemaVersion == 1
  and .activationId == "ascp-v4-base-sepolia-activation-v1-2026-08-26"
  and .network == "base-sepolia"
  and .chainId == 84532
  and .status == "executed-verified-funding-disabled"
  and .sourceDeployment == {
    record: "deployments/base-sepolia-ascp-v4.json",
    releaseId: $d.releaseId,
    sourceCommit: $d.sourceCommit
  }
  and .safe.address == $d.safe.address
  and .safe.threshold == $d.safe.threshold
  and (.safe.safeTxHash | digest)
  and .safe.nonceBefore == 0
  and .safe.nonceAfter == 1
  and (.safe.confirmedOwners | length == $activation.safe.threshold and all(address) and (unique | length == $activation.safe.threshold))
  and ([.safe.confirmedOwners[] as $owner | $d.safe.owners | index($owner)] | all(. != null))
  and .contracts == {
    serviceDirectory: $contracts.service_directory,
    agentRegistry: $contracts.agent_registry,
    escrow: $contracts.ascp_call_escrow,
    spendModule: $contracts.ascp_spend_module,
    asset: $d.asset.address
  }
  and .safeTransaction.to == "0x9641d764fc13c8b624c04430c7356c1c7c8102e2"
  and .safeTransaction.value == "0"
  and .safeTransaction.operation == 1
  and .safeTransaction.safeTxGas == "0"
  and .safeTransaction.baseGas == "0"
  and .safeTransaction.gasPrice == "0"
  and .safeTransaction.gasToken == "0x0000000000000000000000000000000000000000"
  and .safeTransaction.refundReceiver == "0x0000000000000000000000000000000000000000"
  and .safeTransaction.nonce == .safe.nonceBefore
  and (.safeTransaction.data | test("^0x8d80ff0a[0-9a-f]+$") and ((length - 2) % 2 == 0))
  and (.safeTransaction.dataHash | digest)
  and (.actions | length == 2)
  and .actions[0].order == 1
  and .actions[0].to == .contracts.spendModule
  and .actions[0].value == "0"
  and .actions[0].operation == "CALL"
  and .actions[0].method == "setEscrowAllowlist(address,bytes32,bytes32,bytes32)"
  and .actions[0].escrow == .contracts.escrow
  and (.actions[0].escrowRuntimeCodeHash | digest)
  and (.actions[0].workflowId | digest)
  and (.actions[0].workflowPayloadHash | digest)
  and (.actions[0].data | test("^0x7a22532a[0-9a-f]{256}$"))
  and .actions[1] == {
    order: 2,
    to: .safe.address,
    value: "0",
    operation: "CALL",
    method: "enableModule(address)",
    module: .contracts.spendModule,
    data: ("0x610b5925000000000000000000000000" + (.contracts.spendModule | ltrimstr("0x")))
  }
  and (.execution.transactionHash | digest)
  and (.execution.outerFrom | address)
  and (.execution.outerTo | address)
  and (.execution.outerNonce | type == "number" and . >= 0)
  and (.execution.outerInputHash | digest)
  and .execution.transactionType == 4
  and .execution.receiptStatus == true
  and (.execution.blockNumber | type == "number" and . > ($d.contracts | map(.deploymentBlock) | max))
  and (.execution.blockHash | digest)
  and (.execution.blockTimestamp | timestamp)
  and (.execution.transactionIndex | type == "number" and . >= 0)
  and (.execution.gasUsed | type == "number" and . > 0)
  and (.execution.effectiveGasPriceWei | test("^[0-9]+$") and (tonumber > 0))
  and (.execution.l1FeeWei | test("^[0-9]+$") and (tonumber >= 0))
  and .eventEvidence == {
    executionSuccessTopic: "0x442e715f626346e8c54381002da614f62bee8d27386535b2521ec8540898556e",
    enabledModuleTopic: "0xecdf3a3effea5783a3c4c2140e677577666428d44ed9d474a0b3a4c9943f8440",
    escrowAllowlistSetTopic: "0x02b8c7e709e3f27c20a4ecb3669d2682fcba9309e1902881bf1814c71b9f6eb3",
    governanceWorkflowBoundTopic: "0x71840a8df3cf7e14c302ff72b4fd1c651a2845389dfb0a4fdd884a2ffb104bfe"
  }
  and .postState == {
    safeNonce: .safe.nonceAfter,
    spendModuleEnabled: true,
    escrowAllowlistCodeHash: .actions[0].escrowRuntimeCodeHash,
    directoryVersion: 0,
    directoryRoot: "0x0000000000000000000000000000000000000000000000000000000000000000",
    agentCount: 0,
    escrowTotalLocked: "0",
    spendExecutedPrincipal: "0",
    callEscrowEmergencyPaused: false,
    spendModuleEmergencyPaused: false,
    safeNativeBalanceWei: "0",
    safeUsdcBalance: "0",
    allContractNativeBalancesWei: "0",
    allContractUsdcBalances: "0",
    safeToSpendModuleUsdcAllowance: "0",
    safeToCallEscrowUsdcAllowance: "0",
    runtimeEnabled: true,
    fundingEnabled: false
  }
  and .verification.providersAgreed == true
  and (.verification.verifiedAt | timestamp)
  and (.verification.rpcEvidence | length >= 2)
  and (.verification.rpcEvidence | map(.url) | unique | length >= 2)
  and (.verification.rpcEvidence | all(.observedHead >= $activation.execution.blockNumber))
  and (.verification.safeTransactionUrl | startswith("https://app.safe.global/transactions/tx?"))
  and (.verification.safeTransactionUrl | contains($activation.safe.safeTxHash))
  and .verification.safeTransactionServiceUrl == ("https://api.safe.global/tx-service/basesep/api/v1/multisig-transactions/" + .safe.safeTxHash + "/")
  and .verification.baseScanTransactionUrl == ("https://sepolia.basescan.org/tx/" + .execution.transactionHash)
  and (.verification.tenderlySimulationUrl | startswith("https://dashboard.tenderly.co/public/safe/"))
  and .mainnetApproved == false
' "${record}" >/dev/null

source_commit="$(jq -er '.sourceDeployment.sourceCommit' "${record}")"
git -C "${repo_root}" cat-file -e "${source_commit}^{commit}"
git -C "${repo_root}" merge-base --is-ancestor "${source_commit}" HEAD

safe_data="$(jq -er '.safeTransaction.data' "${record}")"
test "$(printf '%s' "${safe_data}" | cast keccak)" = "$(jq -er '.safeTransaction.dataHash' "${record}")"

action_one_to="$(jq -er '.actions[0].to' "${record}")"
action_one_data="$(jq -er '.actions[0].data' "${record}")"
action_two_to="$(jq -er '.actions[1].to' "${record}")"
action_two_data="$(jq -er '.actions[1].data' "${record}")"
test "${action_one_data:0:10}" = "$(cast sig 'setEscrowAllowlist(address,bytes32,bytes32,bytes32)')"
test "${action_two_data:0:10}" = "$(cast sig 'enableModule(address)')"
test "${action_one_data}" = "$(cast calldata \
  'setEscrowAllowlist(address,bytes32,bytes32,bytes32)' \
  "$(jq -er '.actions[0].escrow' "${record}")" \
  "$(jq -er '.actions[0].escrowRuntimeCodeHash' "${record}")" \
  "$(jq -er '.actions[0].workflowId' "${record}")" \
  "$(jq -er '.actions[0].workflowPayloadHash' "${record}")")"
test "${action_two_data}" = "$(cast calldata 'enableModule(address)' "$(jq -er '.actions[1].module' "${record}")")"

encode_action() {
  local target="$1"
  local data="$2"
  local data_hex="${data#0x}"
  local data_length=$(( ${#data_hex} / 2 ))
  printf '00%s%064x%064x%s' "${target#0x}" 0 "${data_length}" "${data_hex}"
}

packed="$(encode_action "${action_one_to}" "${action_one_data}")$(encode_action "${action_two_to}" "${action_two_data}")"
test "$(cast calldata 'multiSend(bytes)' "0x${packed}")" = "${safe_data}"

printf 'validated ASCP v4 Base Sepolia activation evidence %s\n' "$(jq -er '.activationId' "${record}")"
