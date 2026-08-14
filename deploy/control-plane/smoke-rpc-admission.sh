#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
providers='[{"name":"rpc_alpha","url":"https://alpha.vendor.example/v1/smoke-secret-alpha"},{"name":"rpc_beta","url":"https://beta.vendor.example/v1/smoke-secret-beta"}]'
admission='{"schemaVersion":1,"providers":[{"name":"rpc_alpha","operator":"vendor_alpha","failureDomain":"vendor_alpha_global","serviceTier":"paid","productionEligible":true},{"name":"rpc_beta","operator":"vendor_beta","failureDomain":"vendor_beta_global","serviceTier":"paid","productionEligible":true}]}'

run_check() {
  FLOWOPS_BASE_CHAIN_ID=8453 \
    FLOWOPS_BASE_RPC_PROVIDERS_JSON="$1" \
    FLOWOPS_BASE_RPC_ADMISSION_JSON="$2" \
    go run "${repo_root}/cmd/rpc-admission-check"
}

output="$(run_check "${providers}" "${admission}")"
jq -e '.chainId == 8453 and .providerCount == 2 and .productionAdmitted == true' <<<"${output}" >/dev/null
if grep -Eq 'smoke-secret|vendor\.example' <<<"${output}"; then
  printf 'RPC admission output exposed a secret-bearing URL\n' >&2
  exit 1
fi

expect_rejected() {
  local name="$1"
  local provider_json="$2"
  local admission_json="$3"
  if run_check "${provider_json}" "${admission_json}" >/dev/null 2>&1; then
    printf 'RPC admission accepted unsafe configuration: %s\n' "${name}" >&2
    exit 1
  fi
}

expect_rejected public-endpoint "${providers/alpha.vendor.example/mainnet.base.org}" "${admission}"
expect_rejected shared-operator "${providers}" "${admission/vendor_beta/vendor_alpha}"
expect_rejected shared-failure-domain "${providers}" "${admission/vendor_beta_global/vendor_alpha_global}"
expect_rejected free-tier "${providers}" "${admission/\"serviceTier\":\"paid\"/\"serviceTier\":\"free\"}"
expect_rejected ineligible "${providers}" "${admission/\"productionEligible\":true/\"productionEligible\":false}"

printf 'Base mainnet production RPC admission smoke passed without network access or secret output\n'
