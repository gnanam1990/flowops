#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
record="${FLOWOPS_PROPOSAL_ANCHOR_RECORD:-${repo_root}/deployments/base-mainnet-proposal-anchor.json}"
script="${repo_root}/contracts/script/DeployFlowOpsProposalAnchorBaseMainnet.s.sol"
contract="${repo_root}/contracts/src/FlowOpsProposalAnchor.sol"

jq -e '
  .schemaVersion == 1
  and .kind == "flowops-proposal-anchor"
  and .network == "base-mainnet"
  and .chainId == 8453
  and .status == "experimental-deployed-evidence-verified"
  and .proposalDocument == "docs/proposals/FLOWOPS_BASE_MAINNET_EXPERIMENTAL_ANCHOR_V1.md"
  and .proposalDigest == "0x35476d70f7c33d19bb8fc1fa3484e289f0a42aac43e2beca7f941f5340132362"
  and .sourceCommit == "bd9292d0f916b1e3d828443b41e31a8e635b2b3e"
  and .designatedDeployer == "0xEEC526F6555dD43536F712D5c978CbC13CB4517f"
  and .candidateDeployer.address == "0xEEC526F6555dD43536F712D5c978CbC13CB4517f"
  and .candidateDeployer.signerClass == "software-eoa-proposal-only"
  and .candidateDeployer.productionUseProhibited == true
  and .candidateDeployer.observedCode == "0x"
  and .candidateDeployer.observedLatestNonce == 0
  and .candidateDeployer.observedPendingNonce == 0
  and .candidateDeployer.observedBalanceWei == "159318862860265"
  and .candidateDeployer.expectedCreateAddressAtObservedNonce == "0x149D03Ec527Ad8667d47e7b6a2d316Dd54033250"
  and (.candidateDeployer.observedAt | fromdateiso8601 > 0)
  and (.candidateDeployer.observers | sort == ["base-rpc.publicnode.com", "mainnet.base.org"])
  and .promotionPackageApproval.utterance == "APPROVE FLOWOPS PROPOSAL ANCHOR PROMOTION PACKAGE"
  and .promotionPackageApproval.digestAlgorithm == "sha256-utf8-no-newline"
  and .promotionPackageApproval.digest == "0xbfc1cd20d1f05885029683100e8c0a5387948597db5de68ea13eb1043223a726"
  and (.promotionPackageApproval.approvedAt | fromdateiso8601 > 0)
  and .promotionPackageApproval.scope == "promotion-package-only-no-broadcast"
  and .ceremony.expectedDeployerNonce == 0
  and .ceremony.expectedContractAddress == "0x149D03Ec527Ad8667d47e7b6a2d316Dd54033250"
  and .ceremony.initCodeHash == "0x41d3a9c08503394daca600ba7520c6818d7f373c08ecff3c916e2eceef93d35e"
  and .ceremony.expectedRuntimeCodeHash == "0xe5b5b63f37bfd5b6627f48cedd8c0fdcc841f130fd1d5259058374e7a543ed86"
  and .ceremony.estimatedGas == 188437
  and .ceremony.maxGasLimit == 250000
  and .ceremony.maxFeePerGasWei == "20000000"
  and .ceremony.maxGasSpendWei == "5000000000000"
  and (.activationApproval.canonicalStatement | startswith("APPROVE FLOWOPS BASE MAINNET PROPOSAL ANCHOR ACTIVATION:"))
  and .activationApproval.digestAlgorithm == "sha256-utf8-no-newline"
  and .activationApproval.canonicalStatementDigest == "0x5f7b7a92e649df58f7df8afd468e514c8ac5d0f7ff7c5a8108150d25f2cefd17"
  and .activationApproval.userResponse == "do remaining all"
  and .activationApproval.userResponseDigest == "0xf55b7fae90bcceffa559f7d763162b024f5399898d374566623916ee52d2a851"
  and (.activationApproval.approvedAt | fromdateiso8601 > 0)
  and .activationApproval.scope == "activation-package-only-no-broadcast"
  and .deploymentApproval.canonicalStatement == "APPROVE FLOWOPS BASE MAINNET PROPOSAL ANCHOR BROADCAST: chainId=8453; deployer=0xEEC526F6555dD43536F712D5c978CbC13CB4517f; proposalDigest=0x35476d70f7c33d19bb8fc1fa3484e289f0a42aac43e2beca7f941f5340132362; sourceCommit=bd9292d0f916b1e3d828443b41e31a8e635b2b3e; expectedNonce=0; expectedContract=0x149D03Ec527Ad8667d47e7b6a2d316Dd54033250; estimatedGas=188437; maxGasLimit=250000; maxFeePerGasWei=20000000; maxGasSpendWei=5000000000000; noTokenApprovalOrFunding=true; experimentalUnauditedNoFunds=true; broadcast=true"
  and .deploymentApproval.digestAlgorithm == "sha256-utf8-no-newline"
  and .deploymentApproval.canonicalStatementDigest == "0x19b2ec0dad4ae81c0ec838d04285301618f670aa581bda4f218c52dbbd8b5377"
  and (.deploymentApproval.approvedAt | fromdateiso8601 > 0)
  and .deploymentApproval.scope == "one-time-proposal-anchor-broadcast"
  and .deploymentApprovalDigest == "0x19b2ec0dad4ae81c0ec838d04285301618f670aa581bda4f218c52dbbd8b5377"
  and .deploymentEvidence.transactionHash == "0x7fe3986c45a1c4de2c9ca421222569ba8e41cc6b7fe9173340a3954c9306a76b"
  and .deploymentEvidence.receiptStatus == "0x1"
  and .deploymentEvidence.blockNumber == 50008264
  and .deploymentEvidence.blockHash == "0xef4a24ad1b9803df3e5a03b533ee39e36c4a17b1585eb7b90e1b852d4a3a8ae8"
  and .deploymentEvidence.blockTimestamp == "2026-08-15T14:57:55Z"
  and .deploymentEvidence.deployer == "0xEEC526F6555dD43536F712D5c978CbC13CB4517f"
  and .deploymentEvidence.deployerNonce == 0
  and .deploymentEvidence.contractAddress == "0x149D03Ec527Ad8667d47e7b6a2d316Dd54033250"
  and .deploymentEvidence.transactionValueWei == "0"
  and .deploymentEvidence.gasLimit == 232243
  and .deploymentEvidence.gasUsed == 185795
  and .deploymentEvidence.maxFeePerGasWei == "20000000"
  and .deploymentEvidence.maxPriorityFeePerGasWei == "1000000"
  and .deploymentEvidence.effectiveGasPriceWei == "6000000"
  and .deploymentEvidence.totalPaidWei == "1114770000000"
  and .deploymentEvidence.creationInputHash == "0x41d3a9c08503394daca600ba7520c6818d7f373c08ecff3c916e2eceef93d35e"
  and .deploymentEvidence.runtimeCodeHash == "0xe5b5b63f37bfd5b6627f48cedd8c0fdcc841f130fd1d5259058374e7a543ed86"
  and .deploymentEvidence.eventSignature == "0x3a34f175c3fff575959bd2f7cff58cedda132bd7396d693e43e0ace0d5785c6e"
  and (.deploymentEvidence.observedAt | fromdateiso8601 > 0)
  and (.deploymentEvidence.observers | sort == ["base.drpc.org", "mainnet.base.org"])
  and .deploymentEvidence.sourceVerification.provider == "base-blockscout"
  and .deploymentEvidence.sourceVerification.url == "https://base.blockscout.com/address/0x149d03ec527ad8667d47e7b6a2d316dd54033250"
  and .deploymentEvidence.sourceVerification.status == "fully-verified"
  and .deploymentEvidence.sourceVerification.contractName == "FlowOpsProposalAnchor"
  and .deploymentEvidence.sourceVerification.compilerVersion == "v0.8.26+commit.8a97fa7a"
  and .deploymentEvidence.sourceVerification.optimizationEnabled == true
  and .deploymentEvidence.sourceVerification.optimizationRuns == 200
  and .deploymentEvidence.sourceVerification.evmVersion == "cancun"
  and (.deploymentEvidence.sourceVerification.verifiedAt | fromdateiso8601 > 0)
  and .postDeploymentObservation.deployerLatestNonce == 1
  and .postDeploymentObservation.deployerPendingNonce == 1
  and .postDeploymentObservation.deployerBalanceWei == "158200871075539"
  and .postDeploymentObservation.runtimeCodeHash == "0xe5b5b63f37bfd5b6627f48cedd8c0fdcc841f130fd1d5259058374e7a543ed86"
  and (.postDeploymentObservation.observedAt | fromdateiso8601 > 0)
  and (.postDeploymentObservation.observers | sort == ["base-rpc.publicnode.com", "mainnet.base.org"])
  and .contractAddress == "0x149D03Ec527Ad8667d47e7b6a2d316Dd54033250"
  and .transactionHash == "0x7fe3986c45a1c4de2c9ca421222569ba8e41cc6b7fe9173340a3954c9306a76b"
  and .blockNumber == 50008264
  and .runtimeCodeHash == "0xe5b5b63f37bfd5b6627f48cedd8c0fdcc841f130fd1d5259058374e7a543ed86"
  and .sourceVerified == true
  and .broadcastAuthorized == false
  and .productionReady == false
  and .fundingEnabled == false
  and .vaultCreationEnabled == false
  and .externalAuditCompleted == false
  and (.warnings | length == 4)
  and (.warnings | any(test("Experimental and unaudited")))
  and (.warnings | any(test("Not approved for production")))
  and (.warnings | any(test("Do not send ETH or tokens")))
  and (.warnings | any(test("No USDC approval, deposit, funding, or vault creation")))
' "${record}" >/dev/null

grep -Fq 'address public constant DESIGNATED_DEPLOYER = 0xEEC526F6555dD43536F712D5c978CbC13CB4517f;' "${script}"
grep -Fq 'bytes32 public constant PROPOSAL_DIGEST = 0x35476d70f7c33d19bb8fc1fa3484e289f0a42aac43e2beca7f941f5340132362;' "${script}"
grep -Fq 'bytes20 public constant SOURCE_COMMIT = hex"bd9292d0f916b1e3d828443b41e31a8e635b2b3e";' "${script}"
grep -Fq 'uint64 public constant EXPECTED_DEPLOYER_NONCE = 0;' "${script}"
grep -Fq 'address public constant EXPECTED_ANCHOR_ADDRESS = 0x149D03Ec527Ad8667d47e7b6a2d316Dd54033250;' "${script}"
grep -Fq '0x19b2ec0dad4ae81c0ec838d04285301618f670aa581bda4f218c52dbbd8b5377;' "${script}"
grep -Fq 'bool public constant MAINNET_BROADCAST_ENABLED = false;' "${script}"
grep -Fq 'string public constant DEPLOYMENT_STATUS = "EXPERIMENTAL_UNAUDITED_NO_FUNDS";' "${contract}"

method_identifiers="$(
  cd "${repo_root}"
  forge inspect contracts/src/FlowOpsProposalAnchor.sol:FlowOpsProposalAnchor methodIdentifiers --json
)"
jq -e '
  keys | sort == [
    "BASE_MAINNET_CHAIN_ID()",
    "DEPLOYMENT_STATUS()",
    "KIND()",
    "acceptsFunds()",
    "deployer()",
    "productionReady()",
    "proposalDigest()",
    "sourceCommit()",
    "vaultCreationEnabled()"
  ]
' <<<"${method_identifiers}" >/dev/null

abi="$(
  cd "${repo_root}"
  forge inspect contracts/src/FlowOpsProposalAnchor.sol:FlowOpsProposalAnchor abi --json
)"
jq -e '
  ([.[] | select(.type == "function") | .stateMutability] | all(. == "view" or . == "pure"))
  and ([.[] | select(.type == "receive" or .type == "fallback")] | length == 0)
  and ([.[] | select(.type == "constructor") | .stateMutability] == ["nonpayable"])
' <<<"${abi}" >/dev/null

storage_layout="$(
  cd "${repo_root}"
  forge inspect contracts/src/FlowOpsProposalAnchor.sol:FlowOpsProposalAnchor storageLayout --json
)"
jq -e '.storage == []' <<<"${storage_layout}" >/dev/null

proposal_digest="$(shasum -a 256 "${repo_root}/docs/proposals/FLOWOPS_BASE_MAINNET_EXPERIMENTAL_ANCHOR_V1.md" | awk '{print $1}')"
test "0x${proposal_digest}" = "0x35476d70f7c33d19bb8fc1fa3484e289f0a42aac43e2beca7f941f5340132362"

approval_digest="$(printf '%s' 'APPROVE FLOWOPS PROPOSAL ANCHOR PROMOTION PACKAGE' | shasum -a 256 | awk '{print $1}')"
test "0x${approval_digest}" = "0xbfc1cd20d1f05885029683100e8c0a5387948597db5de68ea13eb1043223a726"

activation_statement="$(jq -r '.activationApproval.canonicalStatement' "${record}")"
activation_digest="$(printf '%s' "${activation_statement}" | shasum -a 256 | awk '{print $1}')"
test "0x${activation_digest}" = "0x5f7b7a92e649df58f7df8afd468e514c8ac5d0f7ff7c5a8108150d25f2cefd17"

user_response="$(jq -r '.activationApproval.userResponse' "${record}")"
user_response_digest="$(printf '%s' "${user_response}" | shasum -a 256 | awk '{print $1}')"
test "0x${user_response_digest}" = "0xf55b7fae90bcceffa559f7d763162b024f5399898d374566623916ee52d2a851"

deployment_statement="$(jq -r '.deploymentApproval.canonicalStatement' "${record}")"
deployment_digest="$(printf '%s' "${deployment_statement}" | shasum -a 256 | awk '{print $1}')"
test "0x${deployment_digest}" = "0x19b2ec0dad4ae81c0ec838d04285301618f670aa581bda4f218c52dbbd8b5377"

printf 'validated Base mainnet proposal anchor deployment evidence; broadcast is consumed\n'
