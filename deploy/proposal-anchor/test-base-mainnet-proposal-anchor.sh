#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
validator="${repo_root}/deploy/proposal-anchor/check-base-mainnet-proposal-anchor.sh"
canonical="${repo_root}/deployments/base-mainnet-proposal-anchor.json"
tmp_dir="$(mktemp -d)"
trap 'rm -rf "${tmp_dir}"' EXIT

expect_rejected() {
  local name="$1"
  local mutation="$2"
  local candidate="${tmp_dir}/${name}.json"
  jq "${mutation}" "${canonical}" >"${candidate}"
  if FLOWOPS_PROPOSAL_ANCHOR_RECORD="${candidate}" "${validator}" >/dev/null 2>&1; then
    printf 'proposal anchor validator accepted unsafe mutation: %s\n' "${name}" >&2
    exit 1
  fi
}

"${validator}" >/dev/null
expect_rejected deployed-status '.status = "experimental-deployed"'
expect_rejected contract-address '.contractAddress = "0x1111111111111111111111111111111111111111"'
expect_rejected transaction-hash '.transactionHash = ("0x" + ("1" * 64))'
expect_rejected source-verified '.sourceVerified = true'
expect_rejected broadcast-authorized '.broadcastAuthorized = true'
expect_rejected production-ready '.productionReady = true'
expect_rejected funding-enabled '.fundingEnabled = true'
expect_rejected vault-creation-enabled '.vaultCreationEnabled = true'
expect_rejected audit-completed '.externalAuditCompleted = true'
expect_rejected warning-removed '.warnings = []'
expect_rejected candidate-substituted '.candidateDeployer.address = "0x1111111111111111111111111111111111111111"'
expect_rejected candidate-made-production '.candidateDeployer.productionUseProhibited = false'
expect_rejected candidate-not-eoa '.candidateDeployer.observedCode = "0x01"'
expect_rejected candidate-latest-nonce '.candidateDeployer.observedLatestNonce = 1'
expect_rejected candidate-pending-nonce '.candidateDeployer.observedPendingNonce = 1'
expect_rejected candidate-address-drift '.candidateDeployer.expectedCreateAddressAtObservedNonce = "0x1111111111111111111111111111111111111111"'
expect_rejected candidate-observer-collapsed '.candidateDeployer.observers = ["mainnet.base.org", "mainnet.base.org"]'

printf 'proposal anchor validator rejected all unsafe deployment-record mutations\n'
