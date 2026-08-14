#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
manifest="${FLOWOPS_SECURITY_REVIEW_MANIFEST:-${repo_root}/security/call-escrow/review-manifest.json}"
contract='contracts/src/CallEscrow.sol:CallEscrow'
tmp_dir="$(mktemp -d)"
trap 'rm -rf "${tmp_dir}"' EXIT
cd "${repo_root}"

sha256_file() {
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  elif command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    printf 'no SHA-256 tool is available\n' >&2
    return 1
  fi
}

sha256_stdin() {
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 | awk '{print $1}'
  elif command -v sha256sum >/dev/null 2>&1; then
    sha256sum | awk '{print $1}'
  else
    printf 'no SHA-256 tool is available\n' >&2
    return 1
  fi
}

jq -e '
  .schemaVersion == 1
  and .packageStatus == "prepared-external-review-not-complete"
  and .network == "base-mainnet"
  and .chainId == 8453
  and (.reviewedSourceCommit | test("^[0-9a-f]{40}$"))
  and .scope.contract == "contracts/src/CallEscrow.sol:CallEscrow"
  and .scope.deploymentScript == "contracts/script/DeployCallEscrowBaseMainnet.s.sol"
  and .scope.buildConfig == "foundry.toml"
  and .scope.importedProductionFiles == [
    "lib/openzeppelin-contracts/contracts/interfaces/IERC1363.sol",
    "lib/openzeppelin-contracts/contracts/interfaces/IERC165.sol",
    "lib/openzeppelin-contracts/contracts/interfaces/IERC20.sol",
    "lib/openzeppelin-contracts/contracts/token/ERC20/IERC20.sol",
    "lib/openzeppelin-contracts/contracts/token/ERC20/utils/SafeERC20.sol",
    "lib/openzeppelin-contracts/contracts/utils/ReentrancyGuard.sol",
    "lib/openzeppelin-contracts/contracts/utils/introspection/IERC165.sol"
  ]
  and .scope.deploymentCeremony == [
    "deploy/call-escrow/audit-base-mainnet-readiness.sh",
    "deploy/call-escrow/deploy-base-mainnet-hardware.sh",
    "deploy/call-escrow/verify-base-mainnet-deployment-readonly.sh",
    "deploy/call-escrow/submit-base-mainnet-source-verification.sh"
  ]
  and (.scope.explicitlyOutOfScope | index("customer signer and wallet implementations") != null)
  and (.scope.explicitlyOutOfScope | index("durable reconciliation, ledger, and RPC admission services") != null)
  and (.sourceBindings.contractSha256 | test("^[0-9a-f]{64}$"))
  and (.sourceBindings.deploymentScriptSha256 | test("^[0-9a-f]{64}$"))
  and (.sourceBindings.foundryConfigSha256 | test("^[0-9a-f]{64}$"))
  and (.sourceBindings.abiSha256 | test("^[0-9a-f]{64}$"))
  and (.sourceBindings.storageLayoutSha256 | test("^[0-9a-f]{64}$"))
  and (.sourceBindings.methodIdentifiersSha256 | test("^[0-9a-f]{64}$"))
  and (.ceremonyBindings.finalReadinessAuditSha256 | test("^[0-9a-f]{64}$"))
  and (.ceremonyBindings.hardwareBroadcastWrapperSha256 | test("^[0-9a-f]{64}$"))
  and (.ceremonyBindings.readOnlyDeploymentVerifierSha256 | test("^[0-9a-f]{64}$"))
  and (.ceremonyBindings.sourceSubmissionWrapperSha256 | test("^[0-9a-f]{64}$"))
  and (.dependencies.forgeStdCommit | test("^[0-9a-f]{40}$"))
  and (.dependencies.openzeppelinContractsCommit | test("^[0-9a-f]{40}$"))
  and .compiler.solidity == "0.8.26+commit.8a97fa7a"
  and .compiler.optimizerEnabled == true
  and .compiler.optimizerRuns == 200
  and .compiler.evmVersion == "cancun"
  and .compiler.viaIR == false
  and .sourceRehearsal.record == "deployments/base-mainnet-source-rehearsal.json"
  and (.sourceRehearsal.standardJsonSha256 | test("^[0-9a-f]{64}$"))
  and (.sourceRehearsal.creationBytecodeHash | test("^0x[0-9a-f]{64}$"))
  and (.sourceRehearsal.deployedBytecodeTemplateHash | test("^0x[0-9a-f]{64}$"))
  and .reviewDocuments.threatModel == "docs/security/CALL_ESCROW_THREAT_MODEL.md"
  and .reviewDocuments.reviewBrief == "docs/security/CALL_ESCROW_EXTERNAL_REVIEW_BRIEF.md"
  and (.requiredTestCommands | index("forge test --match-path contracts/test/CallEscrow.t.sol") != null)
  and (.requiredTestCommands | index("forge test --match-path contracts/test/CallEscrow.invariant.t.sol") != null)
  and (.requiredTestCommands | index("make test-mainnet-final-audit") != null)
  and .knownLimitations == [
    "KL-01-DIGESTS-PROVE-BYTES-NOT-QUALITY",
    "KL-02-OWNERLESS-NO-PAUSE-RESCUE-OR-UPGRADE",
    "KL-03-UNSOLICITED-ASSET-REMAINS-UNRECOVERABLE",
    "KL-04-USDC-UPGRADE-FREEZE-BLACKLIST-DEPENDENCY",
    "KL-05-CHAIN-TIME-HALT-AND-REORG-DEPENDENCY",
    "KL-06-ANYONE-MAY-FINALIZE-PINNED-RECIPIENT",
    "KL-07-EXTERNAL-SIGNER-AND-RECONCILIATION-REQUIRE-SEPARATE-REVIEW"
  ]
  and (
    (
      .externalReview.complete == false
      and .externalReview.reviewer == null
      and .externalReview.reportSha256 == null
      and .externalReview.completedAt == null
      and .externalReview.retestComplete == false
      and .externalReview.unresolvedCritical == null
      and .externalReview.unresolvedHigh == null
    )
    or
    (
      .externalReview.complete == true
      and (.externalReview.reviewer | type == "string" and length > 0)
      and (.externalReview.reportSha256 | test("^[0-9a-f]{64}$"))
      and (.externalReview.completedAt | test("^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$"))
      and .externalReview.retestComplete == true
      and .externalReview.unresolvedCritical == 0
      and .externalReview.unresolvedHigh == 0
    )
  )
' "${manifest}" >/dev/null

reviewed_commit="$(jq -r '.reviewedSourceCommit' "${manifest}")"
git cat-file -e "${reviewed_commit}^{commit}"

bind_file() {
  local path="$1"
  local field="$2"
  local expected current reviewed
  expected="$(jq -r "${field}" "${manifest}")"
  current="$(sha256_file "${repo_root}/${path}")"
  reviewed="$(git show "${reviewed_commit}:${path}" | sha256_stdin)"
  test "${current}" = "${expected}"
  test "${reviewed}" = "${expected}"
}

bind_file 'contracts/src/CallEscrow.sol' '.sourceBindings.contractSha256'
bind_file 'contracts/script/DeployCallEscrowBaseMainnet.s.sol' '.sourceBindings.deploymentScriptSha256'
bind_file 'foundry.toml' '.sourceBindings.foundryConfigSha256'

test "$(sha256_file deploy/call-escrow/audit-base-mainnet-readiness.sh)" = \
  "$(jq -r '.ceremonyBindings.finalReadinessAuditSha256' "${manifest}")"
test "$(sha256_file deploy/call-escrow/deploy-base-mainnet-hardware.sh)" = \
  "$(jq -r '.ceremonyBindings.hardwareBroadcastWrapperSha256' "${manifest}")"
test "$(sha256_file deploy/call-escrow/verify-base-mainnet-deployment-readonly.sh)" = \
  "$(jq -r '.ceremonyBindings.readOnlyDeploymentVerifierSha256' "${manifest}")"
test "$(sha256_file deploy/call-escrow/submit-base-mainnet-source-verification.sh)" = \
  "$(jq -r '.ceremonyBindings.sourceSubmissionWrapperSha256' "${manifest}")"

test "$(git rev-parse "${reviewed_commit}:lib/forge-std")" = "$(jq -r '.dependencies.forgeStdCommit' "${manifest}")"
test "$(git rev-parse "${reviewed_commit}:lib/openzeppelin-contracts")" = "$(jq -r '.dependencies.openzeppelinContractsCommit' "${manifest}")"

forge build --force --extra-output storageLayout >/dev/null
artifact="${repo_root}/contracts/out/CallEscrow.sol/CallEscrow.json"
jq -S '.abi' "${artifact}" | sha256_stdin >"${tmp_dir}/abi.sha"
# Solidity AST identifiers are build-order metadata, not storage semantics.
# Remove them and their numeric type suffixes from both object keys and values
# before hashing the actual layout.
jq -S '
  def normalize:
    if type == "object" then
      with_entries(
        select(.key != "astId")
        | .key |= gsub("\\)[0-9]+"; ")")
        | .value |= normalize
      )
    elif type == "array" then map(normalize)
    elif type == "string" then gsub("\\)[0-9]+"; ")")
    else .
    end;
  .storageLayout | normalize
' "${artifact}" | sha256_stdin >"${tmp_dir}/storage.sha"
jq -S '.methodIdentifiers' "${artifact}" | sha256_stdin >"${tmp_dir}/methods.sha"
test "$(cat "${tmp_dir}/abi.sha")" = "$(jq -r '.sourceBindings.abiSha256' "${manifest}")"
test "$(cat "${tmp_dir}/storage.sha")" = "$(jq -r '.sourceBindings.storageLayoutSha256' "${manifest}")"
test "$(cat "${tmp_dir}/methods.sha")" = "$(jq -r '.sourceBindings.methodIdentifiersSha256' "${manifest}")"

foundry_config="$(forge config --json)"
test "$(jq -r '.solc' <<<"${foundry_config}")" = '0.8.26'
test "$(jq -r '.optimizer' <<<"${foundry_config}")" = 'true'
test "$(jq -r '.optimizer_runs' <<<"${foundry_config}")" = '200'
test "$(jq -r '.evm_version' <<<"${foundry_config}")" = 'cancun'
test "$(jq -r '.via_ir' <<<"${foundry_config}")" = 'false'

source_record="${repo_root}/$(jq -r '.sourceRehearsal.record' "${manifest}")"
jq -e --argjson package "$(cat "${manifest}")" '
  .standardJsonSha256 == $package.sourceRehearsal.standardJsonSha256
  and .creationBytecodeHash == $package.sourceRehearsal.creationBytecodeHash
  and .deployedBytecodeTemplateHash == $package.sourceRehearsal.deployedBytecodeTemplateHash
  and .sourceVerificationApproved == false
' "${source_record}" >/dev/null

test -f "${repo_root}/$(jq -r '.reviewDocuments.threatModel' "${manifest}")"
test -f "${repo_root}/$(jq -r '.reviewDocuments.reviewBrief' "${manifest}")"
if test "$(jq -r '.externalReview.complete' "${manifest}")" = 'false'; then
  grep -Fq 'UNAUDITED: Base mainnet use is prohibited' "${repo_root}/contracts/src/CallEscrow.sol"
elif grep -Fq 'UNAUDITED: Base mainnet use is prohibited' "${repo_root}/contracts/src/CallEscrow.sol"; then
  printf 'completed review package still contains the mainnet prohibition\n' >&2
  exit 1
fi

printf 'validated exact CallEscrow external-review package; external review remains incomplete\n'
