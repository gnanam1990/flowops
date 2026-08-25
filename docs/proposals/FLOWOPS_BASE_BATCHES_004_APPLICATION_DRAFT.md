# FlowOps — Base Batches 004 Application Draft

Status: offline draft for founder review; **do not submit without completing the
founder, company, fundraising, video, and acknowledgement fields**

Official program: [Base Batches 004](https://www.base.org/batches)

Application deadline shown by Base on 2026-08-25: **September 9, 2026**. The
official form says drafts are not saved and requires both written answers and a
one-to-five-minute founding-team video. Prepare every answer and link offline
before opening the final form.

## Recommended positioning

- **Company name:** FlowOps
- **Primary category:** AI / Agents
- **Secondary product category:** Payments
- **Stage:** MVP
- **Network commitment:** Base-first; Base is the default settlement network
- **Application URL:** <https://flowopsagent.xyz/base>
- **Repository:** <https://github.com/gnanam1990/flowops>

## 1. Company

### Company Name

FlowOps

### What are you building?

FlowOps is the control and evidence plane for autonomous-agent payments on
Base. It evaluates every proposed spend against deterministic policy, routes
higher-risk actions for human approval, issues exact task-, recipient-, amount-,
nonce-, policy-, and expiry-bound authorization, hands execution to a
customer-managed signer, and reconciles settlement, delivery, release, or
refund only from canonical Base evidence.

### Website / Product URL

<https://flowopsagent.xyz/base>

### X URL

**FOUNDER INPUT REQUIRED:** Add the official FlowOps or founder X profile.

### Category

Select **AI / Agents**. Payments is the core product surface, but the product's
distinct customer and workflow are autonomous agents and the teams operating
them.

## 2. Team

The form supports up to four founders. Complete these fields with current,
accurate personal information:

- **Name:** FOUNDER INPUT REQUIRED
- **Role:** FOUNDER INPUT REQUIRED
- **Previous professional experience:** FOUNDER INPUT REQUIRED
- **Hardest problem or period of adversity:** FOUNDER INPUT REQUIRED
- **Email:** FOUNDER INPUT REQUIRED
- **Telegram:** FOUNDER INPUT REQUIRED
- **X:** FOUNDER INPUT REQUIRED
- **LinkedIn:** FOUNDER INPUT REQUIRED
- **Team size:** FOUNDER INPUT REQUIRED
- **Location:** FOUNDER INPUT REQUIRED
- **Founding-team video URL:** Record from the script below and add the final
  public or unlisted Loom, YouTube, or Vimeo URL.

Do not infer or invent any founder biography, contact information, location, or
team size from repository activity.

## 3. Product and traction

### What is the problem you are solving?

Autonomous agents can discover and call paid APIs, data, compute, and digital
services faster than a person can review every request. Wallet and payment
tools answer how value moves, but operating teams still lack a durable answer
to who authorized a spend, under which policy, for which task, to which exact
recipient, with what human approval, and whether the promised result was
delivered. Without that layer, agent spending is either dangerously broad or
too manual to scale.

### Why are you working on this idea?

Agent commerce needs an operational trust layer before teams can safely give
software meaningful spending authority. FlowOps is built around a simple
conviction: an agent payment should be explainable and recoverable as an exact
business action, not merely visible later as a wallet transaction.

### What is your unique insight or advantage?

Authorization and payment execution must be separate. A FlowOps approval is a
necessary, expiring capability, but it is never sufficient to move funds: the
customer-managed signer independently enforces its own caps, nonce-once rules,
freeze state, recipient binding, and chain-liveness checks. Accounting then
changes only after canonical Base evidence, so a database row cannot invent a
settlement or refund. This gives operators policy control without asking them
to surrender keys.

### How long have you been working on this idea?

Recommended selection: **Less than a year**. Founder must confirm before
submission.

### How far along are you?

Recommended selection: **MVP**. The repository contains working policy,
approval, authorization, signer-boundary, accounting, reconciliation,
PostgreSQL, Solidity, typed-data SDK, MCP, metrics, and browser surfaces. The
product is pilot-gated and does not claim unrestricted production payments.

### Demo URL

<https://flowopsagent.xyz/base>

Use the reviewer brief rather than a fabricated-data demo. It links the working
control room, repository, verified Base mainnet proposal anchor, Base Sepolia
escrow lifecycle, and executable acceptance inventory.

### What have you built to date?

FlowOps has shipped a working Base-first control plane with deterministic policy
decisions, exact-intent approval and authorization lifecycles, customer-managed
signer boundaries, durable PostgreSQL accounting, canonical chain
reconciliation, agent/service governance, x402/MCP integration, operational
metrics, and an organization-private control room. The repository tracks 88
acceptance criteria, including 67 with executable local evidence. CI covers
race-enabled Go tests, real PostgreSQL integration, Solidity unit/fuzz/invariant
tests, cross-language typed-data vectors, and desktop/mobile browser acceptance.

Public onchain evidence includes:

- Base mainnet evidence-only `FlowOpsProposalAnchor` at
  [`0x149D03Ec527Ad8667d47e7b6a2d316Dd54033250`](https://base.blockscout.com/address/0x149d03ec527ad8667d47e7b6a2d316dd54033250?tab=contract),
  with exact-match source verification; and
- Base Sepolia `CallEscrow` at
  [`0x86e145397f58e71C134C0e054320dB929483227a`](https://base-sepolia.blockscout.com/address/0x86e145397f58e71c134c0e054320db929483227a),
  with a capped 0.1 test-USDC fund-to-refund reference-signer lifecycle and zero
  terminal escrow balance.

### What is your current traction?

FlowOps has technical and onchain validation, not commercial traction. The
honest current evidence is a working public reviewer surface, a fully verified
Base mainnet proposal anchor, a completed Base Sepolia test-USDC escrow
lifecycle, and a broad executable acceptance suite. **Current active users,
paying customers, revenue, transaction volume, and TVL are not claimed.** Add
only independently verifiable design-partner or usage metrics that exist before
submission.

### Dune dashboards and/or public smart-contract addresses

- Base mainnet proposal anchor:
  `0x149D03Ec527Ad8667d47e7b6a2d316Dd54033250`
- Base Sepolia CallEscrow:
  `0x86e145397f58e71C134C0e054320dB929483227a`
- No Dune dashboard is currently claimed.

### Have you raised capital? What is your runway?

**FOUNDER INPUT REQUIRED:** State the exact amount raised, investors or
self-funded status, monthly burn, and runway. Do not submit repository-derived
guesses.

### Fundraising goals

**FOUNDER CONFIRMATION REQUIRED. Suggested draft:** FlowOps intends to use the
Base Batches investment to recruit and support allowlisted design partners,
complete the production Base settlement and signer path, strengthen operational
reliability, and validate a repeatable go-to-market motion for agent-payment
teams. State the actual target round, timing, and allocation only after founder
review.

## 4. Why Base

### Why do you want to join Base Batches? (1–2 sentences)

FlowOps needs the Base Batches team's payments and agent-market expertise to
turn a technically deep MVP into a focused design-partner pilot and repeatable
business. The program's Base-first operator network, product support, and Demo
Day are directly aligned with finding the teams that need governed agent
spending now.

### What part is or will be onchain? What uses Base?

Base is FlowOps' default settlement and evidence network. Exact payment
execution, escrow funding, provider acknowledgement, release/refund, service and
agent governance, and canonical receipt evidence are anchored to Base, while
policy evaluation, human approval, private organization data, and signing-key
custody remain offchain. The customer signer submits only narrowly authorized
transactions; FlowOps reconciles accounting from independently observed Base
receipts rather than treating internal state as payment truth.

### Token

FlowOps has no token and this application does not propose one.

### Anything else

FlowOps is deliberately honest about its stage. The mainnet contract is a
permanent no-funds proposal-provenance anchor, not a payment launch. The product
is applying with a working MVP, Base Sepolia lifecycle evidence, and a bounded
pilot plan. Independent audit, legal diligence, and production signing
ceremonies are later launch gates, not claims of work already completed.

Add a pitch-deck URL here when available.

### Referral

Optional. **FOUNDER INPUT REQUIRED** if someone referred the team.

## Required acknowledgements

The founder must personally read and accept or decline each statement in the
official form. They cover:

1. Base Batches is an investment program, not a grant, and accepting the
   offered investment is a condition of participation.
2. Submitted information is not confidential.
3. Submission creates no obligation to invest.
4. Base and Coinbase receive a non-exclusive worldwide licence to use and
   publicly display the submitted video and program materials.
5. The company and team satisfy the stated US-sanctions eligibility condition.

These acknowledgements are not pre-approved by this draft.

## Founding-team video script (about 3 minutes)

### 0:00–0:25 — Founder and problem

“I am **[name]**, **[role]** at FlowOps. Autonomous agents can now buy APIs,
data, compute, and services, but the teams operating them still have to choose
between broad wallet permissions and manually approving every payment. A wallet
transaction alone does not explain which task, policy, recipient, approval, or
delivered result justified the spend.”

### 0:25–1:05 — Product

“FlowOps is the control and evidence plane between agent intent and Base money
movement. It evaluates deterministic policy, asks for human approval only when
needed, produces an exact expiring authorization, and hands that to a
customer-managed signer. The signer independently checks caps, nonce reuse,
freeze state, recipient, and chain health before it can submit anything.”

Show the `/base` reviewer page, then the control room.

### 1:05–1:45 — What is working

“The MVP includes the policy and approval lifecycle, durable PostgreSQL
accounting, customer-signer boundaries, canonical reconciliation, x402 and MCP
integration, operational metrics, and the control room. Our CI exercises Go
race behavior, real PostgreSQL, Solidity fuzz and invariant tests, typed-data
compatibility, and browser flows. We do not fill the product with invented demo
payments or claim users and revenue we do not have.”

Show the repository and acceptance inventory.

### 1:45–2:20 — Why Base

“Base is the product's default settlement network. We have an exact-match
verified, no-funds proposal anchor on Base mainnet and a capped test-USDC escrow
lifecycle on Base Sepolia that ends in a canonical refund and zero escrow
balance. Policy and private organization data stay offchain; settlement and
economic evidence come from Base.”

Show both Blockscout links.

### 2:20–3:00 — Program goal

“Through Base Batches, we want to recruit the first allowlisted design partners,
sharpen the product around their agent-payment workflows, and turn the Base
settlement path into a reliable pilot. FlowOps can give the Base agent economy
the missing operational layer: every payment has a reason, a rule, and a
receipt.”

End on the FlowOps reviewer page and the three program goals: design partners,
product-market fit, and Base-first productionization.

## Final pre-submission checklist

- [ ] Founder and team fields completed and verified
- [ ] Official X URL added
- [ ] Capital, runway, and fundraising answers founder-approved
- [ ] Three-minute video recorded, reviewed, and uploaded
- [ ] Public reviewer page updated from the current repository version
- [ ] Every public link opened successfully
- [ ] No confidential information included
- [ ] Founder personally reviewed all five acknowledgements
- [ ] Final form filled in one session
- [ ] Founder explicitly approves the final Submit action
