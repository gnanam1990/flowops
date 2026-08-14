#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
validator="${repo_root}/deploy/call-escrow/check-base-mainnet-readiness.sh"
canonical_record="${repo_root}/deployments/base-mainnet-readiness.json"
tmp_dir="$(mktemp -d)"
trap 'rm -rf "${tmp_dir}"' EXIT

bash -n "${validator}" "${repo_root}/deploy/call-escrow/smoke-base-mainnet-readiness.sh"
FLOWOPS_MAINNET_READINESS_RECORD="${canonical_record}" "${validator}" >/dev/null

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
expect_rejected designated-before-review '.gates.designatedDeployer = "0x1111111111111111111111111111111111111111"'
expect_rejected false-audit-claim '.gates.externalSecurityReview.complete = true'
expect_rejected ambiguous-review-digest '.gates.externalSecurityReview.reportDigestAlgorithm = "keccak256"'
expect_rejected funding-enabled '.pilot.fundingEnabled = true'
expect_rejected invented-limit '.pilot.maximumPerCallUsdc = "1.000000"'
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
