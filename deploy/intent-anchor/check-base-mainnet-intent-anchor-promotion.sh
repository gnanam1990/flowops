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
  and (
    .status == "prepared-awaiting-funding-and-approval"
    or .status == "approval-requested"
    or .status == "approved-zero-value"
    or .status == "attempt-failed-no-broadcast"
  )
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
    elif .status == "approval-requested" then
      (.approval.canonicalStatement | type == "string" and length > 0)
      and (.approval.sha256 | test("^0x[0-9a-f]{64}$"))
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
status="$(jq -r '.status' "${record}")"

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

if test "${status}" != "prepared-awaiting-funding-and-approval"; then
  approval_statement="$(jq -r '.approval.canonicalStatement' "${record}")"
  approval_digest="$(jq -r '.approval.sha256' "${record}")"
  test "$(printf '%s' "${approval_statement}" | shasum -a 256 | awk '{print "0x"$1}')" = "${approval_digest}"
  jq -e \
    --arg deployer "${deployer}" \
    --arg nonce "${nonce}" \
    --arg address "${expected_address}" \
    --arg source "${source_commit}" \
    --arg initcode "${expected_initcode_hash}" \
    --arg runtime "${expected_runtime_hash}" \
    --arg gas "${maximum_gas}" \
    --arg fee "${maximum_fee}" \
    --arg priority "${maximum_priority}" \
    --arg spend "${maximum_spend}" \
    --arg status "${status}" '
      .action == "deploy-contract"
      and .browserWalletPrompt == true
      and .chainId == 8453
      and .contract == "FlowOpsIntentAnchor"
      and .deployer == $deployer
      and .executesPayments == false
      and .expectedAddress == $address
      and .expectedNonce == $nonce
      and .fundingEnabled == false
      and .initcodeKeccak256 == $initcode
      and .maxFeePerGasWei == $fee
      and .maxGasLimit == $gas
      and .maxGasSpendWei == $spend
      and .maxPriorityFeePerGasWei == $priority
      and .network == "base-mainnet"
      and .noTokenApproval == true
      and .runtimeCodeKeccak256 == $runtime
      and .scope == "one-zero-value-contract-deployment"
      and .sourceCommit == $source
      and .transactionValueWei == "0"
      and .version == 1
      and (
        if $status == "approval-requested" or $status == "approved-zero-value"
          or $status == "attempt-failed-no-broadcast" then
          .ceremonyAttempt == 2
          and .previousApprovalDigest == "0x50791fe87170a29c24b19571325a6c8596a115170145866b0c61d8a2ce14521b"
          and .previousAttemptOutcome == "failed-no-broadcast-wallet-chain-mismatch"
          and .requiredWalletChainId == 8453
        else true
        end
      )
    ' <<<"${approval_statement}" >/dev/null
fi

if test "${status}" = "approval-requested" || test "${status}" = "approved-zero-value" \
  || test "${status}" = "attempt-failed-no-broadcast"; then
  jq -e '
    .previousAttempts | length == 1
    and .[0].ceremonyAttempt == 1
    and .[0].approval.sha256 == "0x50791fe87170a29c24b19571325a6c8596a115170145866b0c61d8a2ce14521b"
    and .[0].deploymentEvidence.approvalConsumed == true
    and .[0].deploymentEvidence.connectedWalletChainId == 1
    and .[0].deploymentEvidence.expectedChainId == 8453
    and .[0].deploymentEvidence.failure == "wallet-chain-mismatch"
    and .[0].deploymentEvidence.transactionHash == null
    and .[0].deploymentEvidence.receipt == null
    and .[0].deploymentEvidence.postAttemptLatestNonce == "0"
    and .[0].deploymentEvidence.postAttemptPendingNonce == "0"
    and .[0].deploymentEvidence.postAttemptPredictedAddressCode == "0x"
  ' "${record}" >/dev/null
fi

if test "${status}" = "attempt-failed-no-broadcast"; then
  jq -e '
    .deploymentEvidence.approvalConsumed == true
    and .deploymentEvidence.connectedWallet == .deployer
    and .deploymentEvidence.connectedWalletChainId == 1
    and .deploymentEvidence.expectedChainId == 8453
    and .deploymentEvidence.failure == "wallet-chain-mismatch"
    and .deploymentEvidence.transactionHash == null
    and .deploymentEvidence.receipt == null
    and .deploymentEvidence.postAttemptLatestNonce == .expectedDeployerNonce
    and .deploymentEvidence.postAttemptPendingNonce == .expectedDeployerNonce
    and .deploymentEvidence.postAttemptPredictedAddressCode == "0x"
    and .deploymentEvidence.providerAgreement == true
  ' "${record}" >/dev/null
fi

source_text="$(tr '[:upper:]' '[:lower:]' <"${deployment_script}")"
grep -Fq "address public constant designated_deployer = $(tr '[:upper:]' '[:lower:]' <<<"${deployer}");" <<<"${source_text}"
grep -Fq "bytes20 public constant source_commit = hex\"${source_commit}\";" <<<"${source_text}"
grep -Fq "uint64 public constant expected_deployer_nonce = ${nonce};" <<<"${source_text}"
grep -Fq "address public constant expected_contract_address = $(tr '[:upper:]' '[:lower:]' <<<"${expected_address}");" <<<"${source_text}"
grep -Fq "${expected_initcode_hash};" <<<"${source_text}"
grep -Fq "${expected_runtime_hash};" <<<"${source_text}"

if test "${status}" = "approved-zero-value"; then
  approval="$(jq -r '.approval.sha256' "${record}")"
  grep -Fq "bytes32 public constant deployment_approval_digest =" <<<"${source_text}"
  grep -Fq "${approval};" <<<"${source_text}"
  grep -Fq "bool public constant mainnet_broadcast_enabled = true;" <<<"${source_text}"
elif test "${status}" = "attempt-failed-no-broadcast"; then
  approval="$(jq -r '.approval.sha256' "${record}")"
  grep -Fq "bytes32 public constant deployment_approval_digest =" <<<"${source_text}"
  grep -Fq "${approval};" <<<"${source_text}"
  grep -Fq "bool public constant mainnet_broadcast_enabled = false;" <<<"${source_text}"
else
  grep -Fq "bytes32 public constant deployment_approval_digest = bytes32(0);" <<<"${source_text}"
  grep -Fq "bool public constant mainnet_broadcast_enabled = false;" <<<"${source_text}"
fi
