#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
validator="${repo_root}/security/call-escrow/check-review-package.sh"
manifest="${repo_root}/security/call-escrow/review-manifest.json"
tmp_dir="$(mktemp -d)"
trap 'rm -rf "${tmp_dir}"' EXIT
cd "${repo_root}"

bash -n "${validator}"
"${validator}" >/dev/null

expect_rejected() {
  local name="$1"
  local mutation="$2"
  local candidate="${tmp_dir}/${name}.json"
  jq "${mutation}" "${manifest}" >"${candidate}"
  if FLOWOPS_SECURITY_REVIEW_MANIFEST="${candidate}" "${validator}" >/dev/null 2>&1; then
    printf 'security-review package accepted unsafe mutation: %s\n' "${name}" >&2
    exit 1
  fi
}

expect_rejected false-completion '.externalReview.complete = true'
expect_rejected invented-reviewer '.externalReview.reviewer = "unverified reviewer"'
expect_rejected invented-report '.externalReview.reportSha256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"'
expect_rejected wrong-source '.sourceBindings.contractSha256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"'
expect_rejected wrong-commit '.reviewedSourceCommit = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"'
expect_rejected missing-contract-scope '.scope.contract = "contracts/src/Other.sol:Other"'
expect_rejected removed-out-of-scope '.scope.explicitlyOutOfScope -= ["customer signer and wallet implementations"]'
expect_rejected removed-limitation '.knownLimitations -= ["KL-04-USDC-UPGRADE-FREEZE-BLACKLIST-DEPENDENCY"]'
expect_rejected wrong-compiler '.compiler.solidity = "0.8.27"'
expect_rejected wrong-abi '.sourceBindings.abiSha256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"'
expect_rejected changed-ceremony '.ceremonyBindings.hardwareBroadcastWrapperSha256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"'
expect_rejected changed-final-audit '.ceremonyBindings.finalReadinessAuditSha256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"'
expect_rejected premature-source-approval '.sourceRehearsal.creationBytecodeHash = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"'

grep -Fq 'UNAUDITED: Base mainnet use is prohibited' contracts/src/CallEscrow.sol
forge lint contracts/src/CallEscrow.sol --severity high med --deny warnings
forge lint contracts/script/DeployCallEscrowBaseMainnet.s.sol --severity high med --deny warnings
forge test --match-path contracts/test/CallEscrow.t.sol
forge test --match-path contracts/test/CallEscrow.invariant.t.sol

printf 'security-review package rejected all unsafe mutations; contract remains explicitly unaudited\n'
