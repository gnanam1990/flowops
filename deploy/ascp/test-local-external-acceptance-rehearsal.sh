#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
runner="${repo_root}/deploy/ascp/run-local-external-acceptance-rehearsal.sh"

bash -n "${runner}"
grep -Fq 'classification: "LOCAL_REHEARSAL_ONLY"' "${runner}"
grep -Fq 'externalAcceptanceGranted: false' "${runner}"
grep -Fq 'externalStatus: "STILL_REQUIRED"' "${runner}"
grep -Fq '(.criteria | length) == 14' "${runner}"
grep -Fq 'FLOWOPS_LOCAL_ACCEPTANCE_OUTPUT_DIR must be empty' "${runner}"
grep -Fq 'sourceTreeDirty: $sourceTreeDirty' "${runner}"

printf 'local external-acceptance rehearsal boundary checks passed\n'
