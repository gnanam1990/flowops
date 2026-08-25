#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
deployment_script="${repo_root}/contracts/script/DeployASCPBaseSepolia.s.sol"
primary="${BASE_SEPOLIA_RPC_URL_PRIMARY:-https://sepolia.base.org}"
secondary="${BASE_SEPOLIA_RPC_URL_SECONDARY:-https://base-sepolia-rpc.publicnode.com}"

asset="$(awk '/address public constant BASE_SEPOLIA_USDC =/ { value=$NF; sub(/;$/, "", value); print value; exit }' "${deployment_script}")"
expected_hash="$(awk '/bytes32 public constant BASE_SEPOLIA_USDC_RUNTIME_CODE_HASH =/ { getline; gsub(/[[:space:];]/, ""); print; exit }' "${deployment_script}")"
expected_hash_lower="$(printf '%s' "${expected_hash}" | tr '[:upper:]' '[:lower:]')"

case "${asset}" in
  0x????????????????????????????????????????) ;;
  *) printf 'invalid pinned Base Sepolia asset address\n' >&2; exit 1 ;;
esac
case "${expected_hash}" in
  0x????????????????????????????????????????????????????????????????) ;;
  *) printf 'invalid pinned Base Sepolia asset runtime code hash\n' >&2; exit 1 ;;
esac

rpc_host() {
  local rpc_url="$1"
  local authority host

  if [[ "${rpc_url}" != *://* ]]; then
    return 1
  fi
  authority="${rpc_url#*://}"
  authority="${authority%%[/?#]*}"
  authority="${authority##*@}"
  if [[ "${authority}" == \[* ]]; then
    host="${authority#\[}"
    host="${host%%\]*}"
  else
    host="${authority%%:*}"
  fi
  [[ -n "${host}" ]] || return 1
  printf '%s\n' "${host}" | tr '[:upper:]' '[:lower:]'
}

if [[ "$(rpc_host "${primary}")" == "$(rpc_host "${secondary}")" ]]; then
  printf 'Base Sepolia asset verification requires two distinct RPC hosts\n' >&2
  exit 1
fi

observe() {
  local name="$1"
  local rpc_url="$2"
  local attempt chain_id code_hash head

  for attempt in 1 2 3; do
    if chain_id="$(cast chain-id --rpc-url "${rpc_url}" 2>/dev/null)" \
      && code_hash="$(cast codehash "${asset}" --rpc-url "${rpc_url}" 2>/dev/null)" \
      && head="$(cast block-number --rpc-url "${rpc_url}" 2>/dev/null)"; then
      break
    fi
    if (( attempt == 3 )); then
      printf '%s Base Sepolia RPC was unavailable after three attempts\n' "${name}" >&2
      return 1
    fi
  done

  if [[ "${chain_id}" != "84532" ]]; then
    printf '%s RPC returned unexpected chain ID %s\n' "${name}" "${chain_id}" >&2
    return 1
  fi
  code_hash="$(printf '%s' "${code_hash}" | tr '[:upper:]' '[:lower:]')"
  if [[ "${code_hash}" != "${expected_hash_lower}" ]]; then
    printf '%s RPC returned unexpected asset runtime code hash %s\n' "${name}" "${code_hash}" >&2
    return 1
  fi
  printf '%s host=%s head=%s codehash=%s\n' "${name}" "$(rpc_host "${rpc_url}")" "${head}" "${code_hash}"
}

primary_observation="$(observe primary "${primary}")"
secondary_observation="$(observe secondary "${secondary}")"
printf '%s\n%s\n' "${primary_observation}" "${secondary_observation}"
printf 'canonical Base Sepolia test-USDC runtime matches the pinned deployment hash through two RPC providers\n'
