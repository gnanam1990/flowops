#!/usr/bin/env bash
set -euo pipefail

# This is the only supported mainnet broadcast wrapper. The committed records
# deliberately make it refuse today. A future reviewed promotion PR must fill
# every gate before a human confirms the transaction on a Ledger or Trezor.
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
record="${repo_root}/deployments/base-mainnet-promotion.json"
readiness="${repo_root}/deployments/base-mainnet-readiness.json"
cd "${repo_root}"

: "${FLOWOPS_HARDWARE_WALLET:?set FLOWOPS_HARDWARE_WALLET to ledger or trezor}"
: "${FLOWOPS_EXPLICIT_BROADCAST_APPROVAL_SHA256:?load the fresh approval digest}"
: "${BASE_MAINNET_RPC_URL_PRIMARY:?load the primary production RPC secret}"
: "${BASE_MAINNET_RPC_URL_SECONDARY:?load the secondary production RPC secret}"
: "${FLOWOPS_BASE_RPC_PROVIDERS_JSON:?load the production provider secret JSON}"
: "${FLOWOPS_BASE_RPC_ADMISSION_JSON:?load the reviewed production provider admission JSON}"

approved_at="$(jq -er '.broadcastApproval.approvedAt' "${record}")"
expires_at="$(jq -er '.broadcastApproval.expiresAt' "${record}")"
parse_epoch() {
  date -u -d "$1" '+%s' 2>/dev/null || date -u -j -f '%Y-%m-%dT%H:%M:%SZ' "$1" '+%s'
}
approved_epoch="$(parse_epoch "${approved_at}")"
expires_epoch="$(parse_epoch "${expires_at}")"
now_epoch="$(date -u '+%s')"

jq -e \
  --arg wallet "${FLOWOPS_HARDWARE_WALLET}" \
  --arg approval "${FLOWOPS_EXPLICIT_BROADCAST_APPROVAL_SHA256}" '
  .schemaVersion == 1
  and .network == "base-mainnet"
  and .chainId == 8453
  and .status == "approved-zero-fund"
  and (.reviewedSourceCommit | test("^[0-9a-f]{40}$"))
  and .signing.walletType == $wallet
  and ($wallet == "ledger" or $wallet == "trezor")
  and (.signing.derivationPath | test("^m(/[0-9]+[hH\x27]?)+$"))
  and (.signing.deployer | test("^0x[0-9a-f]{40}$"))
  and .signing.deployer != "0x079bdde909e28e437768a06d7001eb40896668d4"
  and .signing.deployer != "0xc2f0967c4df966636e4ac1dad40abda65536cbb6"
  and (.signing.ownershipAttestationSha256 | test("^[0-9a-f]{64}$"))
  and .signing.recoveryRunbookApproved == true
  and (.externalReviewSha256 | test("^[0-9a-f]{64}$"))
  and .sourceVerification.rehearsalRecord == "deployments/base-mainnet-source-rehearsal.json"
  and .sourceVerification.approved == true
  and .broadcastApproval.approved == true
  and .broadcastApproval.approvalSha256 == $approval
  and (.broadcastApproval.approvalSha256 | test("^[0-9a-f]{64}$"))
  and (.broadcastApproval.approvedAt | test("^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$"))
  and (.broadcastApproval.expiresAt | test("^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$"))
  and (.broadcastApproval.expectedDeployerNonce | test("^(0|[1-9][0-9]*)$"))
  and (.broadcastApproval.maximumGasLimit | test("^[1-9][0-9]*$"))
  and (.broadcastApproval.maximumFeePerGasWei | test("^[1-9][0-9]*$"))
  and (.broadcastApproval.maximumPriorityFeePerGasWei | test("^[0-9]+$"))
  and (.broadcastApproval.maximumTotalGasWei | test("^[1-9][0-9]*$"))
' "${record}" >/dev/null

test "${approved_epoch}" -lt "${expires_epoch}"
test "$((expires_epoch - approved_epoch))" -le 3600
test "${now_epoch}" -ge "${approved_epoch}"
test "${now_epoch}" -lt "${expires_epoch}"
test "$(git -C "${repo_root}" rev-parse HEAD)" = "$(jq -r '.reviewedSourceCommit' "${record}")"
test -z "$(git -C "${repo_root}" status --short)"

deployer="$(jq -r '.signing.deployer' "${record}")"
derivation_path="$(jq -r '.signing.derivationPath' "${record}")"
review_digest="$(jq -r '.externalReviewSha256' "${record}")"
security_manifest="${repo_root}/security/call-escrow/review-manifest.json"

env -u FLOWOPS_SECURITY_REVIEW_MANIFEST \
  "${repo_root}/security/call-escrow/check-review-package.sh" >/dev/null
jq -e --arg review_digest "${review_digest}" '
  .externalReview.complete == true
  and .externalReview.reportSha256 == $review_digest
  and .externalReview.retestComplete == true
  and .externalReview.unresolvedCritical == 0
  and .externalReview.unresolvedHigh == 0
' "${security_manifest}" >/dev/null

jq -e \
  --arg deployer "${deployer}" \
  --arg review_digest "${review_digest}" \
  --arg reviewed_commit "$(git -C "${repo_root}" rev-parse HEAD)" '
  .status == "approved-zero-fund"
  and .mainnetApproved == true
  and .broadcastEnabled == true
  and (.canonicalUsdc.address | ascii_downcase) == "0x833589fcd6edb6e08f4c7c32d4f71b54bda02913"
  and .callEscrow.address == null
  and .callEscrow.deploymentTransaction == null
  and .callEscrow.deploymentBlock == null
  and .callEscrow.reviewedSourceCommit == $reviewed_commit
  and (.callEscrow.minimumDeploymentConfirmations | type == "number" and floor == . and . > 0 and . <= 10000)
  and (.gates.designatedDeployer | ascii_downcase) == $deployer
  and .gates.keyOwnershipDocumented == true
  and .gates.externalSecurityReview.packagePrepared == true
  and .gates.externalSecurityReview.packageManifest == "security/call-escrow/review-manifest.json"
  and .gates.externalSecurityReview.complete == true
  and .gates.externalSecurityReview.reportDigestAlgorithm == "sha256"
  and .gates.externalSecurityReview.reportDigest == $review_digest
  and .gates.legalReviewComplete == true
  and .gates.durableEscrowReconciliation == true
  and .gates.productionRpcAdmissionImplemented == true
  and .gates.independentPaidRpcProviders == true
  and .gates.referenceSignerFundedSepoliaProof == true
  and .gates.hardwareWalletCeremonyImplemented == true
  and .gates.sourceVerificationPlanImplemented == true
  and .gates.sourceVerificationPlanApproved == true
  and .gates.explicitBroadcastApproval == true
  and .pilot.fundingEnabled == false
  and .pilot.profileSelected == true
  and .pilot.limitsEnforced == true
  and .pilot.controlPlaneEnforced == true
  and .pilot.directUsdcSignerEnforced == true
  and .pilot.escrowSignerEnforced == true
  and .pilot.exactApprovalOnly == true
' "${readiness}" >/dev/null

maximum_gas_limit="$(jq -r '.broadcastApproval.maximumGasLimit' "${record}")"
maximum_fee_per_gas="$(jq -r '.broadcastApproval.maximumFeePerGasWei' "${record}")"
maximum_priority_fee="$(jq -r '.broadcastApproval.maximumPriorityFeePerGasWei' "${record}")"
maximum_total_gas="$(jq -r '.broadcastApproval.maximumTotalGasWei' "${record}")"
expected_nonce="$(jq -r '.broadcastApproval.expectedDeployerNonce' "${record}")"
test "${maximum_gas_limit}" -le 30000000
test "${maximum_fee_per_gas}" -le 100000000000
test "${maximum_priority_fee}" -le 100000000000
test "${maximum_total_gas}" -le 9000000000000000000
test "${maximum_priority_fee}" -le "${maximum_fee_per_gas}"
test "$((maximum_gas_limit * maximum_fee_per_gas))" -le "${maximum_total_gas}"
source_text="$(tr '[:upper:]' '[:lower:]' <"${repo_root}/contracts/script/DeployCallEscrowBaseMainnet.s.sol")"
grep -Fq "address public constant designated_deployer = ${deployer};" <<<"${source_text}"
grep -Fq "bytes32 public constant external_review_digest = 0x${review_digest};" <<<"${source_text}"
grep -Fq 'bool public constant mainnet_broadcast_enabled = true;' <<<"${source_text}"

FLOWOPS_BASE_CHAIN_ID=8453 go run "${repo_root}/cmd/rpc-admission-check" >/dev/null
env -u FLOWOPS_MAINNET_SOURCE_REHEARSAL_RECORD \
  "${repo_root}/deploy/call-escrow/check-base-mainnet-source-rehearsal.sh" >/dev/null
"${repo_root}/deploy/call-escrow/smoke-base-mainnet-readiness.sh" >/dev/null

require_approved_nonce() {
  local rpc_url raw_latest raw_pending
  rpc_url="$1"
  raw_latest="$(ETH_RPC_URL="${rpc_url}" cast nonce "${deployer}" --block latest)"
  raw_pending="$(ETH_RPC_URL="${rpc_url}" cast nonce "${deployer}" --block pending)"
  test "$(cast to-dec "${raw_latest}")" = "${expected_nonce}"
  test "$(cast to-dec "${raw_pending}")" = "${expected_nonce}"
}

require_approved_nonce "${BASE_MAINNET_RPC_URL_PRIMARY}"
require_approved_nonce "${BASE_MAINNET_RPC_URL_SECONDARY}"

simulation="$(FOUNDRY_ETH_RPC_URL="${BASE_MAINNET_RPC_URL_PRIMARY}" forge script \
  contracts/script/DeployCallEscrowBaseMainnet.s.sol:DeployCallEscrowBaseMainnet \
  --sender "${deployer}" \
  --gas-estimate-multiplier 130 \
  --json)"
jq -s -e --argjson maximum "${maximum_gas_limit}" '
  ([.[] | select(has("success"))] | length == 1)
  and ([.[] | select(has("estimated_total_gas_used"))] | length == 1)
  and (
    [.[] | select(has("success"))][0]
    | .success == true
  )
  and (
    [.[] | select(has("estimated_total_gas_used"))][0]
    | .chain == 8453
    and (.estimated_total_gas_used | tonumber) <= $maximum
  )
' <<<"${simulation}" >/dev/null

require_approved_nonce "${BASE_MAINNET_RPC_URL_PRIMARY}"
require_approved_nonce "${BASE_MAINNET_RPC_URL_SECONDARY}"

# Burn this approval before the hardware prompt. An aborted or ambiguous
# attempt must be investigated and receive a new approval digest; this wrapper
# has no retry or resume path.
attempt_dir="$(git -C "${repo_root}" rev-parse --git-path flowops-mainnet-ceremony)"
attempt_file="${attempt_dir}/${FLOWOPS_EXPLICIT_BROADCAST_APPROVAL_SHA256}.attempted"
mkdir -p "${attempt_dir}"
umask 077
if ! (set -o noclobber; printf '%s\t%s\t%s\t%s\n' \
  "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" \
  "$(git -C "${repo_root}" rev-parse HEAD)" \
  "${deployer}" \
  "${expected_nonce}" >"${attempt_file}") 2>/dev/null; then
  printf 'broadcast approval was already attempted; investigate chain state and issue a new approval\n' >&2
  exit 1
fi

wallet_flag="--${FLOWOPS_HARDWARE_WALLET}"
FOUNDRY_ETH_RPC_URL="${BASE_MAINNET_RPC_URL_PRIMARY}" forge script \
  contracts/script/DeployCallEscrowBaseMainnet.s.sol:DeployCallEscrowBaseMainnet \
  "${wallet_flag}" \
  --mnemonic-derivation-paths "${derivation_path}" \
  --sender "${deployer}" \
  --gas-estimate-multiplier 130 \
  --with-gas-price "${maximum_fee_per_gas}" \
  --priority-gas-price "${maximum_priority_fee}" \
  --broadcast
