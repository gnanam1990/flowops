#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
audit="${repo_root}/deploy/call-escrow/audit-base-mainnet-readiness.sh"
canonical_readiness="${repo_root}/deployments/base-mainnet-readiness.json"
canonical_promotion="${repo_root}/deployments/base-mainnet-promotion.json"
canonical_source="${repo_root}/deployments/base-mainnet-source-rehearsal.json"
canonical_review="${repo_root}/security/call-escrow/review-manifest.json"
hardware_wrapper="${repo_root}/deploy/call-escrow/deploy-base-mainnet-hardware.sh"
tmp_dir="$(mktemp -d)"
trap 'rm -rf "${tmp_dir}"' EXIT

bash -n "${audit}" "${hardware_wrapper}"
report="$(${audit} --report)"
jq -e '
  .schemaVersion == 1
  and .network == "base-mainnet"
  and .chainId == 8453
  and .decision == "BLOCKED"
  and .deploymentAuthorized == false
  and .fundingAuthorized == false
  and (.implementationEvidence | length == 7)
  and (.blockers | length == 12)
  and ([.blockers[].id] | length == (unique | length))
  and ([.blockers[].id] | index("external-security-review") != null)
  and ([.blockers[].id] | index("funded-sepolia-signer-proof") != null)
  and ([.blockers[].id] | index("explicit-zero-fund-broadcast-approval") != null)
' <<<"${report}" >/dev/null

if "${audit}" --require-ready >/dev/null 2>&1; then
  printf 'blocked mainnet audit reported ready\n' >&2
  exit 1
fi
if "${audit}" --unknown >/dev/null 2>&1; then
  printf 'mainnet audit accepted an unknown mode\n' >&2
  exit 1
fi
if grep -Eq -- '(^|[[:space:]])(cast|curl|wget)([[:space:]]|$)|forge script|--broadcast|sendRawTransaction' "${audit}"; then
  printf 'mainnet audit contains a network or write primitive\n' >&2
  exit 1
fi
if grep -Eq -- 'BASE_MAINNET_RPC_URL|FLOWOPS_BASE_RPC_PROVIDERS_JSON' "${audit}"; then
  printf 'mainnet audit reads a secret-bearing production variable\n' >&2
  exit 1
fi

expect_rejected() {
  local name="$1"
  local source_path="$2"
  local variable="$3"
  local mutation="$4"
  local candidate="${tmp_dir}/${name}.json"
  jq "${mutation}" "${source_path}" >"${candidate}"
  if env "${variable}=${candidate}" "${audit}" --report >/dev/null 2>&1; then
    printf 'aggregate audit accepted unsafe mutation: %s\n' "${name}" >&2
    exit 1
  fi
}

expect_rejected false-review "${canonical_review}" FLOWOPS_MAINNET_AUDIT_REVIEW_MANIFEST \
  '.externalReview.complete = true'
expect_rejected invented-deployer "${canonical_promotion}" FLOWOPS_MAINNET_AUDIT_PROMOTION_RECORD \
  '.signing.deployer = "0x1111111111111111111111111111111111111111"'
expect_rejected premature-source-approval "${canonical_source}" FLOWOPS_MAINNET_AUDIT_SOURCE_RECORD \
  '.sourceVerificationApproved = true'
expect_rejected enabled-funding "${canonical_readiness}" FLOWOPS_MAINNET_AUDIT_READINESS_RECORD \
  '.pilot.fundingEnabled = true'

for variable in \
  FLOWOPS_MAINNET_AUDIT_READINESS_RECORD \
  FLOWOPS_MAINNET_AUDIT_PROMOTION_RECORD \
  FLOWOPS_MAINNET_AUDIT_SOURCE_RECORD \
  FLOWOPS_MAINNET_AUDIT_REVIEW_MANIFEST \
  FLOWOPS_MAINNET_READINESS_RECORD \
  FLOWOPS_MAINNET_PROMOTION_RECORD \
  FLOWOPS_MAINNET_SOURCE_REHEARSAL_RECORD \
  FLOWOPS_SECURITY_REVIEW_MANIFEST; do
  grep -Fq -- "-u ${variable}" "${hardware_wrapper}"
done
grep -Fq 'audit-base-mainnet-readiness.sh" --require-ready' "${hardware_wrapper}"

printf 'final Base mainnet audit is internally consistent and refuses all current blockers\n'
