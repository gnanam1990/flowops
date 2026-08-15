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
  and .status == "promotion-package-approved-no-deployment"
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
  and .deploymentApprovalDigest == null
  and .contractAddress == null
  and .transactionHash == null
  and .blockNumber == null
  and .runtimeCodeHash == null
  and .sourceVerified == false
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
grep -Fq 'bytes32 public constant DEPLOYMENT_APPROVAL_DIGEST = bytes32(0);' "${script}"
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

printf 'validated pinned Base mainnet proposal package; final approval and broadcast remain blocked\n'
