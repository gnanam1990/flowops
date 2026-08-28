#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
evidence="${FLOWOPS_ASCP_MAINNET_EVIDENCE_RECORD:-${repo_root}/deployments/base-mainnet-ascp-experimental-v1.json}"
dashboard="${FLOWOPS_ASCP_MAINNET_DASHBOARD_RECORD:-${repo_root}/apps/dashboard/app/mainnet/ascp-mainnet-deployment.json}"
runtime="${FLOWOPS_ASCP_MAINNET_RUNTIME_BINDINGS:-${repo_root}/deploy/control-plane/base-mainnet-ascp-deployed-inactive.env.example}"
activation="${FLOWOPS_ASCP_MAINNET_ACTIVATION_RECORD:-${repo_root}/deployments/base-mainnet-ascp-activation-v1.json}"

for file in "${evidence}" "${dashboard}" "${runtime}" "${activation}"; do
  test -f "${file}" || { echo "missing Base mainnet binding input: ${file}" >&2; exit 1; }
done

FLOWOPS_ASCP_MAINNET_ACTIVATION_RECORD="${activation}" \
  "${repo_root}/deploy/ascp/check-base-mainnet-activation-evidence.sh" >/dev/null

jq -e '
  .schemaVersion == 1 and
  .status == "finalized" and
  .network == "base-mainnet" and
  .chainId == 8453 and
  .authorization.externalReviewCompleted == false and
  .authorization.fundingAuthorized == false and
  .authorization.moduleActivationAuthorized == false and
  .safe.spendModuleEnabled == false and
  .observedInitialState.escrowAllowlisted == false and
  .observedInitialState.allContractNativeBalancesWei == "0" and
  .observedInitialState.allContractUsdcBalancesAtomic == "0" and
  .verification.allDeploymentBlocksFinalized == true and
  ([.contracts[].sourceVerified] | all)
' "${evidence}" >/dev/null

expected_dashboard="$(jq -c --slurpfile activation "${activation}" '
  $activation[0] as $a |
  {
    releaseId,
    network: "Base mainnet",
    chainId,
    firstDeploymentBlock: ([.contracts[].blockNumber] | min),
    finalizedThroughBlock: .verification.finalizedThroughBlock,
    safe: .safe.address,
    asset: .asset,
    contracts: [.contracts[] | {
      name,
      address,
      blockNumber,
      runtimeCodeKeccak,
      sourceVerified
    }],
    activation: {
      externalReviewCompleted: .authorization.externalReviewCompleted,
      safeModuleEnabled: $a.postState.spendModuleEnabled,
      escrowAllowlisted: true,
      fundingAuthorized: $a.postState.fundingEnabled,
      safeTxHash: $a.safe.safeTxHash,
      transactionHash: $a.execution.transactionHash,
      blockNumber: $a.execution.blockNumber,
      safeNonce: $a.postState.safeNonce,
      escrowRuntimeCodeHash: $a.postState.escrowAllowlistCodeHash,
      allContractNativeBalancesWei: .observedInitialState.allContractNativeBalancesWei,
      allContractUsdcBalancesAtomic: .observedInitialState.allContractUsdcBalancesAtomic
    }
  }
' "${evidence}")"

actual_dashboard="$(jq -c '
  {
    releaseId,
    network,
    chainId,
    firstDeploymentBlock,
    finalizedThroughBlock,
    safe,
    asset,
    contracts: [.contracts[] | {
      name: ({
        "Service directory": "service_directory",
        "Agent registry": "agent_registry",
        "Call escrow": "ascp_call_escrow",
        "Safe spend module": "ascp_spend_module"
      }[.name]),
      address,
      blockNumber,
      runtimeCodeKeccak,
      sourceVerified
    }],
    activation: {
      externalReviewCompleted: .activation.externalReviewCompleted,
      safeModuleEnabled: .activation.safeModuleEnabled,
      escrowAllowlisted: .activation.escrowAllowlisted,
      fundingAuthorized: .activation.fundingAuthorized,
      safeTxHash: .activation.safeTxHash,
      transactionHash: .activation.transactionHash,
      blockNumber: .activation.blockNumber,
      safeNonce: .activation.safeNonce,
      escrowRuntimeCodeHash: .activation.escrowRuntimeCodeHash,
      allContractNativeBalancesWei: .activation.allContractNativeBalancesWei,
      allContractUsdcBalancesAtomic: .activation.allContractUsdcBalancesAtomic
    }
  }
' "${dashboard}")"

test "${actual_dashboard}" = "${expected_dashboard}" || {
  echo "dashboard Base mainnet deployment binding differs from canonical evidence" >&2
  exit 1
}

jq -e '
  .status == "activated-zero-fund" and
  .activation.runtimeEnabled == true and
  ([.contracts[].binding] | sort) == ([
    "FLOWOPS_ASCP_DIRECTORY_CONTRACT",
    "FLOWOPS_ASCP_AGENT_REGISTRY_CONTRACT",
    "FLOWOPS_ASCP_CALL_ESCROW_CONTRACT",
    "FLOWOPS_ASCP_SPEND_MODULE_CONTRACT"
  ] | sort)
' "${dashboard}" >/dev/null

env_value() {
  local key="$1"
  local count
  count="$(awk -F= -v key="${key}" '$1 == key { count += 1 } END { print count + 0 }' "${runtime}")"
  test "${count}" = "1" || { echo "${key} must appear exactly once in runtime bindings" >&2; exit 1; }
  awk -F= -v key="${key}" '$1 == key { print substr($0, index($0, "=") + 1) }' "${runtime}"
}

evidence_contract() {
  jq -r --arg name "$1" '.contracts[] | select(.name == $name) | .address' "${evidence}"
}

test "$(env_value FLOWOPS_BASE_CHAIN_ID)" = "8453"
test "$(env_value FLOWOPS_ESCROW_ASSET)" = "$(jq -r '.asset.address' "${evidence}")"
test "$(env_value FLOWOPS_ASCP_DIRECTORY_CONTRACT)" = "$(evidence_contract service_directory)"
test "$(env_value FLOWOPS_ASCP_AGENT_REGISTRY_CONTRACT)" = "$(evidence_contract agent_registry)"
test "$(env_value FLOWOPS_ASCP_CALL_ESCROW_CONTRACT)" = "$(evidence_contract ascp_call_escrow)"
test "$(env_value FLOWOPS_ASCP_SPEND_MODULE_CONTRACT)" = "$(evidence_contract ascp_spend_module)"
test "$(env_value FLOWOPS_ESCROW_CONTRACT)" = "$(evidence_contract ascp_call_escrow)"
test "$(env_value FLOWOPS_ASCP_GOVERNANCE_FROM_BLOCK)" = "$(jq -r '[.contracts[].blockNumber] | min' "${evidence}")"

echo "Base mainnet zero-fund activated dashboard and runtime bindings are canonical"
