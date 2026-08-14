#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source_validator="${repo_root}/deploy/call-escrow/check-base-mainnet-source-rehearsal.sh"
hardware_validator="${repo_root}/deploy/call-escrow/check-base-mainnet-hardware-ceremony.sh"
broadcast_wrapper="${repo_root}/deploy/call-escrow/deploy-base-mainnet-hardware.sh"
readonly_verifier="${repo_root}/deploy/call-escrow/verify-base-mainnet-deployment-readonly.sh"
submit_verifier="${repo_root}/deploy/call-escrow/submit-base-mainnet-source-verification.sh"
source_record="${repo_root}/deployments/base-mainnet-source-rehearsal.json"
promotion_record="${repo_root}/deployments/base-mainnet-promotion.json"
tmp_dir="$(mktemp -d)"
trap 'rm -rf "${tmp_dir}"' EXIT

bash -n "${source_validator}" "${hardware_validator}" "${broadcast_wrapper}" "${readonly_verifier}" "${submit_verifier}"
"${source_validator}" >/dev/null
"${hardware_validator}" >/dev/null

expect_source_rejected() {
  local name="$1"
  local mutation="$2"
  local candidate="${tmp_dir}/source-${name}.json"
  jq "${mutation}" "${source_record}" >"${candidate}"
  if FLOWOPS_MAINNET_SOURCE_REHEARSAL_RECORD="${candidate}" "${source_validator}" >/dev/null 2>&1; then
    printf 'source rehearsal accepted unsafe mutation: %s\n' "${name}" >&2
    exit 1
  fi
}

expect_source_rejected source-hash '.sourceSha256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"'
expect_source_rejected constructor '.constructor.optimisticReleaseWindowSeconds = 3599'
expect_source_rejected creation-input '.creationInputHash = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"'
expect_source_rejected standard-json '.standardJsonSha256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"'
expect_source_rejected premature-approval '.sourceVerificationApproved = true'

expect_hardware_rejected() {
  local name="$1"
  local mutation="$2"
  local candidate="${tmp_dir}/hardware-${name}.json"
  jq "${mutation}" "${promotion_record}" >"${candidate}"
  if FLOWOPS_MAINNET_PROMOTION_RECORD="${candidate}" "${hardware_validator}" >/dev/null 2>&1; then
    printf 'hardware ceremony accepted unsafe mutation: %s\n' "${name}" >&2
    exit 1
  fi
}

expect_hardware_rejected invented-deployer '.signing.deployer = "0x1111111111111111111111111111111111111111"'
expect_hardware_rejected software-wallet '.signing.walletType = "keystore"'
expect_hardware_rejected invented-review '.externalReviewSha256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"'
expect_hardware_rejected premature-source-approval '.sourceVerification.approved = true'
expect_hardware_rejected premature-broadcast '.broadcastApproval.approved = true'
expect_hardware_rejected invented-nonce '.broadcastApproval.expectedDeployerNonce = "0"'

if grep -Eq -- '--private-key|--account|--keystore|--interactive|--rpc-url' "${broadcast_wrapper}"; then
  printf 'hardware wrapper contains a raw-key, software-keystore, or argument-level RPC path\n' >&2
  exit 1
fi
if grep -Eq -- 'sendRawTransaction|cast send|--broadcast|verify-contract' "${readonly_verifier}"; then
  printf 'read-only deployment verifier contains a write or source-submission path\n' >&2
  exit 1
fi
if ! grep -Fq 'FLOWOPS_SUBMIT_SOURCE_VERIFICATION=true' "${submit_verifier}"; then
  printf 'source submission lacks a dedicated explicit approval gate\n' >&2
  exit 1
fi
if grep -Eq -- '--rpc-url|--etherscan-api-key' "${broadcast_wrapper}" "${readonly_verifier}" "${submit_verifier}"; then
  printf 'ceremony exposes secret RPC or explorer credentials in process arguments\n' >&2
  exit 1
fi
if grep -Eq -- 'FLOWOPS_MAINNET_(PROMOTION|READINESS)_RECORD' "${broadcast_wrapper}" "${readonly_verifier}" "${submit_verifier}"; then
  printf 'production ceremony permits an unreviewed record-path override\n' >&2
  exit 1
fi
if ! grep -Fq 'env -u FLOWOPS_MAINNET_SOURCE_REHEARSAL_RECORD' "${broadcast_wrapper}"; then
  printf 'hardware wrapper does not clear the source-rehearsal test override\n' >&2
  exit 1
fi
if ! grep -Fq 'env -u FLOWOPS_SECURITY_REVIEW_MANIFEST' "${broadcast_wrapper}" || \
  ! grep -Fq 'security/call-escrow/check-review-package.sh' "${broadcast_wrapper}"; then
  printf 'hardware wrapper does not validate the canonical completed security-review package\n' >&2
  exit 1
fi
if test "$(grep -Fc 'FOUNDRY_ETH_RPC_URL="${BASE_MAINNET_RPC_URL_PRIMARY}" forge script' "${broadcast_wrapper}")" -ne 2; then
  printf 'hardware simulation and broadcast are not both bound to the production RPC environment\n' >&2
  exit 1
fi
if ! grep -Fq 'jq -s -e --argjson maximum' "${broadcast_wrapper}"; then
  printf 'hardware simulation does not parse Foundry multi-document JSON safely\n' >&2
  exit 1
fi
if ! grep -Fq 'estimated_total_gas_used' "${broadcast_wrapper}" || \
  test "$(grep -Fc -- '--gas-estimate-multiplier 130' "${broadcast_wrapper}")" -ne 2; then
  printf 'hardware simulation does not bind Foundry total gas estimate to the approved cap\n' >&2
  exit 1
fi
if test "$(grep -Fc 'require_approved_nonce "${BASE_MAINNET_RPC_URL_' "${broadcast_wrapper}")" -ne 4; then
  printf 'hardware wrapper does not check latest and pending nonce quorum before and after simulation\n' >&2
  exit 1
fi
if ! grep -Fq 'set -o noclobber' "${broadcast_wrapper}" || grep -Eq -- '--resume|rm .*attempt' "${broadcast_wrapper}"; then
  printf 'hardware wrapper lacks a durable one-shot approval seal\n' >&2
  exit 1
fi

if FLOWOPS_SUBMIT_SOURCE_VERIFICATION=false "${submit_verifier}" >/dev/null 2>&1; then
  printf 'source submission accepted an explicit refusal\n' >&2
  exit 1
fi
if env -u FLOWOPS_SUBMIT_SOURCE_VERIFICATION "${submit_verifier}" >/dev/null 2>&1; then
  printf 'source submission proceeded without a dedicated approval\n' >&2
  exit 1
fi
if FLOWOPS_SUBMIT_SOURCE_VERIFICATION=true "${submit_verifier}" >/dev/null 2>&1; then
  printf 'blocked readiness record permitted source submission\n' >&2
  exit 1
fi

if FLOWOPS_HARDWARE_WALLET=ledger \
  FLOWOPS_EXPLICIT_BROADCAST_APPROVAL_SHA256="$(printf 'a%.0s' {1..64})" \
  BASE_MAINNET_RPC_URL_PRIMARY='https://primary.invalid/secret' \
  BASE_MAINNET_RPC_URL_SECONDARY='https://secondary.invalid/secret' \
  FLOWOPS_BASE_RPC_PROVIDERS_JSON='[]' \
  FLOWOPS_BASE_RPC_ADMISSION_JSON='{}' \
  "${broadcast_wrapper}" >/dev/null 2>&1; then
  printf 'blocked ceremony reached the mainnet broadcast path\n' >&2
  exit 1
fi

printf 'mainnet hardware-deployer and source-verification gates rejected all unsafe mutations\n'
