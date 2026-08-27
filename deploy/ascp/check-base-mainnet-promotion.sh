#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
record="${FLOWOPS_ASCP_MAINNET_PROMOTION_RECORD:-${repo_root}/deployments/base-mainnet-ascp-promotion.json}"

jq -e '
  . as $record
  | .schemaVersion == 1
  and .network == "base-mainnet"
  and .chainId == 8453
  and .status == "draft-unapproved"
  and .deploymentAuthorized == false
  and .fundingAuthorized == false
  and .sourceCommit == null
  and .deployer == {
    address: "0x3c1daa7a6193848320e9477cbcfb7f512c0fd74b",
    expectedNonce: 1,
    mustNotDeploySafe: true
  }
  and (.safe.address == null)
  and (.safe.owners | length == 3)
  and (.safe.owners | length == (unique | length))
  and (.safe.owners | all(test("^0x[0-9a-f]{40}$")))
  and .safe.threshold == 2
  and .safe.deploymentTransaction == null
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
  and (.approvals | all(. == false))
  and (.unresolved | sort == [
    "ascp-independent-contract-review",
    "fresh-zero-fund-broadcast-approval",
    "safe-address-and-deployment-receipt",
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

printf 'validated unapproved Base mainnet ASCP promotion draft; no deployment or funding authorized\n'
