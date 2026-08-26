#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
record="${FLOWOPS_ASCP_SEPOLIA_EVIDENCE_RECORD:-${repo_root}/deployments/base-sepolia-ascp-v4.json}"

jq -e '
  def address: test("^0x[0-9a-f]{40}$") and . != "0x0000000000000000000000000000000000000000";
  def digest: test("^0x[0-9a-f]{64}$") and . != "0x0000000000000000000000000000000000000000000000000000000000000000";
  def timestamp: test("^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$");
  (.contracts | map({key: .name, value: .}) | from_entries) as $contracts
  | .schemaVersion == 1
  and .releaseId == "ascp-v4-base-sepolia-2026-08-26"
  and .network == "base-sepolia"
  and .chainId == 84532
  and .status == "deployed-source-verified-write-inert"
  and (.sourceCommit | test("^[0-9a-f]{40}$"))
  and (.deploymentPlanDigest | digest)
  and (.organizationDomain | digest)
  and (.typedDataManifestSha256 | digest)
  and (.deployer.address | address)
  and .deployer.startingNonce == 6
  and .deployer.endingNonce == 10
  and (.safe.address | address)
  and (.safe.owners | length == 3 and all(address) and (unique | length == 3))
  and .safe.threshold == 2
  and .safe.spendModuleEnabled == false
  and .authorities.governor == .safe.address
  and ([.deployer.address, .safe.address, .authorities.directoryPublisher, .authorities.directoryPauser,
        .authorities.registryAdmin, .authorities.spendAuthorizer] | all(address) and (unique | length == 6))
  and (.asset.address | address)
  and .asset.symbol == "USDC"
  and .asset.decimals == 6
  and (.asset.runtimeCodeHash | digest)
  and (.contracts | length == 4)
  and (.contracts | map(.name) | sort == ["agent_registry", "ascp_call_escrow", "ascp_spend_module", "service_directory"])
  and (.contracts | map(.address) | all(address) and (unique | length == 4))
  and (.contracts | map(.deploymentTx) | all(digest) and (unique | length == 4))
  and (.contracts | map(.runtimeCodeHash) | all(digest) and (unique | length == 4))
  and (.contracts | map(.creationInputHash) | all(digest) and (unique | length == 4))
  and (.contracts | map(.creationNonce) | sort == [6, 7, 8, 9])
  and $contracts.service_directory.creationNonce == 6
  and $contracts.agent_registry.creationNonce == 7
  and $contracts.ascp_call_escrow.creationNonce == 8
  and $contracts.ascp_spend_module.creationNonce == 9
  and ($contracts.service_directory.deploymentBlock < $contracts.agent_registry.deploymentBlock)
  and ($contracts.agent_registry.deploymentBlock < $contracts.ascp_call_escrow.deploymentBlock)
  and ($contracts.ascp_call_escrow.deploymentBlock < $contracts.ascp_spend_module.deploymentBlock)
  and (.contracts | all(
    .address as $address
    | (.artifact | test("^contracts/src/[A-Za-z0-9]+\\.sol:[A-Za-z0-9]+$"))
    and (.deploymentBlock | type == "number" and . > 0)
    and (.deploymentTimestamp | timestamp)
    and .receiptStatus == true
    and (.gasUsed | type == "number" and . > 0)
    and (.l2ExecutionFeeWei | test("^[0-9]+$") and (tonumber > 0))
    and (.l1DataFeeWei | test("^[0-9]+$") and (tonumber > 0))
    and (.actualTotalFeeWei | test("^[0-9]+$") and (tonumber > 0))
    and ((.actualTotalFeeWei | tonumber) == ((.l2ExecutionFeeWei | tonumber) + (.l1DataFeeWei | tonumber)))
    and (.runtimeCodeBytes | type == "number" and . > 0)
    and .sourceVerification.verified == true
    and .sourceVerification.provider == "sourcify"
    and .sourceVerification.status == "exact_match"
    and (.sourceVerification.jobId | test("^[0-9a-f-]{36}$"))
    and (.sourceVerification.sourcifyUrl | test("^https://sourcify\\.dev/server/v2/contract/84532/0x[0-9a-f]{40}\\?fields=all$"))
    and (.sourceVerification.blockscoutUrl | test("^https://base-sepolia\\.blockscout\\.com/address/0x[0-9a-f]{40}\\?tab=contract$"))
    and (.sourceVerification.sourcifyUrl | contains($address))
    and (.sourceVerification.blockscoutUrl | contains($address))
  ))
  and .governanceFromBlock == $contracts.service_directory.deploymentBlock
  and $contracts.service_directory.constructorArguments.governor == .safe.address
  and $contracts.service_directory.constructorArguments.directoryPublisher == .authorities.directoryPublisher
  and $contracts.service_directory.constructorArguments.directoryPauser == .authorities.directoryPauser
  and $contracts.service_directory.constructorArguments.organizationDomain == .organizationDomain
  and $contracts.agent_registry.constructorArguments.governor == .safe.address
  and $contracts.agent_registry.constructorArguments.registryAdmin == .authorities.registryAdmin
  and $contracts.agent_registry.constructorArguments.organizationDomain == .organizationDomain
  and $contracts.ascp_call_escrow.constructorArguments.usdc == .asset.address
  and $contracts.ascp_call_escrow.constructorArguments.serviceDirectory == $contracts.service_directory.address
  and $contracts.ascp_call_escrow.constructorArguments.safe == .safe.address
  and $contracts.ascp_call_escrow.constructorArguments.governor == .safe.address
  and $contracts.ascp_spend_module.constructorArguments.safe == .safe.address
  and $contracts.ascp_spend_module.constructorArguments.token == .asset.address
  and $contracts.ascp_spend_module.constructorArguments.spendAuthorizer == .authorities.spendAuthorizer
  and $contracts.ascp_spend_module.constructorArguments.caps == {
    perTransaction: "1000000", perDay: "10000000", allowanceCeiling: "10000000"
  }
  and .compiler.version == "v0.8.26+commit.8a97fa7a"
  and .compiler.optimizerEnabled == true
  and .compiler.optimizerRuns == 200
  and .compiler.evmVersion == "cancun"
  and .compiler.bytecodeHash == "ipfs"
  and (.fees.actualGasUsed | type == "number")
  and .fees.actualGasUsed > 0
  and .fees.actualGasUsed <= .fees.approvedGasCeiling
  and .fees.actualGasUsed == ([.contracts[].gasUsed] | add)
  and (.fees.configuredGasLimit | type == "number")
  and .fees.configuredGasLimit <= .fees.approvedGasCeiling
  and ((.fees.actualTotalFeeWei | tonumber) == ((.fees.l2ExecutionFeeWei | tonumber) + (.fees.l1DataFeeWei | tonumber)))
  and ((.fees.l2ExecutionFeeWei | tonumber) == ([.contracts[].l2ExecutionFeeWei | tonumber] | add))
  and ((.fees.l1DataFeeWei | tonumber) == ([.contracts[].l1DataFeeWei | tonumber] | add))
  and ((.fees.actualTotalFeeWei | tonumber) == ([.contracts[].actualTotalFeeWei | tonumber] | add))
  and ((.fees.actualTotalFeeWei | tonumber) <= (.fees.approvedMaximumSpendWei | tonumber))
  and .initialState == {
    directoryVersion: 0,
    directoryRoot: "0x0000000000000000000000000000000000000000000000000000000000000000",
    agentCount: 0,
    escrowTotalLocked: "0",
    spendExecutedPrincipal: "0",
    escrowAllowlistCodeHash: "0x0000000000000000000000000000000000000000000000000000000000000000",
    callEscrowEmergencyPaused: false,
    spendModuleEmergencyPaused: false,
    allContractNativeBalancesWei: "0",
    allContractUsdcBalances: "0",
    safeToSpendModuleUsdcAllowance: "0",
    safeToCallEscrowUsdcAllowance: "0"
  }
  and .verification.providersAgreed == true
  and (.verification.verifiedAt | timestamp)
  and (.verification.rpcEvidence | length >= 2)
  and (.verification.rpcEvidence | map(.url) | unique | length >= 2)
  and (.verification.rpcEvidence | all(.observedHead >= $contracts.ascp_spend_module.deploymentBlock))
  and .runtimeEnabled == false
  and .fundingEnabled == false
  and .mainnetApproved == false
' "${record}" >/dev/null

source_commit="$(jq -r '.sourceCommit' "${record}")"
git -C "${repo_root}" cat-file -e "${source_commit}^{commit}"
git -C "${repo_root}" merge-base --is-ancestor "${source_commit}" HEAD

while IFS= read -r artifact; do
  source_path="${artifact%%:*}"
  test -f "${repo_root}/${source_path}"
done < <(jq -r '.contracts[].artifact' "${record}")

foundry_config="$(forge config --root "${repo_root}" --json)"
test "$(jq -r '.solc' <<<"${foundry_config}")" = '0.8.26'
test "$(jq -r '.optimizer' <<<"${foundry_config}")" = 'true'
test "$(jq -r '.optimizer_runs' <<<"${foundry_config}")" = '200'
test "$(jq -r '.evm_version' <<<"${foundry_config}")" = 'cancun'
test "$(jq -r '.bytecode_hash' <<<"${foundry_config}")" = 'ipfs'

printf 'validated ASCP v4 Base Sepolia evidence %s\n' "$(jq -r '.releaseId' "${record}")"
