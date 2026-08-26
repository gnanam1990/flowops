#!/usr/bin/env bash
set -euo pipefail

# Confirms the live Base Sepolia graph is still at the exact, funding-disabled
# predecessor required for the first ServiceDirectory proposal. Read-only only.
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
deployment_record="${repo_root}/deployments/base-sepolia-ascp-v4.json"
primary_rpc="${BASE_SEPOLIA_RPC_URL_PRIMARY:-https://sepolia.base.org}"
secondary_rpc="${BASE_SEPOLIA_RPC_URL_SECONDARY:-https://base-sepolia-rpc.publicnode.com}"
zero_digest='0x0000000000000000000000000000000000000000000000000000000000000000'

cd "${repo_root}"
deploy/ascp/verify-base-sepolia-activation-readonly.sh >/dev/null

directory="$(jq -er '.contracts[] | select(.name == "service_directory") | .address' "${deployment_record}")"
safe="$(jq -er '.safe.address' "${deployment_record}")"
org_domain="$(jq -er '.organizationDomain' "${deployment_record}")"
publisher="$(jq -er '.authorities.directoryPublisher' "${deployment_record}")"
pauser="$(jq -er '.authorities.directoryPauser' "${deployment_record}")"

rpc_host() {
  sed -E 's#^[a-zA-Z]+://([^/:]+).*#\1#' <<<"$1" | tr '[:upper:]' '[:lower:]'
}

test "$(rpc_host "${primary_rpc}")" != "$(rpc_host "${secondary_rpc}")"

observe_provider() {
  local rpc_url="$1"
  local head
  test "$(cast chain-id --rpc-url "${rpc_url}")" = '84532'
  head="$(cast block-number --rpc-url "${rpc_url}")"
  test "$(cast call --rpc-url "${rpc_url}" "${directory}" 'governor()(address)' | tr '[:upper:]' '[:lower:]')" = "${safe}"
  test "$(cast call --rpc-url "${rpc_url}" "${directory}" 'orgDomain()(bytes32)' | tr '[:upper:]' '[:lower:]')" = "${org_domain}"
  test "$(cast call --rpc-url "${rpc_url}" "${directory}" 'directoryPublisher()(address)' | tr '[:upper:]' '[:lower:]')" = "${publisher}"
  test "$(cast call --rpc-url "${rpc_url}" "${directory}" 'directoryPublisherEpoch()(uint64)')" = '1'
  test "$(cast call --rpc-url "${rpc_url}" "${directory}" 'pauser()(address)' | tr '[:upper:]' '[:lower:]')" = "${pauser}"
  test "$(cast call --rpc-url "${rpc_url}" "${directory}" 'pauserEpoch()(uint64)')" = '1'
  test "$(cast call --rpc-url "${rpc_url}" "${directory}" 'currentVersion()(uint64)')" = '0'
  test "$(cast call --rpc-url "${rpc_url}" "${directory}" 'currentRoot()(bytes32)')" = "${zero_digest}"
  test "$(cast call --rpc-url "${rpc_url}" "${directory}" 'latestProposalHash(uint64)(bytes32)' 1)" = "${zero_digest}"
  printf '%s:%s:%s:%s:%s:%s:%s\n' "${directory}" "${safe}" "${org_domain}" "${publisher}" "${pauser}" "${zero_digest}" "${head}"
}

primary_observation="$(mktemp -t flowops-directory-v1-primary.XXXXXX)"
secondary_observation="$(mktemp -t flowops-directory-v1-secondary.XXXXXX)"
trap 'rm -f "${primary_observation}" "${secondary_observation}" "${primary_observation}.canonical" "${secondary_observation}.canonical"' EXIT
observe_provider "${primary_rpc}" >"${primary_observation}"
observe_provider "${secondary_rpc}" >"${secondary_observation}"
cut -d: -f1-6 "${primary_observation}" >"${primary_observation}.canonical"
cut -d: -f1-6 "${secondary_observation}" >"${secondary_observation}.canonical"
cmp -s "${primary_observation}.canonical" "${secondary_observation}.canonical"

printf 'Base Sepolia ServiceDirectory v1 predecessor and funding-disabled activation boundary verified read-only\n'
