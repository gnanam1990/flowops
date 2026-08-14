#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
validator="${repo_root}/deploy/call-escrow/check-base-mainnet-readiness.sh"
smoke="${repo_root}/deploy/call-escrow/smoke-base-mainnet-readiness.sh"
canonical_record="${repo_root}/deployments/base-mainnet-readiness.json"
tmp_dir="$(mktemp -d)"
trap 'rm -rf "${tmp_dir}"' EXIT

bash -n "${validator}" "${smoke}"
FLOWOPS_MAINNET_READINESS_RECORD="${canonical_record}" "${validator}" >/dev/null

expect_smoke_rejected_before_network() {
  local name="$1"
  local primary="$2"
  local secondary="$3"

  if BASE_MAINNET_RPC_URL_PRIMARY="${primary}" BASE_MAINNET_RPC_URL_SECONDARY="${secondary}" \
    "${smoke}" >/dev/null 2>&1; then
    printf 'smoke accepted unsafe RPC configuration: %s\n' "${name}" >&2
    exit 1
  fi
}

expect_smoke_rejected_before_network duplicate-url 'https://mainnet.base.org' 'https://mainnet.base.org'
expect_smoke_rejected_before_network duplicate-host 'https://mainnet.base.org/one' 'https://mainnet.base.org/two'
expect_smoke_rejected_before_network duplicate-host-userinfo 'https://first@MAINNET.BASE.ORG/one' 'https://second@mainnet.base.org/two'
expect_smoke_rejected_before_network malformed-primary 'not-a-url' 'https://base-rpc.publicnode.com'

if grep -Eq -- '--rpc-url|--arg url' "${smoke}"; then
  printf 'smoke exposes credential-bearing RPC URLs as command arguments\n' >&2
  exit 1
fi

expect_rejected() {
  local name="$1"
  local mutation="$2"
  local mutated_record="${tmp_dir}/${name}.json"

  jq "${mutation}" "${canonical_record}" >"${mutated_record}"
  if FLOWOPS_MAINNET_READINESS_RECORD="${mutated_record}" "${validator}" >/dev/null 2>&1; then
    printf 'validator accepted unsafe mainnet readiness mutation: %s\n' "${name}" >&2
    exit 1
  fi
}

expect_rejected mainnet-approved '.mainnetApproved = true'
expect_rejected wrong-evidence-document '.evidenceDocument = "docs/evidence/not-real.md"'
expect_rejected broadcast-enabled '.broadcastEnabled = true'
expect_rejected invented-contract '.callEscrow.address = "0x1111111111111111111111111111111111111111"'
expect_rejected invented-transaction '.callEscrow.deploymentTransaction = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"'
expect_rejected invented-confirmation-depth '.callEscrow.minimumDeploymentConfirmations = 1'
expect_rejected designated-before-review '.gates.designatedDeployer = "0x1111111111111111111111111111111111111111"'
expect_rejected false-audit-claim '.gates.externalSecurityReview.complete = true'
expect_rejected ambiguous-review-digest '.gates.externalSecurityReview.reportDigestAlgorithm = "keccak256"'
expect_rejected missing-rpc-admission-code '.gates.productionRpcAdmissionImplemented = false'
expect_rejected false-production-rpc-selection '.gates.independentPaidRpcProviders = true'
expect_rejected missing-hardware-ceremony '.gates.hardwareWalletCeremonyImplemented = false'
expect_rejected missing-source-plan '.gates.sourceVerificationPlanImplemented = false'
expect_rejected false-source-approval '.gates.sourceVerificationPlanApproved = true'
expect_rejected funding-enabled '.pilot.fundingEnabled = true'
expect_rejected false-full-enforcement '.pilot.limitsEnforced = true'
expect_rejected profile-unselected '.pilot.profileSelected = false'
expect_rejected changed-per-call-limit '.pilot.maximumPerCallUsdc = "1.000001"'
expect_rejected changed-outstanding-limit '.pilot.maximumOutstandingUsdc = "10.000001"'
expect_rejected false-global-scope '.pilot.exposureScope = "global"'
expect_rejected optimistic-signer-accounting '.pilot.signerAccountingPosture = "settlement-released"'
expect_rejected false-escrow-enforcement '.pilot.escrowSignerEnforced = true'
expect_rejected missing-control-enforcement '.pilot.controlPlaneEnforced = false'
expect_rejected missing-direct-enforcement '.pilot.directUsdcSignerEnforced = false'
expect_rejected inexact-approval '.pilot.exactApprovalOnly = false'
expect_rejected wrong-contract '.callEscrow.contract = "contracts/src/CallEscrow.sol:SomethingElse"'
expect_rejected duplicate-rpc '.verification.rpcEvidence[1].url = .verification.rpcEvidence[0].url'
expect_rejected unexpected-rpc '.verification.rpcEvidence[1].url = "https://rpc.example"'
expect_rejected wrong-chain '.verification.rpcEvidence[0].chainId = 84532'
expect_rejected head-before-anchor '(.verification.canonicalAnchor.block - 1) as $head | .verification.rpcEvidence[0].observedHead = $head'
expect_rejected invalid-anchor-hash '.verification.canonicalAnchor.hash = "0x00"'
expect_rejected unexpected-runtime-hash '.canonicalUsdc.runtimeCodeHash = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" | .verification.rpcEvidence[].assetRuntimeCodeHash = .canonicalUsdc.runtimeCodeHash'
expect_rejected code-hash-disagreement '.verification.rpcEvidence[1].assetRuntimeCodeHash = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"'
expect_rejected production-rpc-claim '.verification.rpcEvidence[0].productionEligible = true'

printf 'mainnet readiness validator rejected all unsafe mutations\n'
