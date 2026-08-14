#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
record="${FLOWOPS_MAINNET_PROMOTION_RECORD:-${repo_root}/deployments/base-mainnet-promotion.json}"

jq -e '
  .schemaVersion == 1
  and .network == "base-mainnet"
  and .chainId == 8453
  and .status == "blocked-unassigned"
  and .reviewedSourceCommit == null
  and .signing.walletType == null
  and .signing.derivationPath == null
  and .signing.deployer == null
  and .signing.ownershipAttestationSha256 == null
  and .signing.recoveryRunbookApproved == false
  and .externalReviewSha256 == null
  and .sourceVerification.rehearsalRecord == "deployments/base-mainnet-source-rehearsal.json"
  and .sourceVerification.approved == false
  and .broadcastApproval.approved == false
  and .broadcastApproval.approvalSha256 == null
  and .broadcastApproval.approvedAt == null
  and .broadcastApproval.expiresAt == null
  and .broadcastApproval.expectedDeployerNonce == null
  and .broadcastApproval.maximumGasLimit == null
  and .broadcastApproval.maximumFeePerGasWei == null
  and .broadcastApproval.maximumPriorityFeePerGasWei == null
  and .broadcastApproval.maximumTotalGasWei == null
' "${record}" >/dev/null

test -f "${repo_root}/$(jq -r '.sourceVerification.rehearsalRecord' "${record}")"
printf 'validated blocked hardware-wallet ceremony record; no deployer or broadcast is authorized\n'
