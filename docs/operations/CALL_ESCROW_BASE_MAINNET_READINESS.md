# CallEscrow Base Mainnet Readiness

Status: blocked; no Base mainnet deployment exists
Mainnet funds: prohibited

This runbook prepares a reviewed deployment package without weakening the
current mainnet prohibition. It does not authorize a broadcast, a token
approval, or any USDC funding.

## Current structural stop

`contracts/script/DeployCallEscrowBaseMainnet.s.sol` pins:

- Base mainnet chain ID `8453`;
- Circle native USDC on Base;
- a 3,600-second optimistic release window;
- `DESIGNATED_DEPLOYER = address(0)`;
- `EXTERNAL_REVIEW_DIGEST = bytes32(0)`; and
- `MAINNET_BROADCAST_ENABLED = false`.

The first three values define the intended deployment. The last three make the
committed script refuse every broadcast attempt. Do not bypass the gate with a
second unreviewed script, `forge create`, a raw transaction, or an environment
variable.

The canonical blocked record is
`deployments/base-mainnet-readiness.json`. It intentionally contains no
contract address, transaction hash, block, reviewed source commit, or deployer.
It records the proposed but still unfunded 1 USDC per-call and 10 USDC
per-customer pilot profile. Full enforcement remains false because the escrow
signer path has not yet completed its funded, canonically reconciled Sepolia
proof.

The external-review package is now prepared at
`security/call-escrow/review-manifest.json`. It binds the exact contract and
deployment-ceremony scope, compiler/dependencies, ABI/storage/method surface,
threat model, tests, and known limitations to reviewed source commit
`808caa4c9905334c52d6f237863f5ff33b11ffb0`. This is reviewer input, not a
completed audit. Reviewer identity, report digest, retest, and finding counts
remain empty; the contract keeps its `UNAUDITED` mainnet prohibition.

Two additional records make the future ceremony reviewable without filling in
facts that do not exist yet:

- `deployments/base-mainnet-source-rehearsal.json` binds the exact source,
  Foundry configuration, dependency commits, compiler settings, constructor
  encoding, creation bytecode, and contract-scoped production-import standard
  JSON compiler input. Test-only and unrelated-script edits cannot alter that
  source-verification hash. It is a local rehearsal and is not explorer
  verification.
- `deployments/base-mainnet-promotion.json` is intentionally
  `blocked-unassigned`. Its hardware-wallet identity, ownership attestation,
  external-review digest, source-verification approval, and gas-capped
  broadcast approval are all empty.

## Stage 1: evidence and production dependencies

Complete all items in a separate promotion package:

1. Independent security review of the exact source commit and compiler
   settings. Store the report's SHA-256 digest and a non-secret reference in
   the repository; do not commit a confidential report without approval.
2. Specialist legal review of the ownerless escrow and delivery-assurance
   posture.
3. Designate a new production signing identity with hardware-backed recovery
   and documented operators. Do not reuse either Sepolia MetaMask wallet.
4. Wire escrow FUND, ACKNOWLEDGE, DELIVERY, RELEASE, and REFUND events into the
   durable intent journal, ledger correction, and reorg workflow.
5. Complete one funded, capped reference-signer execution on Base Sepolia and
   reconcile it through the production-shaped worker.
6. Select two operationally independent paid Base RPC providers. Store their
   credential-bearing URLs only in the deployment secret manager. Create the
   separate URL-free schema-v1 admission record that binds each runtime name to
   a distinct reviewed operator and failure domain, then run
   `make smoke-rpc-admission`. The code gate being present is not evidence that
   providers have been selected.
7. Rehearse source verification, constructor decoding, runtime bytecode hashing,
   immutable getter reads, and dual-provider receipt confirmation. Measure and
   approve a positive deployment-confirmation depth; do not copy an unmeasured
   threshold from another chain.
8. Complete the separately approved funded Sepolia proof for the implemented
   exact-allowance-check and exact-fund-calldata customer signer. Local and
   no-funds tests are not evidence of a canonical funded outcome.

## Stage 2: local and read-only verification

Run the deterministic checks:

```bash
make test-mainnet-readiness
make test-mainnet-deployer-verification
make test-security-review-package
make smoke-rpc-admission
make smoke-escrow-mainnet-readiness
make check
```

The smoke target is read-only. Set two non-secret public endpoints for a manual
integration drill, or credential-bearing production endpoints through the
secret manager:

```bash
export BASE_MAINNET_RPC_URL_PRIMARY="https://mainnet.base.org"
export BASE_MAINNET_RPC_URL_SECONDARY="https://base-rpc.publicnode.com"
make smoke-escrow-mainnet-readiness
```

It verifies chain identity, live USDC bytecode, token metadata, runtime code
hash agreement, bounded head skew, and a shared canonical anchor. It then
deploys the test-only promoted harness inside an ephemeral Base mainnet fork to
exercise the constructor path. It never submits a transaction to Base. Any
contract address printed by the fork test is synthetic and must not be copied
into the deployment registry. Public endpoints passing this drill does not
make them production observers.

The primary and secondary URLs must have different normalized hostnames; the
smoke refuses duplicate-host configurations before contacting either endpoint.
The script passes URLs to `cast` through its environment so credential-bearing
values do not appear in command arguments or normal output. Distinct hostnames
are only a minimum check—operators must still establish organizational and
infrastructure independence before promotion.

## Stage 3: promotion PR

Only after Stage 1 is evidenced may a separate PR:

- designate the production deployer in the script and readiness registry;
- set the external-review digest;
- set `MAINNET_BROADCAST_ENABLED = true`;
- pin the reviewed source commit without changing the proposed pilot profile;
- add a Base mainnet deployment evidence validator; and
- include the exact source-verification command for the selected explorer.

The PR must pass focused Solidity tests, mutation tests, `make check`, CI,
independent review, and a no-broadcast rehearsal. Merging it still does not
authorize broadcast.

The only supported future broadcast entrypoint is
`deploy/call-escrow/deploy-base-mainnet-hardware.sh`. It accepts `ledger` or
`trezor`, never a raw private key or software keystore. It requires the exact
reviewed clean commit, a fresh time-bounded approval digest, four explicit gas
ceilings, the approved readiness and promotion records, production RPC
admission, the deterministic source rehearsal, and the read-only mainnet
preflight. Today the committed records and Solidity constants make the wrapper
refuse before simulation or broadcast.

The wrapper also requires the canonical security-review package to remain
declared as prepared and requires a separately completed review plus its exact
SHA-256 report digest. Preparing the package cannot satisfy that completion
gate.

The approval pins the deployer's expected nonce. Both providers must report
that nonce for `latest` and `pending` before and after simulation. Immediately
before the hardware prompt, the wrapper atomically writes a one-shot attempt
seal under the checkout's Git metadata. The seal is never auto-deleted. A user
rejection, timeout, or unknown result therefore requires chain investigation
and a new approval digest instead of a blind rerun.

## Stage 4: zero-fund deployment ceremony

A zero-fund deployment is a real mainnet transaction. Immediately before it:

1. Work from the reviewed merge commit with a clean tree.
2. Confirm both production observers agree on Base chain ID, the canonical USDC
   runtime, and a recent anchor.
3. Confirm the designated signing device and displayed deployer identity.
4. Simulate the exact script and record the predicted constructor values and
   gas ceiling.
5. Obtain a fresh human approval naming the network, deployer, reviewed commit,
   constructor arguments, and maximum gas spend.
6. Broadcast once. If the outcome is unknown, quarantine it and inspect
   canonical receipts; never rerun blindly.
7. Verify source, receipt, constructor arguments, runtime bytecode, immutable
   getters, and lack of funds through both production observers.

After the receipt is canonical, first run
`deploy/call-escrow/verify-base-mainnet-deployment-readonly.sh`. It compares the
transaction sender and exact creation input, receipt, live runtime hash, asset,
and release window through both admitted production providers. Source
submission remains a separate operation and requires
`FLOWOPS_SUBMIT_SOURCE_VERIFICATION=true` plus the explorer credential before
`deploy/call-escrow/submit-base-mainnet-source-verification.sh` will run.

Commit the resulting deployment evidence in a separate PR. Until that PR is
merged, every UI and service must continue to report mainnet unavailable.

## Stage 5: separately approved capped pilot

Do not approve or fund USDC as part of deployment. Pilot funding requires a
second ceremony after the deployed address is registered and the customer
signer recognizes that exact address and runtime hash.

- exact approvals only; never unlimited approval;
- one allowlisted provider and one predefined objective request;
- independently enforced per-call and total-outstanding caps;
- canonical reconciliation before any increase;
- one deliberate successful release and one failed-delivery refund; and
- immediate freeze on observer disagreement, unknown outcome, or evidence
  mismatch.

No cap increase is automatic. Each increase requires a new reviewed policy
version and explicit approval.
