#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
record="${FLOWOPS_MAINNET_SOURCE_REHEARSAL_RECORD:-${repo_root}/deployments/base-mainnet-source-rehearsal.json}"
contract='contracts/src/CallEscrow.sol:CallEscrow'
asset='0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913'
release_window='3600'
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

forge build --force --build-info >/dev/null

constructor_args="$(cast abi-encode 'constructor(address,uint256)' "${asset}" "${release_window}")"
creation_bytecode="$(forge inspect "${contract}" bytecode)"
deployed_template="$(forge inspect "${contract}" deployedBytecode)"
compiler="$(forge inspect "${contract}" metadata | jq -er '.compiler.version')"
standard_json="${tmp_dir}/standard-input.json"
build_info=''
while IFS= read -r candidate; do
  if jq -e '
    .input.sources["contracts/src/CallEscrow.sol"] != null
    and .output.contracts["contracts/src/CallEscrow.sol"].CallEscrow != null
  ' "${candidate}" >/dev/null; then
    build_info="${candidate}"
    break
  fi
done < <(find "${repo_root}/contracts/out/build-info" -type f -name '*.json' -print | sort)
test -n "${build_info}"

# Canonicalize the exact portable compiler input Foundry used. Foundry adds
# checkout-absolute allow/base/include paths around the Solidity standard JSON;
# those transport fields are machine-specific and do not change the embedded
# sources, remappings, settings, or compiler output we bind below. Unlike
# `forge verify-contract --show-standard-json-input`, this does not initialize
# an explorer verifier or require its network metadata merely to prove the
# local build inputs.
jq -S '.input | del(.allowPaths, .basePath, .includePaths)' "${build_info}" >"${standard_json}"

source_sha="$(sha256_file "${repo_root}/contracts/src/CallEscrow.sol")"
config_sha="$(sha256_file "${repo_root}/foundry.toml")"
creation_hash="$(printf '%s' "${creation_bytecode}" | cast keccak)"
creation_input_hash="$(printf '%s' "${creation_bytecode}${constructor_args#0x}" | cast keccak)"
deployed_template_hash="$(printf '%s' "${deployed_template}" | cast keccak)"
standard_json_sha="$(sha256_file "${standard_json}")"
forge_std_commit="$(git -C "${repo_root}" rev-parse HEAD:lib/forge-std)"
openzeppelin_commit="$(git -C "${repo_root}" rev-parse HEAD:lib/openzeppelin-contracts)"

jq -e \
  --arg source_sha "${source_sha}" \
  --arg config_sha "${config_sha}" \
  --arg forge_std_commit "${forge_std_commit}" \
  --arg openzeppelin_commit "${openzeppelin_commit}" \
  --arg compiler "${compiler}" \
  --arg constructor_args "${constructor_args}" \
  --arg creation_hash "${creation_hash}" \
  --arg creation_input_hash "${creation_input_hash}" \
  --arg deployed_template_hash "${deployed_template_hash}" \
  --arg standard_json_sha "${standard_json_sha}" '
  .schemaVersion == 1
  and .network == "base-mainnet"
  and .chainId == 8453
  and .status == "rehearsed-not-approved"
  and .contract == "contracts/src/CallEscrow.sol:CallEscrow"
  and .sourcePath == "contracts/src/CallEscrow.sol"
  and .sourceSha256 == $source_sha
  and .foundryConfigSha256 == $config_sha
  and .dependencies.forgeStdCommit == $forge_std_commit
  and .dependencies.openzeppelinContractsCommit == $openzeppelin_commit
  and .compiler == $compiler
  and .optimizerEnabled == true
  and .optimizerRuns == 200
  and .evmVersion == "cancun"
  and (.constructor.asset | ascii_downcase) == "0x833589fcd6edb6e08f4c7c32d4f71b54bda02913"
  and .constructor.optimisticReleaseWindowSeconds == 3600
  and .constructor.encodedArguments == $constructor_args
  and .creationBytecodeHash == $creation_hash
  and .creationInputHash == $creation_input_hash
  and .deployedBytecodeTemplateHash == $deployed_template_hash
  and .standardJsonSha256 == $standard_json_sha
  and .standardJsonSource == "portable canonical Foundry build-info input"
  and .verificationTarget == "BaseScan"
  and .sourceVerificationApproved == false
  and (.notes | contains("immutable placeholders"))
' "${record}" >/dev/null

jq -e '
  .language == "Solidity"
  and .version == "0.8.26"
  and .settings.optimizer.enabled == true
  and .settings.optimizer.runs == 200
  and .settings.evmVersion == "cancun"
  and .settings.viaIR == false
  and .sources["contracts/src/CallEscrow.sol"] != null
' "${standard_json}" >/dev/null

jq -e '
  .solcVersion == "0.8.26"
  and .language == "Solidity"
  and .output.contracts["contracts/src/CallEscrow.sol"].CallEscrow.evm.bytecode.object != null
  and .output.contracts["contracts/src/CallEscrow.sol"].CallEscrow.evm.deployedBytecode.object != null
' "${build_info}" >/dev/null

printf 'validated deterministic Base mainnet source-verification rehearsal; no Base RPC or explorer submission performed\n'
