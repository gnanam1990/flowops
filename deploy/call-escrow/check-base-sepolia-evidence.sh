#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
record="${FLOWOPS_DEPLOYMENT_RECORD:-${repo_root}/deployments/base-sepolia.json}"

jq -e '
  .schemaVersion == 1
  and .network == "base-sepolia"
  and .chainId == 84532
  and .status == "deployed-verified"
  and .mainnetApproved == false
  and (.designatedDeployer | test("^0x[0-9A-Fa-f]{40}$"))
  and (.canonicalUsdc.address | test("^0x[0-9A-Fa-f]{40}$"))
  and (.callEscrow.address | test("^0x[0-9A-Fa-f]{40}$"))
  and (.callEscrow.deploymentTransaction | test("^0x[0-9a-f]{64}$"))
  and (.callEscrow.deploymentBlock | type == "number" and . > 0)
  and (.callEscrow.deploymentTimestamp | test("^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$"))
  and (.callEscrow.constructorArguments.asset == .canonicalUsdc.address)
  and (.callEscrow.constructorArguments.optimisticReleaseWindowSeconds == .callEscrow.optimisticReleaseWindowSeconds)
  and (.callEscrow.optimisticReleaseWindowSeconds == 3600)
  and (.callEscrow.compiler | test("^v0\\.8\\.26\\+commit\\.[0-9a-f]{8}$"))
  and (.callEscrow.optimizerRuns == 200)
  and (.callEscrow.evmVersion == "cancun")
  and (.callEscrow.sourceVerificationUrl | test("^https://base-sepolia\\.blockscout\\.com/address/0x[0-9a-f]{40}$"))
  and (.callEscrow.runtimeCodeBytes == 5288)
  and (.callEscrow.runtimeCodeHash | test("^0x[0-9a-f]{64}$"))
  and .verification.receiptStatus == true
  and .verification.sourceVerified == true
  and (.verification.rpcEvidence | length >= 2)
  and (.callEscrow.deploymentBlock as $block | .verification.rpcEvidence | all(.observedHead >= $block))
  and (.verification.rpcEvidence | map(.url) | unique | length >= 2)
' "${record}" >/dev/null

contract_identifier="$(jq -r '.callEscrow.contract' "${record}")"
source_path="${contract_identifier%%:*}"
test -f "${repo_root}/${source_path}"

record_address="$(jq -r '.callEscrow.address' "${record}" | tr '[:upper:]' '[:lower:]')"
verification_url="$(jq -r '.callEscrow.sourceVerificationUrl' "${record}")"
case "${verification_url}" in
  *"${record_address}") ;;
  *) exit 1 ;;
esac

foundry_config="$(forge config --json)"
configured_solc="$(jq -r '.solc' <<<"${foundry_config}")"
configured_evm="$(jq -r '.evm_version' <<<"${foundry_config}")"
configured_optimizer_runs="$(jq -r '.optimizer_runs' <<<"${foundry_config}")"
recorded_compiler="$(jq -r '.callEscrow.compiler' "${record}")"
recorded_evm="$(jq -r '.callEscrow.evmVersion' "${record}")"
recorded_optimizer_runs="$(jq -r '.callEscrow.optimizerRuns' "${record}")"

case "${recorded_compiler}" in
  "v${configured_solc}+commit."*) ;;
  *) exit 1 ;;
esac
test "${recorded_evm}" = "${configured_evm}"
test "${recorded_optimizer_runs}" = "${configured_optimizer_runs}"

printf 'validated Base Sepolia deployment evidence %s\n' "$(jq -r '.callEscrow.address' "${record}")"
