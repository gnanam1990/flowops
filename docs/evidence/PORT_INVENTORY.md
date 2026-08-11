# FlowOps Phase 0 Port Inventory

Status: evidence freeze, 2026-08-11  
Scope: Snapfall, Tollbooth, and the upstream x402 Builder Code implementation  
Rule: a component is reusable only when its source, immutable commit, test, deployment evidence, limitations, and FlowOps acceptance test are recorded here.

## Executive disposition

The two portfolio repositories reproduce from clean public checkouts. Snapfall's contracts passed 119 tests, its Go daemon passed the full race-enabled suite, its sidecar passed every declared verification script, and its dashboard passed type checking, 78 tests, and a production build. Tollbooth passed type checking and all 43 contract tests, including stateful invariants. The checked-out contract runtimes also match the deployed Arc testnet bytecode after accounting for constructor immutables.

That evidence supports reuse of design invariants and selected implementation modules. It does **not** support an unchanged Base deployment. The current Snapfall x402 rail is V1 and its live Arc payment was self-facilitated, not a hosted-facilitator proof. Tollbooth's disputed `Held` state has no resolution path. Neither repository contains a license file or an external audit. The Snapfall dashboard also has four high-severity dependency advisories.

## Repositories and evidence boundary

| Repository | Immutable commit | Checkout | Owner / license evidence | Clean checkout |
|---|---|---|---|---|
| [Snapfall](https://github.com/gnanam1990/snapfall) | `103f0bea01b68023739c652dc331cb62dd327769` | `work/snapfall` | GitHub owner `gnanam1990`; no `LICENSE*` file found at repository root | Yes |
| [Tollbooth](https://github.com/gnanam1990/tollbooth) | `8ce18b9380aaaa9ce46e6b056589810c9091ac82` | `work/tollbooth` | GitHub owner `gnanam1990`; no `LICENSE*` file found at repository root | Yes |
| [x402 reference implementation](https://github.com/x402-foundation/x402) | `1d15062628b086b497ca10bb9b4c675a528c864e` | `work/x402` | x402 Foundation; Apache-2.0 in repository `LICENSE` | Yes |

No-license does not prevent the repository owner from reusing their own code, but FlowOps must choose and record a distribution license before third-party use or contribution.

## Component inventory

| ID | Existing component | Source path | Current proof | Dependencies / assumptions | Security and correctness limitations | Disposition | FlowOps acceptance test |
|---|---|---|---|---|---|---|---|
| SF-01 | Deterministic policy engine | `work/snapfall/daemon/internal/policy` | Race-enabled Go suite passed | Go; policy version supplied by lifecycle; integer micro-USDC amounts | Demo policy and merchant fixtures are product-specific; no Base recipient identity or x402 V2 quote schema | **Adapt** | Golden decisions for deny, allow, approval, stale policy, recipient substitution, task cap, org cap, and concurrent reservation |
| SF-02 | Approval lifecycle and owner decision state | `work/snapfall/daemon/internal/approval`; `daemon/internal/ownerapi`; dashboard approval pages | Race-enabled Go suite passed; AT-02/03/04/05/09/10 paths present | Durable event store; exact intent hash; clock; policy version | Current UI and schemas speak Snapfall jobs; must add rail, quote, recipient, delivery, signer, and Base chain context | **Adapt** | Approval authorizes exactly one frozen intent digest and survives restart without permitting replay or substitution |
| SF-03 | Capability-style `Grant` | `work/snapfall/daemon/internal/approval/lifecycle.go`; `daemon/internal/funding/boundary_test.go` | `Grant` has unexported fields and no exported populated constructor; funding boundary tests passed | Execution must flow through `Lifecycle.Execute`; funding methods remain exhaustively classified | Binds Snapfall `Intent`, not a FlowOps authorization envelope; one exception (`SettleOnChain`) depends on separate customer/onchain authorization | **Adapt, preserve invariant** | A forged/empty or replayed grant cannot reach any money-moving adapter; the only valid grant is minted after hash, approval, expiry, policy, freeze, and nonce gates |
| SF-04 | Durable exactly-once execution claim | `work/snapfall/daemon/internal/approval/lifecycle.go`; `daemon/internal/budget` | Race-enabled suite passed; restart and replay cases passed | Event append must become durable before executor runs | A post-claim ambiguous external broadcast remains intentionally non-retryable until reconciliation; FlowOps needs explicit ambiguous state | **Adapt** | Crash at every boundary produces either no broadcast or one quarantined/settled broadcast, never two |
| SF-05 | Kill switch / freeze registry | `work/snapfall/daemon/internal/freeze`; `daemon/internal/approval/g11_freeze_test.go`; `daemon/internal/integration/g11_test.go` | Scope, persistence, concurrency, restart, and in-flight behavior passed | All intake and execution paths must consult the same registry | Existing tests prove synchronous gating, not a measured end-to-end one-second operational SLO; reference signer is not yet covered | **Adapt** | Org/task/agent freeze blocks new envelopes and signer broadcasts, survives restart, reports in-flight work, and preserves read-only access |
| SF-06 | Budget holds and job-keyed ledger | `work/snapfall/daemon/internal/budget`; `daemon/internal/funding` | Race-enabled suite passed; reservation, commit, release, divergence, sweep, and concurrency tests passed | Event store; micro-USDC; lifecycle execution claim; per-job gates | “Cross-job spending is structurally inexpressible” is too broad as an end-to-end claim. The Grant and events bind a job, but a new multi-customer signer and Base adapters still require explicit isolation proofs | **Adapt; re-prove isolation** | No authorization, hold, nonce, receipt, or signer policy from task A can fund task B, including under concurrency and restart |
| SF-07 | JobVault settlement waterfall | `work/snapfall/contracts/src/JobVault.sol`; `contracts/test/JobVault.t.sol` | 30 JobVault tests passed; deployed runtime matches current source outside immutable slots; Arc settlement paid pool before operator | Solidity 0.8.26; OpenZeppelin; Arc USDC; FloatPool bidirectional wiring | Arc-specific configuration; deployed contracts unaudited; a documented pre-wiring funding state can strand settlement until wiring is completed | **Port** | Base Sepolia and fork tests prove constructor config, lifecycle, error decoding, exact pool-before-operator log order, conservation, and no funding before complete wiring |
| SF-08 | FloatPool | `work/snapfall/contracts/src/FloatPool.sol`; `contracts/test/FloatPool.t.sol` | 19 tests passed; deployed runtime matches outside immutable slots | JobVault, USDC, rate constants, wiring | Arc economics/configuration; unaudited; mock strategies elsewhere must not be imported as production yield | **Port** | Base tests prove authorization, rate bounds, advance accounting, repayment, loss allocation, and wiring guards |
| SF-09 | AuditAnchor | `work/snapfall/contracts/src/AuditAnchor.sol`; tests | 8 tests passed; exact deployed runtime match | Hash anchoring only | Does not prove offchain content truth or availability; no privacy mechanism by itself | **Carry or replace after data-model ADR** | A revision-bound FlowOps evidence root can be independently recomputed without placing sensitive payloads onchain |
| SF-10 | Onchain chain adapter and reconciliation | `work/snapfall/daemon/internal/chain`; `chaincfg`; integration reconciliation tests | Go suite passed; deployed Arc getters and receipts were independently read | Arc RPC, chain ID, contract ABIs, receipt/log semantics | Not a Base adapter; no multi-provider observer quorum; no chain-halt state machine; no proof against Base reorg/finality behavior | **Rewrite Base boundary, reuse decoding patterns** | Two independent Base providers agree on canonical outcomes; stale/reorged receipts cannot finalize ledger state |
| SF-11 | x402 buyer/seller sidecar | `work/snapfall/sidecar/src` | All 12 declared validation/test scripts passed; Arc self-facilitated 0.04 USDC transaction confirmed | TypeScript; EIP-3009; custom H2/H3 binding; Circle V1 client | Current rail is x402 V1. Circle hosted path never ran. Local loopback can deliver with `NOT_BROADCAST`. Arc live tx has no ERC-8021 marker. It is not evidence for x402 V2, Base, Bazaar, or a hosted facilitator | **Rewrite protocol layer; retain threat-model tests and canonical intent binding** | x402 V2 on Base Sepolia through selected facilitator, exact recipient/amount/resource binding, no delivery on unknown settlement, Builder Code suffix parsed, idempotent retry |
| SF-12 | Dashboard shell and approval UX | `work/snapfall/dashboard` | `npm ci`, typecheck, 78 tests, production build passed | Next.js 15.5.21 and indirect dependencies | `npm audit` reported 4 high advisories affecting `next`, `postcss`, `sharp`, and `nanoid`; UI/data model is Snapfall-specific | **Adapt only after dependency remediation** | Zero high/critical production dependency findings or approved exception; FlowOps task, policy, signer, rail, settlement, refund, and degraded-chain states render correctly |
| SF-13 | USYC idle-capital strategy | `work/snapfall/contracts/src/MockUSYCStrategy.sol` and docs | Tests prove mock behavior only | Permissioned USYC availability | Explicit mock; not production yield evidence | **Exclude from FlowOps MVP** | Not applicable |
| SF-14 | Discovery/scoping/compliance seams | `work/snapfall/daemon/internal/discovery`; worker/scoper seams | Local tests passed | Local catalog; deterministic TF-IDF; optional external model | Marketplace discovery is not live; compliance is a labelled stub; default scoper is deterministic | **Do not present as reusable integrations** | New integrations receive their own provider-level evidence before launch |
| TB-01 | ServiceRegistry | `work/tollbooth/src/ServiceRegistry.sol`; `test/ServiceRegistry.t.sol` | 12 tests passed; deployed Arc runtime exactly matches checkout | Solidity 0.8.26; provider-maintained endpoint/price/manifest | Permissionless metadata can be stale or malicious; source verification is not recorded as complete; unaudited | **Port only if FlowOps needs an owned registry** | Base deployment is verified; provider ownership, deactivation, repricing snapshots, and malicious metadata handling are tested |
| TB-02 | CallEscrow core | `work/tollbooth/src/CallEscrow.sol`; `test/CallEscrow.t.sol`; invariant test | 29 unit/fuzz tests plus 2 stateful invariants passed; deployed runtime matches outside immutable slots | USDC 6 decimals; registry; block timestamp; provider acknowledgement protocol | `Held` disputes have no resolver and strand funds by design; slash flag has no bonded stake; unaudited; Arc timestamps/addresses only | **Port ack/optimistic/expiry primitives; redesign dispute state** | Base tests prove no-pay-without-response refund, delivery hash binding, ack release, optimistic release, chain-time expiry, conservation, and a finite resolution for every state |
| TB-03 | Buyer/provider TypeScript middleware | `work/tollbooth/src` TypeScript package and hosted service | Typecheck passed; hosted endpoint currently returns a real Tollbooth 402 challenge | Proprietary Tollbooth flow; Arc contracts | Not x402 V2; first-party endpoint does not create external provider supply; no broad compatibility evidence | **Wrap as escrow adapter, not core x402 rail** | Evidence Fetch completes a real Base escrowed call and a failed call produces a confirmed onchain refund |
| X4-01 | x402 V2 core and Builder Code extension | `work/x402/go`; builder-code extension in Go/TS/Python trees | Full Go suite passed at pinned commit, including EVM mechanisms, HTTP adapters, MCP, integration, and builder-code packages | Apache-2.0; hosted facilitator must actually deploy compatible version | Local/reference support is not hosted-deployment evidence; hosted facilitator version is opaque | **Depend on pinned release; do not fork initially** | Settlement calldata through chosen hosted facilitator parses to expected `a`, FlowOps `s`, and facilitator `w`, or is explicitly classified unsupported/unresolved |
| NEW-01 | Customer reference signer | No existing component | None | Customer-managed key/HSM/wallet; nonce store; local policy ceiling; FlowOps envelope verifier | Adoption and security-critical; Model D fails without a shippable reference | **Build P0 artifact** | Customer can install, validate, cap, revoke, and upgrade it without giving FlowOps key custody; nonce-once and halt/freeze rules pass |
| NEW-02 | FlowOps Evidence Fetch | No existing component | None | x402 V2 resource server; optional CallEscrow adapter; content hashing | Must distinguish delivery from truth/quality; SSRF, decompression, redirects, size, MIME, and privacy risks | **Build** | A real URL produces bounded normalized output, metadata, and hash; empty/failed/unsafe fetch never releases escrow |

## Reproducibility record

Environment used:

- Foundry `1.7.1`
- Go `1.26.5 darwin/arm64`
- Node `v22.23.1`
- npm `10.9.8`

| Repository area | Exact command | Result |
|---|---|---|
| Snapfall contracts | `cd work/snapfall/contracts && forge test -vvv` | **119 passed, 0 failed, 0 skipped** across 7 suites |
| Snapfall daemon | `cd work/snapfall/daemon && go test -race ./...` | **Passed**, race detector enabled |
| Snapfall sidecar dependencies | `cd work/snapfall/sidecar && npm ci` | Passed; 0 vulnerabilities |
| Snapfall sidecar | `npm run typecheck` plus `service:test`, `store:test`, `test:post-sign`, `test:facilitator`, `test:facilitator-wiring`, `test:self-facilitator`, `test:usdc-domain`, `test:seller-hostile`, `test:h3-vectors`, `test:circle-facilitator-fixture`, and `test:release-policy` | **All passed** |
| Snapfall dashboard | `npm ci`, `npm run typecheck`, test suite, and production build | **78 tests passed; build passed**; audit reported 4 high vulnerabilities |
| Tollbooth dependencies | `cd work/tollbooth && npm ci` | Passed; 0 vulnerabilities |
| Tollbooth TypeScript | `npm run typecheck` | Passed |
| Tollbooth contracts | `forge test -vvv` | **43 passed, 0 failed, 0 skipped**; invariant runs included |
| x402 Go reference | `cd work/x402/go && go test ./extensions/buildercode ./mechanisms/evm/... ./http/... ./...` | **Passed** across core, EVM, HTTP, MCP, extensions, and integration packages |

## Independent Arc deployment evidence

Network: Arc testnet, chain ID `5042002`, RPC `https://rpc.testnet.arc.network`.

### Snapfall

| Contract | Address | Runtime/source evidence |
|---|---|---|
| JobVault | `0xF3830D7C3B8ca873bB0b277c0e179999e3d52681` | 5,841-byte runtime matches pinned build outside five immutable slots; getters confirm USDC and FloatPool |
| FloatPool | `0xde9F58A997Cf7A3258D09A797Eb5546877dc86E5` | 6,335-byte runtime matches outside five immutable slots; getter confirms JobVault |
| AuditAnchor | `0x7CDBF8a6D33d4c4C55fb94447E7E90905b3672c6` | 755-byte exact runtime match |

Settlement transaction `0x108a8f908b368aca286b8011d3dab34fc26c635d32df2689555ffc806ef9de4b` succeeded at block `53,613,272`. Its USDC transfer to the pool is log index 12 for `0.561000` USDC; the operator transfer is log index 15 for `0.439000` USDC. This independently confirms pool-before-operator ordering for that transaction.

The self-facilitated x402 transaction `0x0d39b5738f7042ae82ae0a17f24474e67c27db0cd837b791c112f8d264b6dccc` succeeded and transferred `0.040000` USDC by `transferWithAuthorization`. Its calldata does **not** end with the ERC-8021 marker. It proves Arc EIP-3009 settlement, not hosted-facilitator or Builder Code support.

No independent artifact was found for the previously described “deliberate deployed revert decoded to the expected custom error.” Local revert tests exist, but that specific live claim remains **not independently evidenced**.

### Tollbooth

| Contract | Address | Runtime/source evidence |
|---|---|---|
| ServiceRegistry | `0x5430B4ED4Ea39CBb5C7415e8F3979d3b8400da7e` | 2,775-byte exact runtime match |
| CallEscrow | `0x5e15FA9659347ba1c78a234A2D92EceeE0d20338` | 5,265-byte runtime match outside 11 immutable slots; getters confirm registry, USDC, and 30-second dispute window |

Acknowledgement release transaction `0x5ed5d94f892a2b86b3578260c3608d0cfbbb85c0a3b24168b70d2c61e2191efe` emitted `Released(..., 20000, true)` and transferred `0.020000` USDC from escrow to the provider. Refund transaction `0xb516b9d772ed94ff61111c5264920c529c884bacc1aeec3c34ff143b8beafff5` emitted `Refunded(..., 20000)` and transferred `0.020000` USDC from escrow to the buyer.

The hosted endpoint `https://tollbooth-sdk-production.up.railway.app` returned a live HTTP 402 challenge on 2026-08-11 with service ID 1, the deployed CallEscrow/USDC addresses, chain ID `5042002`, price `20000`, and expiry hint `120`.

## Frozen FlowOps invariants

These are the design properties worth carrying forward. Every one requires a Base-specific test; Arc evidence is not Base evidence.

1. No populated authority object exists before hash, approval, expiry, active-policy, nonce, freeze, and budget gates pass.
2. Every authority is bound to one organization, customer signer, task, action, rail, recipient, asset, amount ceiling, policy version, and expiry.
3. Durable execution claim precedes any external side effect; ambiguous outcomes are quarantined, not blindly retried.
4. Customer revocation, task/org freeze, and chain-halt circuit breakers are enforced in the customer signer, not only in FlowOps.
5. Money movement cannot substitute recipient, amount, asset, resource, task, or rail after approval.
6. Reservations and nonces are isolated by customer and task under concurrency and restart.
7. Database state never invents settlement, release, or refund before canonical Base evidence.
8. Escrow deadlines follow onchain time/state; a wall-clock deadline during a halt cannot fabricate a transition.
9. A no-response escrow call has a reachable exact refund; every dispute state has a finite, disclosed resolution.
10. Product usage and accounting remain valid even when Builder Code attribution is missing or delayed.

## Inventory conclusion

Phase 0 validates a **port-and-extend** strategy, but with narrower reuse than the original PRD table implied:

- Carry Snapfall's policy, approval, Grant, freeze, durable claim, and ledger semantics by adaptation.
- Port the waterfall and escrow primitives only after Base-specific tests and external review.
- Rewrite the x402 protocol boundary to V2 and treat the old sidecar as threat-model/test material.
- Do not claim end-to-end structural cross-task isolation until the new customer signer and Base adapters prove it.
- Do not launch Tollbooth's unresolved `Held` state unchanged.
- Do not reuse the dashboard dependency graph until the four high advisories are remediated or formally accepted.
