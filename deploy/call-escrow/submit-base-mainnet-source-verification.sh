#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
record="${repo_root}/deployments/base-mainnet-readiness.json"
cd "${repo_root}"

: "${FLOWOPS_SUBMIT_SOURCE_VERIFICATION:?set FLOWOPS_SUBMIT_SOURCE_VERIFICATION=true after read-only verification}"
test "${FLOWOPS_SUBMIT_SOURCE_VERIFICATION}" = 'true'

jq -e '
  .mainnetApproved == true
  and .gates.sourceVerificationPlanImplemented == true
  and .gates.sourceVerificationPlanApproved == true
  and (.callEscrow.address | test("^0x[0-9A-Fa-f]{40}$"))
  and (.callEscrow.deploymentTransaction | test("^0x[0-9A-Fa-f]{64}$"))
  and (.callEscrow.runtimeCodeHash | test("^0x[0-9a-f]{64}$"))
  and (.callEscrow.minimumDeploymentConfirmations | type == "number" and floor == . and . > 0 and . <= 10000)
' "${record}" >/dev/null

: "${ETHERSCAN_API_KEY:?load the BaseScan API key from the secret manager}"

deploy/call-escrow/verify-base-mainnet-deployment-readonly.sh

address="$(jq -er '.callEscrow.address' "${record}")"
asset="$(jq -er '.canonicalUsdc.address' "${record}")"
release_window="$(jq -er '.callEscrow.optimisticReleaseWindowSeconds' "${record}")"
constructor_args="$(cast abi-encode 'constructor(address,uint256)' "${asset}" "${release_window}")"

forge verify-contract \
  "${address}" \
  contracts/src/CallEscrow.sol:CallEscrow \
  --chain 8453 \
  --constructor-args "${constructor_args}" \
  --compiler-version v0.8.26+commit.8a97fa7a \
  --num-of-optimizations 200 \
  --evm-version cancun \
  --verifier etherscan \
  --watch
