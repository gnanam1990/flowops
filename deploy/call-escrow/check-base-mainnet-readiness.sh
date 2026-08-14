#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
record="${FLOWOPS_MAINNET_READINESS_RECORD:-${repo_root}/deployments/base-mainnet-readiness.json}"

jq -e '
  .schemaVersion == 1
  and .network == "base-mainnet"
  and .chainId == 8453
  and .evidenceDocument == "docs/evidence/BASE_MAINNET_READINESS_2026-08-14.md"
  and .status == "blocked-no-deployment"
  and .mainnetApproved == false
  and .broadcastEnabled == false
  and (.canonicalUsdc.address | ascii_downcase) == "0x833589fcd6edb6e08f4c7c32d4f71b54bda02913"
  and .canonicalUsdc.source == "https://developers.circle.com/stablecoins/usdc-contract-addresses"
  and .canonicalUsdc.symbol == "USDC"
  and .canonicalUsdc.decimals == 6
  and .canonicalUsdc.runtimeCodeHash == "0xa6705a10bb756b5dea144591118be77d7af0c3eee3bf2dfe2583dcb0364fefab"
  and .callEscrow.address == null
  and .callEscrow.deploymentTransaction == null
  and .callEscrow.deploymentBlock == null
  and .callEscrow.reviewedSourceCommit == null
  and .callEscrow.optimisticReleaseWindowSeconds == 3600
  and .callEscrow.compiler == "0.8.26"
  and .callEscrow.optimizerRuns == 200
  and .callEscrow.evmVersion == "cancun"
  and .callEscrow.contract == "contracts/src/CallEscrow.sol:CallEscrow"
  and .gates.designatedDeployer == null
  and .gates.keyOwnershipDocumented == false
  and .gates.externalSecurityReview.complete == false
  and .gates.externalSecurityReview.reportDigestAlgorithm == "sha256"
  and .gates.externalSecurityReview.reportDigest == null
  and .gates.legalReviewComplete == false
  and .gates.durableEscrowReconciliation == false
  and .gates.independentPaidRpcProviders == false
  and .gates.referenceSignerFundedSepoliaProof == false
  and .gates.sourceVerificationPlanApproved == false
  and .gates.explicitBroadcastApproval == false
  and .pilot.fundingEnabled == false
  and .pilot.limitsEnforced == false
  and .pilot.maximumPerCallUsdc == null
  and .pilot.maximumOutstandingUsdc == null
  and .pilot.exactApprovalOnly == true
  and (.verification.verifiedAt | test("^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$"))
  and (.verification.canonicalAnchor.block | type == "number" and . > 0)
  and (.verification.canonicalAnchor.hash | test("^0x[0-9a-f]{64}$"))
  and (.verification.rpcEvidence | length >= 2)
  and (.verification.rpcEvidence | map(.url) | sort == ["https://base-rpc.publicnode.com", "https://mainnet.base.org"])
  and (.verification.rpcEvidence | all(.chainId == 8453))
  and (.verification.canonicalAnchor.block as $anchor | .verification.rpcEvidence | all(.observedHead >= $anchor))
  and (.canonicalUsdc.runtimeCodeHash as $hash | .verification.rpcEvidence | all(.assetRuntimeCodeHash == $hash))
  and (.verification.rpcEvidence | all(.productionEligible == false))
' "${record}" >/dev/null

contract_identifier="$(jq -r '.callEscrow.contract' "${record}")"
source_path="${contract_identifier%%:*}"
test -f "${repo_root}/${source_path}"
test -f "${repo_root}/$(jq -r '.evidenceDocument' "${record}")"

foundry_config="$(forge config --json)"
test "$(jq -r '.solc' <<<"${foundry_config}")" = "$(jq -r '.callEscrow.compiler' "${record}")"
test "$(jq -r '.evm_version' <<<"${foundry_config}")" = "$(jq -r '.callEscrow.evmVersion' "${record}")"
test "$(jq -r '.optimizer_runs' <<<"${foundry_config}")" = "$(jq -r '.callEscrow.optimizerRuns' "${record}")"

printf 'validated blocked Base mainnet readiness record; no deployment authorized\n'
