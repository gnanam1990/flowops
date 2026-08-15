#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
readiness="${FLOWOPS_MAINNET_AUDIT_READINESS_RECORD:-${repo_root}/deployments/base-mainnet-readiness.json}"
promotion="${FLOWOPS_MAINNET_AUDIT_PROMOTION_RECORD:-${repo_root}/deployments/base-mainnet-promotion.json}"
source_rehearsal="${FLOWOPS_MAINNET_AUDIT_SOURCE_RECORD:-${repo_root}/deployments/base-mainnet-source-rehearsal.json}"
review_manifest="${FLOWOPS_MAINNET_AUDIT_REVIEW_MANIFEST:-${repo_root}/security/call-escrow/review-manifest.json}"
mode="${1:---report}"

case "${mode}" in
  --report | --require-ready) ;;
  *)
    printf 'usage: %s [--report|--require-ready]\n' "$0" >&2
    exit 64
    ;;
esac

# These validators authenticate the four source records before this command
# joins them. Overrides exist only for mutation tests; the production broadcast
# wrapper clears every override before invoking --require-ready.
FLOWOPS_MAINNET_READINESS_RECORD="${readiness}" \
  "${repo_root}/deploy/call-escrow/check-base-mainnet-readiness.sh" >/dev/null
FLOWOPS_MAINNET_PROMOTION_RECORD="${promotion}" \
  "${repo_root}/deploy/call-escrow/check-base-mainnet-hardware-ceremony.sh" >/dev/null
FLOWOPS_MAINNET_SOURCE_REHEARSAL_RECORD="${source_rehearsal}" \
  "${repo_root}/deploy/call-escrow/check-base-mainnet-source-rehearsal.sh" >/dev/null
FLOWOPS_SECURITY_REVIEW_MANIFEST="${review_manifest}" \
  "${repo_root}/security/call-escrow/check-review-package.sh" >/dev/null

jq -e \
  --slurpfile readiness "${readiness}" \
  --slurpfile promotion "${promotion}" \
  --slurpfile source "${source_rehearsal}" \
  --slurpfile review "${review_manifest}" '
  ($readiness[0]) as $r
  | ($promotion[0]) as $p
  | ($source[0]) as $s
  | ($review[0]) as $v
  | $r.chainId == $p.chainId
  and $r.chainId == $s.chainId
  and $r.chainId == $v.chainId
  and $r.network == $p.network
  and $r.network == $s.network
  and $r.network == $v.network
  and $r.gates.externalSecurityReview.packageManifest == "security/call-escrow/review-manifest.json"
  and $r.gates.externalSecurityReview.reportDigest == $v.externalReview.reportSha256
  and $r.gates.externalSecurityReview.complete == $v.externalReview.complete
  and $p.externalReviewSha256 == $v.externalReview.reportSha256
  and $p.sourceVerification.rehearsalRecord == "deployments/base-mainnet-source-rehearsal.json"
  and $p.sourceVerification.approved == $s.sourceVerificationApproved
  and $r.gates.sourceVerificationPlanApproved == $s.sourceVerificationApproved
  and $r.gates.explicitBroadcastApproval == $p.broadcastApproval.approved
  and $r.gates.designatedDeployer == $p.signing.deployer
  and $r.gates.keyOwnershipDocumented == ($p.signing.ownershipAttestationSha256 != null)
  and $r.mainnetApproved == false
  and $r.broadcastEnabled == false
  and $r.pilot.fundingEnabled == false
  and $r.callEscrow.address == null
  and $p.status == "blocked-unassigned"
  and $s.status == "rehearsed-not-approved"
  and $v.packageStatus == "prepared-external-review-not-complete"
  ' -n >/dev/null

grep -Fq 'address public constant DESIGNATED_DEPLOYER = address(0);' \
  "${repo_root}/contracts/script/DeployCallEscrowBaseMainnet.s.sol"
grep -Fq 'bytes32 public constant EXTERNAL_REVIEW_DIGEST = bytes32(0);' \
  "${repo_root}/contracts/script/DeployCallEscrowBaseMainnet.s.sol"
grep -Fq 'bool public constant MAINNET_BROADCAST_ENABLED = false;' \
  "${repo_root}/contracts/script/DeployCallEscrowBaseMainnet.s.sol"
grep -Fq 'UNAUDITED: Base mainnet use is prohibited' \
  "${repo_root}/contracts/src/CallEscrow.sol"

report="$(jq -n \
  --arg checked_at "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" '
  {
    schemaVersion: 1,
    checkedAt: $checked_at,
    network: "base-mainnet",
    chainId: 8453,
    decision: "BLOCKED",
    deploymentAuthorized: false,
    fundingAuthorized: false,
    implementationEvidence: [
      "durable-escrow-reconciliation-implemented",
      "customer-escrow-fund-signer-implemented",
      "funded-sepolia-reference-signer-reconciled",
      "capped-pilot-limits-implemented",
      "production-rpc-admission-implemented",
      "hardware-wallet-ceremony-implemented",
      "source-verification-rehearsed",
      "external-review-package-prepared"
    ],
    blockers: [
      {id: "external-security-review", owner: "independent-reviewer", evidence: "security/call-escrow/review-manifest.json"},
      {id: "specialist-legal-review", owner: "legal-counsel", evidence: "deployments/base-mainnet-readiness.json"},
      {id: "production-hardware-deployer", owner: "operator", evidence: "deployments/base-mainnet-promotion.json"},
      {id: "key-ownership-and-recovery", owner: "operator", evidence: "deployments/base-mainnet-promotion.json"},
      {id: "independent-paid-rpc-quorum", owner: "operator", evidence: "deployments/base-mainnet-readiness.json"},
      {id: "production-reconciliation-admission", owner: "engineering-and-operator", evidence: "deployments/base-mainnet-readiness.json"},
      {id: "measured-confirmation-depth", owner: "engineering-and-operator", evidence: "deployments/base-mainnet-readiness.json"},
      {id: "source-verification-approval", owner: "reviewer", evidence: "deployments/base-mainnet-source-rehearsal.json"},
      {id: "funded-pilot-enforcement-evidence", owner: "engineering-and-operator", evidence: "deployments/base-mainnet-readiness.json"},
      {id: "explicit-zero-fund-broadcast-approval", owner: "human-approver", evidence: "deployments/base-mainnet-promotion.json"},
      {id: "reviewed-release-configuration", owner: "promotion-pr", evidence: "contracts/script/DeployCallEscrowBaseMainnet.s.sol"}
    ]
  }
')"

if [[ "${mode}" == "--require-ready" ]]; then
  jq -c '{decision, deploymentAuthorized, blockerCount: (.blockers | length), blockers: [.blockers[].id]}' \
    <<<"${report}" >&2
  exit 1
fi

jq . <<<"${report}"
