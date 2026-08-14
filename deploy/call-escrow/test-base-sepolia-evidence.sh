#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
validator="${repo_root}/deploy/call-escrow/check-base-sepolia-evidence.sh"
canonical_record="${repo_root}/deployments/base-sepolia.json"
tmp_dir="$(mktemp -d)"
trap 'rm -rf "${tmp_dir}"' EXIT

FLOWOPS_DEPLOYMENT_RECORD="${canonical_record}" "${validator}" >/dev/null

expect_rejected() {
  local name="$1"
  local mutation="$2"
  local mutated_record="${tmp_dir}/${name}.json"

  jq "${mutation}" "${canonical_record}" >"${mutated_record}"
  if FLOWOPS_DEPLOYMENT_RECORD="${mutated_record}" "${validator}" >/dev/null 2>&1; then
    printf 'validator accepted invalid evidence mutation: %s\n' "${name}" >&2
    exit 1
  fi
}

expect_rejected runtime-code-hash '.callEscrow.runtimeCodeHash = "0x00"'
expect_rejected mainnet-approved '.mainnetApproved = true'
expect_rejected constructor-asset '.callEscrow.constructorArguments.asset = .designatedDeployer'
expect_rejected duplicate-rpc '.verification.rpcEvidence[1].url = .verification.rpcEvidence[0].url'
expect_rejected head-before-deployment '(.callEscrow.deploymentBlock - 1) as $head | .verification.rpcEvidence[0].observedHead = $head'

printf 'deployment evidence validator rejected all invalid mutations\n'
