#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
record="${FLOWOPS_INTENT_ANCHOR_PROMOTION_RECORD:-${repo_root}/deployments/base-mainnet-intent-anchor-promotion.json}"
deployment_script="${repo_root}/contracts/script/DeployFlowOpsIntentAnchorBaseMainnet.s.sol"
cd "${repo_root}"

jq -e '
  .schemaVersion == 1
  and .kind == "flowops-intent-anchor-mainnet-promotion"
  and .network == "base-mainnet"
  and .chainId == 8453
  and (.status == "prepared-awaiting-funding-and-approval" or .status == "approved-zero-value")
  and (.sourceCommit | test("^[0-9a-f]{40}$"))
  and (.deployer | test("^0x[0-9a-fA-F]{40}$"))
  and (.expectedDeployerNonce | test("^(0|[1-9][0-9]*)$"))
  and (.expectedContractAddress | test("^0x[0-9a-fA-F]{40}$"))
  and (.initcodeKeccak256 | test("^0x[0-9a-f]{64}$"))
  and (.runtimeCodeKeccak256 | test("^0x[0-9a-f]{64}$"))
  and (.runtimeCodeSha256 | test("^0x[0-9a-f]{64}$"))
  and .observation.providerAgreement == true
  and .observation.latestNonce == .expectedDeployerNonce
  and .observation.pendingNonce == .expectedDeployerNonce
  and .observation.predictedAddressCode == "0x"
  and (.observation.estimatedDeploymentGas | test("^[1-9][0-9]*$"))
  and (.gasCeilings.maxGasLimit | test("^[1-9][0-9]*$"))
  and (.gasCeilings.maxFeePerGasWei | test("^[1-9][0-9]*$"))
  and (.gasCeilings.maxPriorityFeePerGasWei | test("^[0-9]+$"))
  and (.gasCeilings.maxGasSpendWei | test("^[1-9][0-9]*$"))
  and .gasCeilings.transactionValueWei == "0"
  and .fundingEnabled == false
  and .executesPayments == false
  and (
    if .status == "prepared-awaiting-funding-and-approval" then
      .approval.canonicalStatement == null
      and .approval.sha256 == null
      and .approval.approvedAt == null
    else
      (.approval.canonicalStatement | type == "string" and length > 0)
      and (.approval.sha256 | test("^0x[0-9a-f]{64}$"))
      and (.approval.approvedAt | test("^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$"))
    end
  )
' "${record}" >/dev/null

source_commit="$(jq -r '.sourceCommit' "${record}")"
deployer="$(jq -r '.deployer' "${record}")"
nonce="$(jq -r '.expectedDeployerNonce' "${record}")"
expected_address="$(jq -r '.expectedContractAddress' "${record}")"
expected_initcode_hash="$(jq -r '.initcodeKeccak256' "${record}")"
expected_runtime_hash="$(jq -r '.runtimeCodeKeccak256' "${record}")"
expected_runtime_sha="$(jq -r '.runtimeCodeSha256' "${record}")"
maximum_gas="$(jq -r '.gasCeilings.maxGasLimit' "${record}")"
maximum_fee="$(jq -r '.gasCeilings.maxFeePerGasWei' "${record}")"
maximum_priority="$(jq -r '.gasCeilings.maxPriorityFeePerGasWei' "${record}")"
maximum_spend="$(jq -r '.gasCeilings.maxGasSpendWei' "${record}")"

git cat-file -e "${source_commit}^{commit}"
git merge-base --is-ancestor "${source_commit}" HEAD
test "$(cast compute-address "${deployer}" --nonce "${nonce}" | awk '{print $NF}' | tr '[:upper:]' '[:lower:]')" = "$(tr '[:upper:]' '[:lower:]' <<<"${expected_address}")"

creation="$(forge inspect contracts/src/FlowOpsIntentAnchor.sol:FlowOpsIntentAnchor bytecode)"
runtime="$(forge inspect contracts/src/FlowOpsIntentAnchor.sol:FlowOpsIntentAnchor deployedBytecode)"
test "$(cast keccak "${creation}")" = "${expected_initcode_hash}"
test "$(cast keccak "${runtime}")" = "${expected_runtime_hash}"
test "$(printf '%s' "${runtime#0x}" | xxd -r -p | shasum -a 256 | awk '{print "0x"$1}')" = "${expected_runtime_sha}"

test "${maximum_gas}" -le 650000
test "${maximum_fee}" -le 20000000
test "${maximum_priority}" -le "${maximum_fee}"
test "$((maximum_gas * maximum_fee))" -le "${maximum_spend}"

source_text="$(tr '[:upper:]' '[:lower:]' <"${deployment_script}")"
grep -Fq "address public constant designated_deployer = $(tr '[:upper:]' '[:lower:]' <<<"${deployer}");" <<<"${source_text}"
grep -Fq "bytes20 public constant source_commit = hex\"${source_commit}\";" <<<"${source_text}"
grep -Fq "uint64 public constant expected_deployer_nonce = ${nonce};" <<<"${source_text}"
grep -Fq "address public constant expected_contract_address = $(tr '[:upper:]' '[:lower:]' <<<"${expected_address}");" <<<"${source_text}"
grep -Fq "${expected_initcode_hash};" <<<"${source_text}"
grep -Fq "${expected_runtime_hash};" <<<"${source_text}"

if test "$(jq -r '.status' "${record}")" = "approved-zero-value"; then
  approval="$(jq -r '.approval.sha256' "${record}")"
  grep -Fq "bytes32 public constant deployment_approval_digest = ${approval};" <<<"${source_text}"
  grep -Fq "bool public constant mainnet_broadcast_enabled = true;" <<<"${source_text}"
else
  grep -Fq "bytes32 public constant deployment_approval_digest = bytes32(0);" <<<"${source_text}"
  grep -Fq "bool public constant mainnet_broadcast_enabled = false;" <<<"${source_text}"
fi
