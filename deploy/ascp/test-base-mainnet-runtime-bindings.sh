#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
validator="${repo_root}/deploy/ascp/check-base-mainnet-runtime-bindings.sh"

"${validator}" >/dev/null

tmp_dir="$(mktemp -d)"
trap 'rm -rf "${tmp_dir}"' EXIT

jq '.contracts[0].address = "0x1111111111111111111111111111111111111111"' \
  "${repo_root}/apps/dashboard/app/mainnet/ascp-mainnet-deployment.json" > "${tmp_dir}/dashboard.json"
if FLOWOPS_ASCP_MAINNET_DASHBOARD_RECORD="${tmp_dir}/dashboard.json" "${validator}" >/dev/null 2>&1; then
  echo "validator accepted a substituted dashboard contract" >&2
  exit 1
fi

sed 's/FLOWOPS_ASCP_GOVERNANCE_FROM_BLOCK=50557746/FLOWOPS_ASCP_GOVERNANCE_FROM_BLOCK=50557923/' \
  "${repo_root}/deploy/control-plane/base-mainnet-ascp-deployed-inactive.env.example" > "${tmp_dir}/runtime.env"
if FLOWOPS_ASCP_MAINNET_RUNTIME_BINDINGS="${tmp_dir}/runtime.env" "${validator}" >/dev/null 2>&1; then
  echo "validator accepted a late governance start block" >&2
  exit 1
fi

echo "Base mainnet runtime binding negative tests passed"
