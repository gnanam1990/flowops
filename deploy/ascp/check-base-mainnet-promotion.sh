#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
record="${FLOWOPS_ASCP_MAINNET_PROMOTION_RECORD:-${repo_root}/deployments/base-mainnet-ascp-promotion.json}"

jq -e '
  . as $record
  | .schemaVersion == 1
  and .network == "base-mainnet"
  and .chainId == 8453
  and .status == "safe-created-contracts-unapproved"
  and .deploymentAuthorized == false
  and .fundingAuthorized == false
  and .sourceCommit == null
  and .deployer == {
    address: "0x3c1daa7a6193848320e9477cbcfb7f512c0fd74b",
    expectedNonce: 1,
    mustNotDeploySafe: true
  }
  and .safe.address == "0x13e9fa8d49ee3e3b456db71d111da9b78fabd518"
  and .safe.version == "1.4.1"
  and .safe.implementation == "0x29fcb43b46531bca003ddc8fcb67ffe91900c762"
  and .safe.fallbackHandler == "0xfd0732dc9e303f09fcef3a7388ad10a83459ec99"
  and .safe.guard == "0x0000000000000000000000000000000000000000"
  and .safe.moduleGuard == "0x0000000000000000000000000000000000000000"
  and .safe.runtimeCodeHash == "0xd7d408ebcd99b2b70be43e20253d6d92a8ea8fab29bd3be7f55b10032331fb4c"
  and .safe.owners == [
    "0x0f094eec6b569c3f33033102ad3ce33eabfeb2fb",
    "0xe8405844a45c209895afe2e49be6aa2c6c6202a6",
    "0xe88872f94013e4584bceafb5d5f87da291d086d2"
  ]
  and .safe.threshold == 2
  and .safe.deploymentTransaction == "0x3a38c0b165281173fa688f8ca8aad51bad719bce9e00d0157547664affc32185"
  and .safe.deploymentBlock == 50535016
  and .safe.deploymentBlockHash == "0x711c6806692adf83641431ac833c1df61d7a44ece8f6bb82bbd30009513fdc20"
  and .safe.deploymentStatus == "success"
  and .safe.deploymentFinalized == true
  and .safe.finality == {
    verifiedAt: "2026-08-28T03:34:59Z",
    deploymentBlock: 50535016,
    rpcObservations: [
      {
        url: "https://mainnet.base.org",
        latestBlock: 50549390,
        finalizedBlock: 50548759
      },
      {
        url: "https://base-mainnet.public.blastapi.io",
        latestBlock: 50549376,
        finalizedBlock: 50548759
      }
    ]
  }
  and (.safe.finality.rpcObservations | all(.finalizedBlock >= $record.safe.deploymentBlock))
  and (.safe.verification.verifiedAt | test("^2026-08-27T19:39:20Z$"))
  and .safe.verification.latestBlock == 50535106
  and .safe.verification.finalizedBlock == 50534557
  and .safe.verification.rpcUrls == ["https://mainnet.base.org", "https://base.drpc.org"]
  and .safe.verification.safeTransactionServiceVerified == true
  and .safe.verification.ownerSetVerified == true
  and .safe.verification.thresholdVerified == true
  and .safe.verification.enabledModules == []
  and .safe.verification.safeNonce == 0
  and .safe.verification.nativeBalanceWei == "0"
  and .safe.verification.usdcBalanceAtomic == "0"
  and .safe.spendModuleEnabled == false
  and (.authorities | keys | sort == ["directoryPauser", "directoryPublisher", "registryAdmin", "spendAuthorizer"])
  and ([.authorities[]] | all(test("^0x[0-9a-f]{40}$")))
  and ([.authorities[]] | length == (unique | length))
  and ([.authorities[]] | index($record.deployer.address) == null)
  and (.organizationDomain | test("^0x[0-9a-f]{64}$"))
  and .asset == {
    address: "0x833589fcd6edb6e08f4c7c32d4f71b54bda02913",
    symbol: "USDC",
    decimals: 6,
    runtimeCodeHash: "0xa6705a10bb756b5dea144591118be77d7af0c3eee3bf2dfe2583dcb0364fefab"
  }
  and (.predictedContracts | length == 4)
  and ([.predictedContracts[].name] == ["service_directory", "agent_registry", "ascp_call_escrow", "ascp_spend_module"])
  and ([.predictedContracts[].creationNonce] == [1, 2, 3, 4])
  and ([.predictedContracts[].address] == [
    "0x2bc89b98ada8335feab04d5b7b5af6a63eb95fd1",
    "0x15332e8c8e230e8a1c05095196dac42ba8cc6906",
    "0x214cbbb2190075ba43fa6518560d37c09720e0c4",
    "0x942b83421c3ac4e1a04753e5e0208fd56cad649e"
  ])
  and .pilot == {
    maximumPerActionAtomic: "1000000",
    maximumDailyAtomic: "10000000",
    maximumAllowanceAtomic: "10000000",
    fundingEnabled: false
  }
  and (.activation | all(. == false))
  and .approvals == {
    safeDeploymentApproved: true,
    contractDeploymentApproved: false,
    activationApproved: false,
    fundingApproved: false
  }
  and (.unresolved | sort == [
    "ascp-independent-contract-review",
    "fresh-zero-fund-broadcast-approval",
    "safe-owner-control-proof",
    "signed-runtime-release-manifest",
    "two-independent-production-rpc-admissions"
  ])
' "${record}" >/dev/null

source_text="$(tr '[:upper:]' '[:lower:]' <"${repo_root}/contracts/script/DeployASCPBaseMainnet.s.sol")"
grep -Fq 'address public constant designated_deployer = address(0);' <<<"${source_text}"
grep -Fq 'uint256 public constant expected_deployer_nonce = 0;' <<<"${source_text}"
grep -Fq 'address public constant production_safe = address(0);' <<<"${source_text}"
grep -Fq 'bool public constant mainnet_broadcast_enabled = false;' <<<"${source_text}"

printf 'validated created Base mainnet Safe and unapproved ASCP contracts; no deployment or funding authorized\n'
