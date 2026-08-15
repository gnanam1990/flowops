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
  and .status == "blocked-no-deployment"
  and .proposalDocument == "docs/proposals/FLOWOPS_BASE_MAINNET_EXPERIMENTAL_ANCHOR_V1.md"
  and .proposalDigest == null
  and .sourceCommit == null
  and .designatedDeployer == null
  and .candidateDeployer.address == "0xE8405844a45C209895afE2e49be6aA2C6C6202a6"
  and .candidateDeployer.signerClass == "software-eoa-proposal-only"
  and .candidateDeployer.productionUseProhibited == true
  and .candidateDeployer.observedCode == "0x"
  and .candidateDeployer.observedLatestNonce == 0
  and .candidateDeployer.observedPendingNonce == 0
  and .candidateDeployer.observedBalanceWei == "307657574152182"
  and .candidateDeployer.expectedCreateAddressAtObservedNonce == "0x524A95082dAD59fd8bf18FA27F89E3f55202eEcf"
  and (.candidateDeployer.observedAt | fromdateiso8601 > 0)
  and (.candidateDeployer.observers | sort == ["base-rpc.publicnode.com", "mainnet.base.org"])
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

grep -Fq 'address public constant DESIGNATED_DEPLOYER = address(0);' "${script}"
grep -Fq 'bytes32 public constant PROPOSAL_DIGEST = bytes32(0);' "${script}"
grep -Fq 'bytes20 public constant SOURCE_COMMIT = bytes20(0);' "${script}"
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

printf 'validated blocked Base mainnet proposal anchor; no deployment or funding authorized\n'
