#!/usr/bin/env bash
set -euo pipefail

# Runs repeatable local evidence for the same defect classes exercised by the
# 14 external ASCP criteria. This is deliberately a rehearsal: it cannot issue
# an external acceptance signature or promote any manifest row to accepted.

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
database_url="${FLOWOPS_TEST_DATABASE_URL:?FLOWOPS_TEST_DATABASE_URL is required}"
output_root="${FLOWOPS_LOCAL_ACCEPTANCE_OUTPUT_DIR:-}"
primary_rpc="${BASE_SEPOLIA_RPC_URL_PRIMARY:-https://sepolia.base.org}"
secondary_rpc="${BASE_SEPOLIA_RPC_URL_SECONDARY:-https://base-sepolia.drpc.org}"
export ETH_RPC_TIMEOUT="${ETH_RPC_TIMEOUT:-15}"

if [[ -z "${output_root}" ]]; then
  output_root="$(mktemp -d "${TMPDIR:-/tmp}/flowops-local-acceptance.XXXXXX")"
else
  test "${output_root:0:1}" = / || {
    echo "FLOWOPS_LOCAL_ACCEPTANCE_OUTPUT_DIR must be absolute" >&2
    exit 1
  }
  if [[ -d "${output_root}" ]] && [[ -n "$(find "${output_root}" -mindepth 1 -maxdepth 1 -print -quit)" ]]; then
    echo "FLOWOPS_LOCAL_ACCEPTANCE_OUTPUT_DIR must be empty" >&2
    exit 1
  fi
  mkdir -p "${output_root}"
fi
chmod 0700 "${output_root}"

cd "${repo_root}"

run_evidence() {
  local name="$1"
  shift
  "$@" >"${output_root}/${name}.log" 2>&1
}

export FLOWOPS_TEST_DATABASE_URL="${database_url}"

run_evidence postgres-integration go test -count=1 -race -p 1 -timeout=5m \
  ./internal/controlapi ./internal/ascpkeeper ./internal/ascprails \
  ./internal/ascpevents ./internal/ascpgovernancerelay \
  ./internal/ascpsettlement ./internal/ascpleadership

run_evidence scenario-defect-classes go test -count=1 -race -p 1 -timeout=5m \
  ./internal/controlplane ./internal/ascpexecauth ./internal/ascpapproval \
  ./internal/ascpreservation ./internal/reconciliation ./internal/ascpkeeper \
  ./internal/ascpevents ./internal/ascprecovery ./internal/ascpring6 \
  ./internal/ascpsignerruntime ./internal/ascpworkflow \
  ./internal/ascpgovernancerelay ./internal/ascpassethealth \
  ./pkg/referencesigner ./pkg/safegovernance ./pkg/spendauthorization

run_evidence solidity-state-machines forge test \
  --no-match-path 'contracts/test/*Fork*.t.sol'

run_evidence external-requirements go run ./cmd/ascp-external-acceptance requirements
run_evidence activation-evidence deploy/ascp/check-base-sepolia-activation-evidence.sh

safe="$(jq -er '.safe.address' deployments/base-sepolia-ascp-external-acceptance-profile-v1.json)"
asset="$(jq -er '.asset.address' deployments/base-sepolia-ascp-v4.json)"
module="$(jq -er '.contracts[] | select(.name == "ascp_spend_module") | .address' deployments/base-sepolia-ascp-v4.json)"
escrow="$(jq -er '.contracts[] | select(.name == "ascp_call_escrow") | .address' deployments/base-sepolia-ascp-v4.json)"

observe_live() {
  local rpc="$1"
  cast chain-id --rpc-url "${rpc}"
  cast block-number --rpc-url "${rpc}"
  cast balance "${safe}" --rpc-url "${rpc}"
  cast call "${asset}" 'balanceOf(address)(uint256)' "${safe}" --rpc-url "${rpc}"
  cast call "${asset}" 'allowance(address,address)(uint256)' "${safe}" "${escrow}" --rpc-url "${rpc}"
  cast call "${safe}" 'isModuleEnabled(address)(bool)' "${module}" --rpc-url "${rpc}"
  cast call "${module}" 'escrowAllowlist(address)(bytes32)' "${escrow}" --rpc-url "${rpc}"
}

observe_live "${primary_rpc}" >"${output_root}/live-primary.log"
observe_live "${secondary_rpc}" >"${output_root}/live-secondary.log"
sed '2d' "${output_root}/live-primary.log" >"${output_root}/live-primary.canonical"
sed '2d' "${output_root}/live-secondary.log" >"${output_root}/live-secondary.canonical"
cmp -s "${output_root}/live-primary.canonical" "${output_root}/live-secondary.canonical"
rm -f "${output_root}/live-primary.canonical" "${output_root}/live-secondary.canonical"

requirements="$(cat "${output_root}/external-requirements.log")"
generated_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
source_commit="$(git rev-parse HEAD)"
source_tree_dirty=false
if [[ -n "$(git status --porcelain)" ]]; then
  source_tree_dirty=true
fi

hash_log() {
  shasum -a 256 "${output_root}/$1.log" | awk '{print $1}'
}

jq -n \
  --arg generatedAt "${generated_at}" \
  --arg sourceCommit "${source_commit}" \
  --argjson sourceTreeDirty "${source_tree_dirty}" \
  --arg requirementsHash "$(hash_log external-requirements)" \
  --arg postgresHash "$(hash_log postgres-integration)" \
  --arg scenarioHash "$(hash_log scenario-defect-classes)" \
  --arg solidityHash "$(hash_log solidity-state-machines)" \
  --arg activationHash "$(hash_log activation-evidence)" \
  --arg primaryHash "$(hash_log live-primary)" \
  --arg secondaryHash "$(hash_log live-secondary)" \
  --argjson requirements "${requirements}" '
  {
    schemaVersion: 1,
    evidenceType: "flowops.ascp-local-external-acceptance-rehearsal.v1",
    generatedAt: $generatedAt,
    sourceCommit: $sourceCommit,
    sourceTreeDirty: $sourceTreeDirty,
    classification: "LOCAL_REHEARSAL_ONLY",
    externalAcceptanceGranted: false,
    commands: [
      {name:"postgres-integration", sha256:$postgresHash},
      {name:"scenario-defect-classes", sha256:$scenarioHash},
      {name:"solidity-state-machines", sha256:$solidityHash},
      {name:"external-requirements", sha256:$requirementsHash},
      {name:"activation-evidence", sha256:$activationHash},
      {name:"live-primary", sha256:$primaryHash},
      {name:"live-secondary", sha256:$secondaryHash}
    ],
    criteria: ($requirements | to_entries | map({
      id: .key,
      localStatus: "REHEARSED",
      externalStatus: "STILL_REQUIRED",
      assertions: .value
    }))
  }' >"${output_root}/report.json"

jq -e '
  .classification == "LOCAL_REHEARSAL_ONLY" and
  .externalAcceptanceGranted == false and
  (.criteria | length) == 14 and
  ([.criteria[].externalStatus] | all(. == "STILL_REQUIRED"))
' "${output_root}/report.json" >/dev/null

printf 'local ASCP external-acceptance rehearsal passed; evidence: %s/report.json\n' "${output_root}"
