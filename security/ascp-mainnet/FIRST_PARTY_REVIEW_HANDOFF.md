# ASCP Base mainnet first-party review handoff

Status: first-party remediation evidence only; **not an independent contract review**.

## Immutable scope

- Review base: `060da156e75bc2925bf4d283e08f1dbbe972545e`
- Remediated source commit: `ae8ebfdfa8d1e6013888134d72610f9ab9032b53`
- Candidate SHA-256: `18aa4b01464d2b0f9c485a0ebca3e5c1412a320323a662d93e770cc92ba4f822`
- Solidity: `0.8.26`, Cancun EVM, optimizer enabled with 200 runs
- Dependencies: forge-std `77041d2ce690e692d6e03cc812b57d1ddaa4d505`, OpenZeppelin Contracts `c64a1edb67b6e3f4a15cca8909c9482ad33a02b0`

Review all source and tests for `ServiceDirectory`, `AgentRegistry`,
`ASCPCallEscrow`, `ASCPSpendModule`, `ASCPTypeHashes` and
`DeployASCPBaseMainnet` at the remediated commit. Do not review only the diff.

## First-party findings remediated

1. `ASCPCallEscrow.lockCall` accepted new USDC locks while the one-way emergency
   pause was active. The remediated contract rejects new locks while preserving
   permissionless expiry recovery for already locked calls.
2. The mainnet deployment script accepted any structurally plausible Safe. The
   remediated script now rejects drift in the exact ordered 2-of-3 owner set,
   threshold, Safe nonce, empty module list, proxy runtime code hash and
   singleton implementation before broadcast begins.

The regression suite includes a recovery-preserving pause test, a substituted
1-of-1 Safe test and fuzzed owner/threshold/nonce/singleton/module drift tests.

## Independent reviewer requirements

Reconstruct the intended authorization and accounting model independently.
Challenge constructor bindings, Safe/module call boundaries, EIP-712 domains,
nonce consumption, replay resistance, signature epochs, directory proofs,
deadline edges, daily/principal/allowance accounting, fee-on-transfer behavior,
USDC blacklist/pause recovery, reentrancy, governance workflow binding,
emergency pause behavior, deployment TOCTOU and zero-fund initial state.

The reviewer should run:

```sh
forge test
BASE_MAINNET_FORK_RPC_URL='<reviewer-controlled archive RPC>' \
  make test-ascp-mainnet-candidate
make check
```

Report exact source locations, reproduction steps, severity and disposition.
Return an immutable report with a SHA-256 digest. An unresolved high or critical
finding blocks promotion. The report digest must not be invented from this
handoff and this handoff must never populate `EXTERNAL_REVIEW_DIGEST`.
