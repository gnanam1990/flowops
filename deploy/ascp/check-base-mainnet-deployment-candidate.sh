#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
record="${FLOWOPS_ASCP_MAINNET_CANDIDATE_RECORD:-${repo_root}/deployments/base-mainnet-ascp-deployment-candidate-v1.json}"
checksum="${FLOWOPS_ASCP_MAINNET_CANDIDATE_CHECKSUM:-${repo_root}/deployments/base-mainnet-ascp-deployment-candidate-v1.sha256}"

jq -e '
  def address: test("^0x[0-9a-f]{40}$");
  def digest: test("^0x[0-9a-f]{64}$");
  . as $record
  | .schemaVersion == 1
  and .status == "prepared-unapproved"
  and .purpose == "zero-fund-ascp-v4-contract-graph"
  and .network == "base-mainnet"
  and .chainId == 8453
  and .preparedAt == "2026-08-28T04:15:22Z"
  and .sourceBaseline.commit == "ae8ebfdfa8d1e6013888134d72610f9ab9032b53"
  and .sourceBaseline.requiresPromotionCommit == true
  and .sourceBaseline.deploymentScript == {
    path: "contracts/script/DeployASCPBaseMainnet.s.sol",
    sha256: "0xf812893ebc02707b0444ebf5c70a62deebd0399d1139f4a7f51efb6002e4c4c7"
  }
  and .sourceBaseline.contractSources == [
    {
      name: "service_directory",
      path: "contracts/src/ServiceDirectory.sol",
      sha256: "0x96383f78d182e194fe5c4e01ca57f4929503258c6558f3b9f693ed7c630f54bc"
    },
    {
      name: "agent_registry",
      path: "contracts/src/AgentRegistry.sol",
      sha256: "0x70e41f43eb8b3e14d9a100b86738dcaa92db6c51ff3ebcd5c55fdd807299bca4"
    },
    {
      name: "ascp_call_escrow",
      path: "contracts/src/ASCPCallEscrow.sol",
      sha256: "0x3849c079e603e0314e976d91fa351e011b8fa09637ed8e283192e7cc40ec7181"
    },
    {
      name: "ascp_spend_module",
      path: "contracts/src/ASCPSpendModule.sol",
      sha256: "0x974dec9237a927258faa232c9e97ba6628ae4d099cbff5208bcd54a72b440efe"
    }
  ]
  and .sourceBaseline.foundryConfiguration == {
    path: "foundry.toml",
    sha256: "0xe843a712bc7043b7ac16ef357ee29c300ba663a0d971d6f229c1c11b84bd88b0",
    solc: "0.8.26",
    evmVersion: "cancun",
    optimizer: true,
    optimizerRuns: 200
  }
  and .sourceBaseline.dependencies == {
    forgeStdCommit: "77041d2ce690e692d6e03cc812b57d1ddaa4d505",
    openzeppelinContractsCommit: "c64a1edb67b6e3f4a15cca8909c9482ad33a02b0"
  }
  and .structuralGates == {
    committedScriptDeployer: "0x0000000000000000000000000000000000000000",
    committedScriptExpectedNonce: 0,
    committedScriptSafe: "0x0000000000000000000000000000000000000000",
    committedScriptExpectedSafeOwners: [
      "0x0000000000000000000000000000000000000000",
      "0x0000000000000000000000000000000000000000",
      "0x0000000000000000000000000000000000000000"
    ],
    committedScriptExpectedSafeThreshold: 0,
    committedScriptExpectedSafeNonce: 0,
    committedScriptExpectedSafeRuntimeCodeHash: "0x0000000000000000000000000000000000000000000000000000000000000000",
    committedScriptExpectedSafeImplementation: "0x0000000000000000000000000000000000000000",
    externalReviewDigest: null,
    releasePlanDigest: null,
    broadcastEnabled: false,
    deploymentAuthorized: false,
    fundingAuthorized: false
  }
  and (.deployer.address | address)
  and .deployer.expectedNonce == 1
  and .deployer.observedLatestNonce == 1
  and .deployer.observedPendingNonce == 1
  and .deployer.observedBalanceWei == "147950415987658"
  and (.safe.address | address)
  and .safe.runtimeCodeHash == "0xd7d408ebcd99b2b70be43e20253d6d92a8ea8fab29bd3be7f55b10032331fb4c"
  and .safe.implementation == "0x29fcb43b46531bca003ddc8fcb67ffe91900c762"
  and (.safe.owners | length == 3 and length == (unique | length) and all(. | address))
  and .safe.threshold == 2
  and (.safe.deploymentTransaction | digest)
  and .safe.deploymentBlock == 50535016
  and .safe.finalized == true
  and .safe.nonce == 0
  and .safe.enabledModules == []
  and .safe.nativeBalanceWei == "0"
  and .safe.usdcBalanceAtomic == "0"
  and .asset == {
    address: "0x833589fcd6edb6e08f4c7c32d4f71b54bda02913",
    symbol: "USDC",
    decimals: 6,
    runtimeCodeHash: "0xa6705a10bb756b5dea144591118be77d7af0c3eee3bf2dfe2583dcb0364fefab"
  }
  and (.organizationDomain | digest)
  and .authorities.governor == .safe.address
  and ([.authorities[]] | length == 5 and length == (unique | length) and all(. | address))
  and .initialCapsAtomic == {perTransaction: "1000000", perDay: "10000000", allowanceCeiling: "10000000"}
  and (.contracts | length == 4)
  and ([.contracts[].name] == ["service_directory", "agent_registry", "ascp_call_escrow", "ascp_spend_module"])
  and ([.contracts[].creationNonce] == [1, 2, 3, 4])
  and ([.contracts[].predictedAddress] | length == (unique | length) and all(. | address))
  and .contracts[0].artifact == "contracts/src/ServiceDirectory.sol:ServiceDirectory"
  and .contracts[0].constructorSignature == "f(address,address,address,bytes32)"
  and .contracts[0].constructorArguments == [
    $record.safe.address,
    $record.authorities.directoryPublisher,
    $record.authorities.directoryPauser,
    $record.organizationDomain
  ]
  and .contracts[1].artifact == "contracts/src/AgentRegistry.sol:AgentRegistry"
  and .contracts[1].constructorSignature == "f(address,address,bytes32)"
  and .contracts[1].constructorArguments == [
    $record.safe.address,
    $record.authorities.registryAdmin,
    $record.organizationDomain
  ]
  and .contracts[2].artifact == "contracts/src/ASCPCallEscrow.sol:ASCPCallEscrow"
  and .contracts[2].constructorSignature == "f(address,address,address,address)"
  and .contracts[2].constructorArguments == [
    $record.asset.address,
    $record.contracts[0].predictedAddress,
    $record.safe.address,
    $record.safe.address
  ]
  and .contracts[3].artifact == "contracts/src/ASCPSpendModule.sol:ASCPSpendModule"
  and .contracts[3].constructorSignature == "f(address,address,address,(uint256,uint256,uint256))"
  and .contracts[3].constructorArguments == [
    $record.safe.address,
    $record.asset.address,
    $record.authorities.spendAuthorizer,
    [
      $record.initialCapsAtomic.perTransaction,
      $record.initialCapsAtomic.perDay,
      $record.initialCapsAtomic.allowanceCeiling
    ]
  ]
  and (.contracts | all((.creationBytecodeKeccak | digest) and (.constructorArgumentsKeccak | digest) and (.initCodeKeccak | digest) and .initCodeBytes > 0))
  and .requiredInitialState == {
    moduleEnabled: false,
    escrowAllowlisted: false,
    directoryVersion: 0,
    directoryRoot: "0x0000000000000000000000000000000000000000000000000000000000000000",
    agentCount: 0,
    verifierActivated: false,
    usdcApproved: false,
    totalLockedAtomic: "0",
    executedPrincipalAtomic: "0",
    contractNativeBalancesWei: "0",
    contractUsdcBalancesAtomic: "0"
  }
  and .readOnlyEvidence.promotionRecord == "deployments/base-mainnet-ascp-promotion.json"
  and .readOnlyEvidence.safeVerifier == "deploy/ascp/verify-base-mainnet-safe-readonly.sh"
  and .readOnlyEvidence.observedAt == "2026-08-28T03:34:59Z"
  and .readOnlyEvidence.publicRpcUrls == ["https://mainnet.base.org", "https://base-mainnet.public.blastapi.io"]
  and .readOnlyEvidence.productionRpcAdmissionComplete == false
  and .readOnlyEvidence.allPredictedAddressesClean == true
  and (.unresolved | sort == [
    "ascp-independent-contract-review",
    "fresh-zero-fund-broadcast-approval",
    "reviewed-promotion-commit",
    "safe-owner-control-proof",
    "signed-runtime-release-manifest",
    "two-independent-production-rpc-admissions"
  ])
' "${record}" >/dev/null

promotion="${repo_root}/$(jq -er '.readOnlyEvidence.promotionRecord' "${record}")"
FLOWOPS_ASCP_MAINNET_PROMOTION_RECORD="${promotion}" \
  "${repo_root}/deploy/ascp/check-base-mainnet-promotion.sh" >/dev/null
jq -e --slurpfile promotion "${promotion}" '
  ($promotion[0]) as $promotion
  | .deployer.address == $promotion.deployer.address
  and .deployer.expectedNonce == $promotion.deployer.expectedNonce
  and .safe.address == $promotion.safe.address
  and .safe.runtimeCodeHash == $promotion.safe.runtimeCodeHash
  and .safe.implementation == $promotion.safe.implementation
  and .safe.owners == $promotion.safe.owners
  and .safe.threshold == $promotion.safe.threshold
  and .safe.deploymentTransaction == $promotion.safe.deploymentTransaction
  and .safe.deploymentBlock == $promotion.safe.deploymentBlock
  and .safe.finalized == $promotion.safe.deploymentFinalized
  and .safe.nonce == $promotion.safe.verification.safeNonce
  and .safe.enabledModules == $promotion.safe.verification.enabledModules
  and .safe.nativeBalanceWei == $promotion.safe.verification.nativeBalanceWei
  and .safe.usdcBalanceAtomic == $promotion.safe.verification.usdcBalanceAtomic
  and .asset == $promotion.asset
  and .organizationDomain == $promotion.organizationDomain
  and .authorities.governor == $promotion.safe.address
  and .authorities.directoryPublisher == $promotion.authorities.directoryPublisher
  and .authorities.directoryPauser == $promotion.authorities.directoryPauser
  and .authorities.registryAdmin == $promotion.authorities.registryAdmin
  and .authorities.spendAuthorizer == $promotion.authorities.spendAuthorizer
  and .initialCapsAtomic.perTransaction == $promotion.pilot.maximumPerActionAtomic
  and .initialCapsAtomic.perDay == $promotion.pilot.maximumDailyAtomic
  and .initialCapsAtomic.allowanceCeiling == $promotion.pilot.maximumAllowanceAtomic
  and ([.contracts[] | {name, creationNonce, address: .predictedAddress}]
    == [$promotion.predictedContracts[] | {name, creationNonce, address}])
' "${record}" >/dev/null

source_commit="$(jq -er '.sourceBaseline.commit' "${record}")"
git -C "${repo_root}" cat-file -e "${source_commit}^{commit}"

verify_source() {
  local path="$1"
  local expected="$2"
  local actual
  actual="0x$(git -C "${repo_root}" show "${source_commit}:${path}" | shasum -a 256 | awk '{print $1}')"
  test "${actual}" = "${expected}"
}

verify_source \
  "$(jq -er '.sourceBaseline.deploymentScript.path' "${record}")" \
  "$(jq -er '.sourceBaseline.deploymentScript.sha256' "${record}")"
while IFS= read -r source; do
  verify_source "$(jq -er '.path' <<<"${source}")" "$(jq -er '.sha256' <<<"${source}")"
done < <(jq -c '.sourceBaseline.contractSources[]' "${record}")
verify_source \
  "$(jq -er '.sourceBaseline.foundryConfiguration.path' "${record}")" \
  "$(jq -er '.sourceBaseline.foundryConfiguration.sha256' "${record}")"

test "$(git -C "${repo_root}/lib/forge-std" rev-parse HEAD)" = "$(jq -er '.sourceBaseline.dependencies.forgeStdCommit' "${record}")"
test "$(git -C "${repo_root}/lib/openzeppelin-contracts" rev-parse HEAD)" = "$(jq -er '.sourceBaseline.dependencies.openzeppelinContractsCommit' "${record}")"

deployer="$(jq -er '.deployer.address' "${record}")"
while IFS= read -r contract; do
  expected_address="$(jq -er '.predictedAddress' <<<"${contract}")"
  creation_nonce="$(jq -er '.creationNonce' <<<"${contract}")"
  observed_address="$(cast compute-address --nonce "${creation_nonce}" "${deployer}" | awk '{print tolower($NF)}')"
  test "${observed_address}" = "${expected_address}"

  artifact="$(jq -er '.artifact' <<<"${contract}")"
  bytecode="$(forge inspect "${artifact}" bytecode)"
  signature="$(jq -er '.constructorSignature' <<<"${contract}")"
  name="$(jq -er '.name' <<<"${contract}")"
  case "${name}" in
    service_directory)
      encoded="$(cast abi-encode "${signature}" \
        "$(jq -er '.constructorArguments[0]' <<<"${contract}")" \
        "$(jq -er '.constructorArguments[1]' <<<"${contract}")" \
        "$(jq -er '.constructorArguments[2]' <<<"${contract}")" \
        "$(jq -er '.constructorArguments[3]' <<<"${contract}")")"
      ;;
    agent_registry)
      encoded="$(cast abi-encode "${signature}" \
        "$(jq -er '.constructorArguments[0]' <<<"${contract}")" \
        "$(jq -er '.constructorArguments[1]' <<<"${contract}")" \
        "$(jq -er '.constructorArguments[2]' <<<"${contract}")")"
      ;;
    ascp_call_escrow)
      encoded="$(cast abi-encode "${signature}" \
        "$(jq -er '.constructorArguments[0]' <<<"${contract}")" \
        "$(jq -er '.constructorArguments[1]' <<<"${contract}")" \
        "$(jq -er '.constructorArguments[2]' <<<"${contract}")" \
        "$(jq -er '.constructorArguments[3]' <<<"${contract}")")"
      ;;
    ascp_spend_module)
      caps="$(jq -r '.constructorArguments[3] | "(" + join(",") + ")"' <<<"${contract}")"
      encoded="$(cast abi-encode "${signature}" \
        "$(jq -er '.constructorArguments[0]' <<<"${contract}")" \
        "$(jq -er '.constructorArguments[1]' <<<"${contract}")" \
        "$(jq -er '.constructorArguments[2]' <<<"${contract}")" \
        "${caps}")"
      ;;
    *) exit 1 ;;
  esac
  initcode="$(printf '0x%s%s' "$(sed 's/^0x//' <<<"${bytecode}")" "$(sed 's/^0x//' <<<"${encoded}")")"
  hex_chars="$(printf '%s' "${initcode}" | wc -c | tr -d ' ')"
  test "$(printf '%s' "${bytecode}" | cast keccak)" = "$(jq -er '.creationBytecodeKeccak' <<<"${contract}")"
  test "$(printf '%s' "${encoded}" | cast keccak)" = "$(jq -er '.constructorArgumentsKeccak' <<<"${contract}")"
  test "$(printf '%s' "${initcode}" | cast keccak)" = "$(jq -er '.initCodeKeccak' <<<"${contract}")"
  test "$(((hex_chars - 2) / 2))" = "$(jq -er '.initCodeBytes' <<<"${contract}")"
done < <(jq -c '.contracts[]' "${record}")

checksum_name="$(basename "${record}")"
expected_checksum="$(awk -v name="${checksum_name}" '$2 == name {print $1}' "${checksum}")"
test -n "${expected_checksum}"
actual_checksum="$(shasum -a 256 "${record}" | awk '{print $1}')"
test "${actual_checksum}" = "${expected_checksum}"

source_text="$(tr '[:upper:]' '[:lower:]' <"${repo_root}/contracts/script/DeployASCPBaseMainnet.s.sol")"
grep -Fq 'address public constant designated_deployer = address(0);' <<<"${source_text}"
grep -Fq 'uint256 public constant expected_deployer_nonce = 0;' <<<"${source_text}"
grep -Fq 'address public constant production_safe = address(0);' <<<"${source_text}"
grep -Fq 'address public constant expected_safe_owner_1 = address(0);' <<<"${source_text}"
grep -Fq 'address public constant expected_safe_owner_2 = address(0);' <<<"${source_text}"
grep -Fq 'address public constant expected_safe_owner_3 = address(0);' <<<"${source_text}"
grep -Fq 'uint256 public constant expected_safe_threshold = 0;' <<<"${source_text}"
grep -Fq 'uint256 public constant expected_safe_nonce = 0;' <<<"${source_text}"
grep -Fq 'bytes32 public constant expected_safe_runtime_code_hash = bytes32(0);' <<<"${source_text}"
grep -Fq 'address public constant expected_safe_implementation = address(0);' <<<"${source_text}"
grep -Fq 'bytes32 public constant external_review_digest = bytes32(0);' <<<"${source_text}"
grep -Fq 'bytes32 public constant release_plan_digest = bytes32(0);' <<<"${source_text}"
grep -Fq 'bool public constant mainnet_broadcast_enabled = false;' <<<"${source_text}"

printf 'validated reproducible Base mainnet ASCP zero-fund deployment candidate; broadcast remains structurally disabled\n'
