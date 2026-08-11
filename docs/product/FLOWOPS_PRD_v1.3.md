# FlowOps Product Requirements Document

**Product:** FlowOps  
**Positioning:** The Economic Operating System for AI Agents, built on Base  
**Document version:** 1.3  
**Status:** Port-and-extend build-candidate PRD  
**Date:** 2026-08-11  
**Initial network:** Base Sepolia, followed by a capped Base mainnet pilot  
**Initial settlement asset:** Native USDC on Base  
**Document owner:** FlowOps founding team  

---

## 1. Executive summary

FlowOps is a Base-native economic control plane that lets people and organizations give AI agents limited financial autonomy without giving those agents unrestricted access to money.

An organization can register an agent, attach a customer-controlled or delegated signer, define exactly what the agent may spend, connect the agent through MCP or an SDK, and monitor every request, approval, payment, receipt, output, and exception from one control surface. The agent can discover and purchase paid digital services through x402, make approved USDC transfers, and complete workflows within a deterministic policy boundary. FlowOps maintains a tamper-evident operational ledger connecting each movement of money to the task and outcome that caused it.

FlowOps is not a general-purpose agent builder, a model provider, a bank account, a crypto trading product, or a replacement for Base or Coinbase wallet infrastructure. It is the authorization, execution, accountability, and interoperability layer between an agent's intent and an irreversible financial action.

The initial product wedge is:

> Controlled purchasing of paid APIs and digital services by AI agents using USDC and x402 on Base.

This wedge is narrow enough for the team to port and extend from its existing Snapfall and Tollbooth systems, while establishing the additional Base-native primitives required for a broader economic operating system: agent identity, customer-controlled signing, budgets, policies, approvals, paid tool access, task-bound payments, delivery assurance, reconciliation, cost attribution, and emergency control.

### 1.1 Product promise

For a business owner:

> “My agents can buy the data and services they need, but they cannot exceed the limits I set, pay an unknown recipient, repeat a charge, hide a transaction, or continue after I revoke them.”

For an agent developer:

> “I can add safe payments to any agent through one MCP connection or SDK without building wallets, policy evaluation, approvals, x402 handling, transaction monitoring, and accounting myself.”

### 1.2 Launch definition

FlowOps v1 is successful when an external agent can autonomously discover an x402 resource, request permission to buy it, be approved or denied by deterministic rules, cause a customer-controlled signer to pay USDC on Base, receive the purchased output, and produce a complete task-linked audit trail—without FlowOps or the agent taking unrestricted custody of customer funds. For an opted-in service, the same flow can use a task-bound escrowed call so failed delivery expires into an automatic refund instead of an unstructured dispute.

---

## 2. Product thesis

AI agents are becoming capable of researching, planning, coding, negotiating, procuring services, and executing multi-step work. However, most agents remain economically incomplete. They can recommend a purchase but cannot safely make one. Giving an agent a raw wallet solves signing but creates a larger governance problem:

- Who owns the agent and the money?
- What is the agent allowed to buy?
- Which recipients and services are trusted?
- How much may it spend per task, transaction, day, or month?
- When is human approval required?
- How is prompt injection prevented from becoming financial loss?
- How are retries prevented from becoming duplicate charges?
- How is an onchain payment tied to a business purpose and delivered output?
- How can finance or an auditor reconstruct what happened?
- How is the agent stopped immediately?

Wallet providers, agent frameworks, x402, MCP, and Base each solve important parts of the problem. They do not by themselves provide organization-level economic governance across the entire lifecycle of an agent action.

FlowOps fills that missing layer.

### 2.1 Strategic insight

The durable moat is not wallet creation. Wallets will become commoditized. The moat is the policy and evidence graph that answers, in real time and after the fact:

> Which agent was authorized to spend which organization’s money, for which task, under which policy version, with whose approval, through which tool, to which recipient, in exchange for what output, and with what final outcome?

### 2.2 Category definition

FlowOps should be described as either:

- **Economic OS for AI agents** when communicating the long-term vision; or
- **Financial control plane for autonomous agents on Base** when explaining the launch product.

“AgentOS” is a category description, not the product name. The brand remains **FlowOps**.

---

## 3. Why Base and why now

Base provides a practical environment for low-cost USDC settlement and account-abstraction-based user experiences. The current ecosystem includes:

- Base Account authentication and wallet connectivity.
- One-tap USDC payments through Base Pay.
- Spend Permissions for bounded recurring or delegated spending.
- App-specific Sub Accounts.
- Batched calls and paymaster-assisted gas sponsorship.
- CDP non-custodial wallets for server-side and agentic applications.
- Delegated signing for time-bound backend operation of user wallets.
- x402 v2 for programmatic HTTP payments.
- The CDP x402 facilitator for verification, settlement, and KYT screening.
- Bazaar discovery for agents to search x402 services, including an MCP interface.

These capabilities make the product technically feasible today, but they must not be treated as interchangeable:

- A Base Sub Account does not automatically provide a production-grade background signer for an offline SaaS agent.
- A Spend Permission authorizes a specific spender within an onchain allowance; it does not replace FlowOps business policy, task binding, approval, or accounting.
- Base Pay is suitable for human-approved USDC payments and funding flows; it is not by itself an autonomous agent procurement protocol.
- x402 moves money for an HTTP resource; it does not decide whether the purchase is appropriate for the organization.
- A CDP wallet can sign and send; it does not know the business purpose or outcome of a payment.

FlowOps therefore composes these primitives behind its own provider-neutral wallet and payment interfaces.

### 3.1 Existing proof base: Snapfall and Tollbooth

FlowOps is not starting from an empty repository or an untested policy concept. The team already owns two relevant systems whose proven components must be treated as inputs to the build plan.

**Snapfall currently proves:**

- deterministic policy evaluation;
- the Grant pattern for bounded authority;
- an approval lifecycle with an owner inbox;
- an emergency kill switch;
- per-job isolation in which cross-job spending is structurally inexpressible;
- a complete onchain execution where the payment waterfall settled the pool before the operator in the intended log order; and
- deliberate revert handling that decoded to the expected domain error.

**Tollbooth currently proves:**

- a deployed `CallEscrow` contract;
- provider acknowledgement;
- optimistic release;
- expiry-based automatic refund; and
- 43 passing contract tests.

These are user-supplied implementation facts for this PRD revision. Before porting, the team must create an evidence inventory linking each claim to its repository path, commit, test command/output, deployed address, chain, contract ABI, and known limitations. FlowOps may reuse a component only after that inventory confirms the implementation and license/ownership status.

### 3.2 What is reused versus genuinely new

| Capability | Current proof | FlowOps disposition |
|---|---|---|
| Policy evaluation | Snapfall | Port rules and invariants; adapt asset, chain, recipient, and task schemas for Base |
| Bounded Grant pattern | Snapfall | Reuse as the core authorization abstraction; translate Arc-specific execution assumptions |
| Approval lifecycle and owner inbox | Snapfall | Reuse product flow and state model; add exact x402 quote/intent binding |
| Kill switch | Snapfall | Reuse semantics; enforce at FlowOps authorization and customer signer boundaries |
| Per-job spend isolation | Snapfall | Preserve as a non-negotiable invariant and map job to FlowOps task |
| Onchain waterfall and error handling | Snapfall | Port and re-prove on Base with Base USDC, gas, receipts, and indexing |
| Call escrow and auto-refund | Tollbooth | Port to Base and expose as an optional delivery-assured payment rail |
| x402 v2 client/gateway | None identified | New |
| Bazaar discovery and trust overlay | None identified | New |
| FlowOps MCP server and SDK | None identified | New |
| Customer-managed signer protocol | None identified | New |
| Base reconciliation and operational ledger | Partial concepts only | New/extend |
| Builder Code attribution | None identified | New Base integration |

“Scratch-built” therefore means a clean FlowOps product boundary and Base-native deployment—not discarding already proven code. The execution plan is port-and-extend.

---

## 4. Product principles

### 4.1 Deterministic control over probabilistic intent

An LLM may propose an action. It must never be the final authority that approves its own spending. Amount validation, recipient validation, policy evaluation, approval requirements, simulation, signing, idempotency, and ledger posting must be deterministic.

### 4.2 Least financial privilege

Every agent begins with no permission. An agent receives only the asset, budget, recipients, tools, actions, and time window required for its role.

### 4.3 No raw keys in the agent context

Agents interact with FlowOps capabilities, not wallet secrets. Signing occurs only inside the customer-controlled or explicitly delegated signer boundary after a valid one-time FlowOps authorization.

### 4.4 Every payment has a purpose

No payment may execute without an organization, agent, task, intent, policy decision, idempotency key, and expected recipient/value. The resulting transaction and purchased output must be linked back to those records.

### 4.5 Human control remains available

Users can require approval, reduce limits, revoke permissions, pause an agent, quarantine a recipient, or stop all organization execution. Emergency actions must not depend on the agent being online.

### 4.6 Honest availability

The UI and documentation must distinguish:

- available and enforced now;
- available through an external dependency;
- beta or partially enforced;
- planned but not implemented.

Availability claims must separate the FlowOps control plane, customer signer, facilitator, RPC/indexer, and Base chain itself. A responsive FlowOps API is not “payments operational” when Base is not producing blocks or independent chain observers cannot agree on progression. The dashboard, API, MCP responses, webhooks, and public status page must expose the affected layer, the last trusted block/time, and whether execution, settlement, refunds, and reconciliation are paused.

### 4.7 Base-only means Base-only

The MVP uses Base Sepolia and Base mainnet only. Multi-chain abstractions may exist internally, but no user-facing multi-chain execution, bridging, or token routing is in scope.

Base-only also means there is no silent failover settlement chain. If Base halts, FlowOps preserves task, approval, reservation, and evidence state but stops issuing new executable authorizations and stops claiming settlement or refund finality. Recovery occurs on Base after liveness and canonical state are re-established; FlowOps never reroutes an already-authorized payment to another network.

### 4.8 USDC-first, not token-first

The MVP supports native USDC only. ETH may be used only for infrastructure-level gas where sponsorship is unavailable. Trading, speculative assets, and arbitrary ERC-20 support are outside the MVP.

### 4.9 Safe failure over silent continuation

Ambiguous price, recipient, policy, wallet state, simulation, settlement, or reconciliation results must pause or fail the action. FlowOps must not “best effort” an irreversible transfer.

---

## 5. Goals and non-goals

### 5.1 Product goals

1. Let an organization create and manage economic identities for its AI agents.
2. Let each agent use a customer-controlled or narrowly delegated Base signer without exposing keys to FlowOps or the agent.
3. Enforce organization-defined budgets and spending policies before every financial action.
4. Support autonomous x402 purchases of digital services on Base.
5. Support controlled direct USDC payments for approved recipients and workflows.
6. Route exceptional actions to human approval and resume them safely.
7. Maintain a task-linked operational ledger and immutable audit trail.
8. Provide an MCP server and SDK that work with multiple agent frameworks.
9. Make agents immediately pausable and permissions revocable.
10. Give developers a testnet sandbox that mirrors production behaviour.

### 5.2 MVP non-goals

- Building a general LLM or agent reasoning framework.
- Hosting arbitrary untrusted agent code.
- DeFi swaps, lending, borrowing, staking, trading, or yield optimization.
- Multi-chain execution or bridging.
- Fiat accounts, cards, ACH, wire transfers, payroll, or off-ramp.
- Pooled customer funds.
- Anonymous high-value payments.
- A public token or FlowOps governance token.
- DAO governance.
- A fully open agent marketplace.
- Escrow or adjudication for physical-world services.
- Tax filing or complete GAAP/IFRS accounting.
- Replacing an organization’s ERP or general ledger.
- Claiming that blockchain settlement proves the quality of an offchain output.

### 5.3 Post-MVP opportunities

- User-delegated wallets with time-bound signing.
- Base Account Spend Permission funding and charging models.
- Agent-to-agent contracting and milestone settlement.
- Verified vendor marketplace.
- Outcome attestations and service-level guarantees.
- Agent revenue collection and profit sharing.
- Accounting integrations.
- Advanced compliance and geographic policies.
- Organization smart accounts with multi-owner governance.
- Optional public agent reputation.

---

## 6. Target customers and personas

### 6.1 Initial ideal customer profile

Model D deliberately trades custody risk for customer integration work. The first mainnet customer cannot be any 2–50-person team with a developer. It must be a technically sophisticated design partner able to operate a signer sidecar and participate in a security-sensitive integration.

**Mainnet design-partner ICP:**

- already runs one or more production/internal agents;
- has an engineer responsible for platform, security, wallet, or onchain infrastructure;
- can deploy and monitor the FlowOps reference signer container or implement the signer protocol;
- can manage its own Base wallet and limited USDC operating balance;
- accepts signer conformance testing, capped transactions, and allowlisted services;
- has recurring spend on paid data, APIs, compute, or digital services;
- needs task-level auditability and human approval; and
- will participate in incident drills and product feedback.

Likely first design partners are agent-native infrastructure startups, onchain AI teams, developer-tool companies, and technically mature research/automation groups. Team size is less important than operational capability.

**Broader post-pilot ICP:**

The original 2–50-employee audience becomes realistic only after FlowOps ships a productized delegated-signing or equivalent customer-controlled wallet experience that removes signer operations from the customer. For that audience, integration should require only MCP/SDK installation, wallet authorization, policy configuration, and funding—not operation of signing infrastructure.

The product must track activation and support burden separately for:

- reference-signer design partners;
- custom-signer enterprise partners; and
- future delegated-wallet self-serve customers.

### 6.2 Persona A: Organization owner

**Goal:** Give agents useful autonomy without losing control of company funds.

**Needs:** Simple onboarding, global limits, visibility, pause controls, clear risk language, and proof that an agent cannot exceed policy.

**Primary actions:** Create organization, invite team, fund agents, approve policies, review exceptions, pause all execution.

### 6.3 Persona B: Agent developer

**Goal:** Add purchasing and payments to an existing agent without becoming a wallet/security expert.

**Needs:** MCP and SDK integration, sandbox credentials, typed errors, test fixtures, idempotency, webhooks, logs, and framework examples.

**Primary actions:** Register agent, issue credentials, connect tools, submit intents, inspect failures, replay test events.

### 6.4 Persona C: Finance or operations manager

**Goal:** Understand and control agent spend.

**Needs:** Budgets, approval queues, recipient controls, transaction categorization, exports, reconciliation, and month-to-date visibility.

**Primary actions:** Create policies, approve/deny payments, review anomalies, export ledger, reconcile exceptions.

### 6.5 Persona D: Approver

**Goal:** Make a quick, informed decision without reading raw calldata.

**Needs:** Human-readable purpose, agent identity, task context, vendor, amount, budget impact, risk indicators, simulation result, and expiry.

**Primary actions:** Approve once, deny, request clarification, or update a recurring rule through a separate privileged flow.

### 6.6 Persona E: Auditor or security reviewer

**Goal:** Reconstruct who authorized and executed each action.

**Needs:** Immutable event history, policy versions, identity records, transaction hashes, evidence, exports, and clear gaps where external information could not be verified.

### 6.7 Machine persona: External agent

**Goal:** Obtain a capability or make an allowed payment with predictable machine-readable outcomes.

**Needs:** Stable tools, explicit schemas, quote visibility, deterministic denial reasons, resumable approval states, idempotency, and receipt retrieval.

The agent is never treated as a trusted human user, even if it was created by an organization administrator.

---

## 7. Jobs to be done

### 7.1 Owner jobs

- When I deploy an agent, I want to define its financial boundary so it can operate without risking the entire treasury.
- When an agent behaves unexpectedly, I want to stop it immediately and know whether any payment is still pending.
- When I fund an agent, I want to see how much is available, committed, pending, and spent.

### 7.2 Developer jobs

- When my agent encounters a paid endpoint, I want FlowOps to handle the quote, authorization, payment, retry, and receipt.
- When an action is denied, I want a typed reason that the agent can handle without guessing.
- When a request is retried, I want proof that it cannot charge twice.

### 7.3 Finance jobs

- When an agent spends money, I want the transaction categorized by agent, task, vendor, policy, and cost center.
- When an action exceeds a rule, I want it held for approval rather than silently failed or executed.
- When month-end arrives, I want a reconciled export with unresolved exceptions clearly separated.

### 7.4 Agent jobs

- When I need an external capability, I want to search approved services and know the price before purchasing.
- When approval is required, I want a durable pending handle so I can resume after a human decision.
- When a purchase succeeds, I want the output and a verifiable receipt tied to my task.

---

## 8. Canonical product model: the Economic Event Graph

FlowOps must maintain a canonical graph connecting economic actions to organizational purpose. The blockchain is authoritative for settlement, but it does not contain enough context to be the product’s complete source of truth.

```text
Organization
  └── Agent
       ├── Credential
       ├── Wallet
       ├── Policy Assignment ──> Immutable Policy Version
       └── Task
            └── Intent
                 ├── Quote
                 ├── Policy Decision
                 ├── Approval(s)
                 ├── Execution Attempt(s)
                 │    └── Base Transaction / x402 Settlement
                 ├── Receipt
                 ├── Output Evidence
                 └── Ledger Entries
```

### 8.1 Required invariants

1. Every execution belongs to exactly one organization and one agent.
2. Every financial execution belongs to exactly one canonical intent.
3. Every intent references the immutable policy version evaluated at decision time.
4. A policy change never retroactively alters a completed decision.
5. Every approval records actor, decision, timestamp, scope, and expiry.
6. Every external request has an idempotency key unique within its organization.
7. Every onchain transaction hash can be consumed by at most one execution record unless explicitly modeled as a batch.
8. A completed payment cannot be marked successful until server-side settlement verification passes.
9. Ledger entries are append-only; corrections are compensating entries.
10. A paused organization or agent cannot create new executable authorizations.
11. An expired quote, approval, delegation, credential, or policy assignment cannot authorize execution.
12. Agent-provided descriptions are evidence inputs, not trusted facts.

### 8.2 Truth labels

FlowOps should label important facts by source:

- **DECLARED:** supplied by a user, agent, or vendor.
- **DERIVED:** calculated deterministically by FlowOps.
- **VERIFIED_OFFCHAIN:** verified through an authenticated external service.
- **VERIFIED_ONCHAIN:** confirmed from Base chain data at the configured confirmation level.
- **INFERRED:** produced by an AI or heuristic and not independently proven.
- **UNKNOWN:** unavailable or unresolved.

The UI must not visually present `DECLARED` or `INFERRED` facts as if they were `VERIFIED_ONCHAIN`.

---

## 9. Product boundaries and trust model

### 9.1 Trusted components

- FlowOps identity and authorization service.
- Deterministic policy engine.
- Approval service.
- Customer-signer protocol, authorization-envelope issuer, and wallet-provider adapters.
- Ledger and reconciliation service.
- Verified Base RPC/indexing sources, subject to independent cross-checks.
- Configured x402 facilitator for verification and settlement.

### 9.2 Untrusted or partially trusted inputs

- Agent prompts and reasoning.
- Tool descriptions and MCP metadata.
- Vendor-provided prices, schemas, and outputs.
- Free-form task descriptions.
- Callback URLs and webhook payloads until verified.
- Browser-reported transaction success.
- RPC results before required confirmation.
- Any value derived only from the LLM.

### 9.3 Money models

FlowOps must support multiple wallet models behind one interface. The near-term operating posture is **Model D: customer-managed signer**, with **Model B: time-bound delegated signing** as the next integrated option. Model A remains useful for sandbox demonstrations and FlowOps-owned operational wallets, but it is not the default customer-funds model.

This ordering protects the product’s actual moat—the policy and evidence graph—without making FlowOps’s operational control of customer funds the prerequisite for adoption.

#### Model A: Dedicated programmatic agent wallet — sandbox or FlowOps-owned funds

A separate CDP API-key wallet is created for each agent. The organization transfers only a limited operating balance into it. FlowOps controls execution through isolated server credentials, internal policy, provider policy where available, and hard pilot caps.

**Advantages:** Reliable background signing, straightforward x402 compatibility, clean per-agent balances, simple accounting.

**Risks:** From the customer’s perspective FlowOps has operational control of the wallet. That is custody in substance regardless of infrastructure marketing language. Legal, custody, recovery, access-control, and incident-response implications make this the wrong default for customer funds. It may be used only for Base Sepolia, FlowOps-owned test funds, or a separately approved operating model.

#### Model B: User wallet with time-bound delegated signing — near-term integrated option

The user owns an embedded wallet and grants FlowOps a time-bound backend delegation.

**Advantages:** Stronger user-control story, explicit expiry and revocation.

**Risks:** Team ownership, long-running organization workflows, recovery, expiry handling, and exact x402 signer compatibility need production validation. Delegation must be narrow enough that FlowOps policy failure does not become unlimited wallet authority.

#### Model C: Base Account Spend Permission — phase candidate

The user grants a FlowOps-controlled spender permission to move a bounded amount of USDC per period.

**Advantages:** Onchain allowance and revocation; suitable for periodic funding or approved charges.

**Risks:** It is not a general signer for arbitrary agent calls, does not replace FlowOps business policy, and integration constraints must be validated for each payment flow.

#### Model D: Customer-managed signer — recommended mainnet posture

The customer runs the signer or connects an approved wallet infrastructure provider. FlowOps returns a short-lived, one-time authorization envelope bound to the canonical intent digest. The customer signer independently verifies organization, agent, task, chain, asset, recipient, amount, expiry, policy decision, and approval proof before signing and broadcasting. FlowOps later verifies the resulting transaction and reconciles it.

**Advantages:** FlowOps never controls the customer’s raw signing key or wallet balance; customer can enforce independent caps and pause; the policy/evidence moat remains intact; suitable for sophisticated design partners before a fully packaged delegated-wallet experience exists.

**Risks:** Higher integration cost, heterogeneous signer implementations, and more complex execution/reconciliation. A reference signer and conformance suite are required so that “customer-managed” does not mean “unverifiable custom integration.”

### 9.4 MVP wallet decision gate

Before Base mainnet pilot, the team must complete a focused signer spike proving Model D and evaluating Model B using these tests:

- autonomous x402 v2 payment on Base Sepolia;
- organization pause and immediate execution denial;
- per-agent and per-transaction cap;
- signing credential isolation;
- signer-side validation of the exact FlowOps authorization envelope;
- one-time authorization consumption and replay denial;
- wallet recovery or migration;
- webhook and onchain reconciliation;
- transaction simulation or deterministic transfer validation;
- full incident runbook;
- legal review of control and custody characterization.

The selected model and evidence must be recorded in an architecture decision record. Base Sepolia development may use Model A with FlowOps-owned test funds. Customer-funded Base mainnet execution defaults to Model D unless Model B is proven to offer equivalent customer control with acceptable delegation scope. Model A requires a separate legal and operating decision and is not an implicit fallback.

### 9.5 Customer signer protocol requirements

The FlowOps/customer boundary is a product surface, not an implementation detail.

The signer protocol must:

- accept a canonical authorization envelope and reject unsigned or expired envelopes;
- verify a FlowOps key through rotation-aware trust configuration;
- bind authorization to organization, agent, task, intent digest, chain, asset, recipient, amount, call data or x402 request digest, nonce, and expiry;
- consume each nonce exactly once;
- independently apply customer-configured maximums and pause state;
- return a signed execution receipt containing transaction/user-operation hash and authorization ID;
- expose health and capability negotiation without exposing keys;
- support a dry-run mode and Base Sepolia conformance suite; and
- allow the customer to revoke FlowOps trust without requesting FlowOps cooperation.

---

## 10. End-to-end product flows

### 10.1 Flow A: Organization onboarding

1. User selects “Create organization.”
2. User authenticates with email/social login or Sign in with Base.
3. FlowOps verifies the authentication response server-side and creates a secure session.
4. User enters organization name, region, and intended use.
5. User accepts product terms and risk disclosures.
6. FlowOps creates the organization and assigns the user the Owner role.
7. Owner is directed to create the first agent.

**Failure handling:** Replayed sign-in payload, expired nonce, unsupported region, duplicate organization slug, or terms failure blocks creation with a recoverable explanation.

### 10.2 Flow B: Create and fund an agent

1. Owner selects “New agent.”
2. Owner enters name, purpose, environment, cost center, and external framework.
3. Owner selects a policy template and customizes limits.
4. FlowOps displays the resulting permissions in plain language.
5. Owner confirms agent creation.
6. FlowOps creates a logical agent and asks the Owner/Developer to connect a customer-controlled signer/wallet; sandbox may use the reference Sepolia signer.
7. FlowOps verifies signer capabilities, wallet address, environment, and authorization trust before activation.
8. Owner funds the customer-controlled wallet with a small USDC amount through Base Pay or a verified direct transfer.
9. FlowOps observes and confirms the deposit onchain.
10. Available balance is shown only after reconciliation.

**Safety rule:** No agent starts with a funded wallet and an unrestricted policy.

### 10.3 Flow C: Connect an external agent

1. Developer opens the agent’s Integration tab.
2. Developer selects MCP or SDK.
3. FlowOps issues a scoped credential shown once.
4. Developer installs the connector and calls `whoami` or `get_capabilities`.
5. FlowOps returns organization, agent, environment, allowed tool categories, and non-sensitive budget availability.
6. A test intent is submitted in simulation-only mode.
7. Integration status becomes Active after the test succeeds.

**Safety rule:** Credentials authenticate an agent identity but never directly authorize spending. Every financial call still passes policy evaluation.

### 10.4 Flow D: Autonomous x402 purchase

1. The agent creates or references a FlowOps task.
2. It requests an approved capability, such as a market report or web extraction.
3. FlowOps queries a configured discovery source or accepts an explicit endpoint.
4. FlowOps retrieves and normalizes the resource’s price, network, asset, method, schema, and recipient.
5. The agent chooses a resource or requests FlowOps to select within declared constraints.
6. FlowOps creates a quote snapshot and canonical intent.
7. Policy engine evaluates amount, vendor, category, task budget, period budget, velocity, destination, and credential scope.
8. FlowOps selects the payment rail declared by the service: standard direct x402 or an opted-in delivery-assured escrowed call.
9. If allowed, FlowOps issues a one-time authorization envelope to the customer-controlled/delegated signer.
10. For direct x402, the signer creates the payment payload and the request is retried with a payment identifier.
11. For an escrowed call, the signer funds the Base `CallEscrow` flow and FlowOps follows the acknowledgement/release/expiry state machine.
12. The facilitator or escrow rail verifies and settles according to its protocol.
13. FlowOps validates the payment response and stores the output/delivery evidence.
14. The reconciliation worker confirms the onchain settlement or escrow state.
15. Ledger and task records are finalized.
16. The agent receives output plus a structured receipt.

**Safety rule:** A changed price, recipient, network, asset, method, or request digest invalidates the quote and forces re-evaluation.

### 10.5 Flow E: Human approval

1. Policy evaluation returns `APPROVAL_REQUIRED`.
2. FlowOps creates an approval request with an expiry.
3. The agent receives a durable `approval_request_id` and pending status.
4. Approver receives an in-app and optional external notification.
5. Approver reviews purpose, agent, vendor, amount, budget impact, destination, risk signals, and expected output.
6. Approver selects Approve, Deny, or Request clarification.
7. Approval records the exact intent digest; it does not authorize changed parameters.
8. If approved and still valid, execution resumes.
9. If expired or changed, the intent is re-evaluated and may require new approval.

### 10.6 Flow F: Direct USDC payment

1. Agent submits recipient, amount, purpose, task, and optional invoice/reference.
2. FlowOps resolves the recipient to a canonical address.
3. Recipient policy and risk checks run against the actual recipient—not only a token or router contract.
4. Policy engine evaluates the payment.
5. Required approval and simulation/validation occur.
6. Wallet provider broadcasts the transfer.
7. FlowOps waits for configured confirmation and verifies sender, token, recipient, amount, chain, and status.
8. Ledger is finalized and a receipt returned.

Direct payments are initially restricted to pre-approved recipients. Arbitrary addresses are out of scope for automatic approval.

### 10.7 Flow G: Pause and revoke

1. Authorized user selects Pause Agent or Emergency Pause Organization.
2. FlowOps requires step-up authentication for organization-wide pause changes.
3. New intents are denied immediately.
4. Approved but unbroadcast executions are cancelled.
5. Broadcast transactions are marked non-cancellable and monitored.
6. Active credentials are revoked or disabled.
7. Where supported, wallet/provider policies and onchain permissions are revoked.
8. An incident view lists pending, broadcast, completed, and unresolved exposure.

### 10.8 Flow H: Reconciliation exception

1. An execution remains pending beyond its expected window or provider and chain data disagree.
2. FlowOps moves it to `RECONCILIATION_REQUIRED`.
3. Reserved budget remains unavailable until resolved.
4. Reconciliation worker queries independent sources.
5. If settlement is confirmed, ledger finalizes.
6. If failed, reservation is released.
7. If unresolved, operations receives an exception with evidence and remediation steps.

FlowOps never repeats a payment merely because the first attempt’s response timed out.

### 10.9 Flow I: Delivery-assured escrowed call

This rail is available only to providers that explicitly integrate the FlowOps/Tollbooth escrow protocol. It cannot transparently protect an arbitrary Bazaar endpoint that only accepts standard x402.

1. Provider advertises an escrow-compatible service and its call terms.
2. FlowOps snapshots price, provider, endpoint, task, delivery window, release rules, and refund expiry.
3. Policy and approval evaluate the complete escrow intent.
4. Customer-controlled signer funds the Base `CallEscrow` contract for that call.
5. Provider acknowledges the call and begins fulfillment.
6. Provider returns output and delivery evidence through the agreed channel.
7. FlowOps records the output and follows the optimistic release path.
8. If the required lifecycle does not complete before expiry, the contract’s refund path returns funds according to contract rules.
9. Reconciliation records exactly one of released, refunded, unresolved, or disputed/manual-review outcomes.

**Safety rule:** FlowOps must not market escrow as proof of output quality. It provides conditional payment and deterministic timeout/refund mechanics. Provider acknowledgement, delivery evidence, release conditions, and any challenge window must be explicit.

---

## 11. Functional module contracts

Each module below defines why it exists, how users or agents enter it, its inputs, internal behaviour, outputs, dependencies, visible UI, failure states, and acceptance criteria.

### 11.1 Identity, organization, and access control

**Why it exists**

Financial actions must belong to a real organizational security boundary. Wallet addresses alone are insufficient for team membership, approval authority, credential lifecycle, or audit attribution.

**Entry points**

- Web onboarding and sign-in.
- Team invitation link.
- Organization settings.
- API authentication for machine clients.

**Inputs**

- Verified identity provider token or SIWE/Base Account proof.
- Server-issued nonce, domain, URI, chain, issued-at, and expiry for wallet sign-in.
- Organization profile and region.
- Invitation token.
- Role assignment.

**Internal behaviour**

- Verify authentication server-side.
- Prevent nonce and invitation replay.
- Create short-lived sessions with rotation.
- Enforce organization tenant isolation on every request.
- Support Owner, Admin, Developer, Finance, Approver, Auditor, and Viewer roles.
- Require step-up authentication for credential issuance, wallet changes, policy relaxation, high-value approval, and emergency unpause.
- Record role and membership changes as immutable audit events.

**Outputs**

- User, organization, membership, session, and role claims.
- Authentication and authorization audit records.

**Dependencies**

- Identity provider or Base Account authentication.
- Session store.
- Audit service.
- Notification service.

**UI**

- Organization switcher.
- Team table with role, status, last activity, and invited-by.
- Security activity panel.
- Session and credential revocation controls.

**Failure states**

- Expired or replayed nonce.
- Invalid signature or token.
- Removed user attempts access.
- Cross-tenant resource identifier supplied.
- Last Owner attempts to leave.
- Invitation accepted by a different identity than intended.

**Acceptance criteria**

- Given a previously consumed sign-in nonce, when it is resubmitted, then authentication fails and a security event is recorded.
- Given a Finance user, when they attempt to issue an agent credential, then the request is denied unless that permission is explicitly granted.
- Given an identifier from another organization, when any API is called, then the response reveals neither existence nor metadata.
- Every privileged action records actor, organization, request ID, timestamp, previous value, and new value.

### 11.2 Agent registry and lifecycle

**Why it exists**

An agent must be a governed machine principal with an owner, purpose, environment, status, credentials, policies, and wallets—not merely a name attached to an API key.

**Entry points**

- “New agent” wizard.
- Agent detail page.
- Administrative API.

**Inputs**

- Name and description.
- Business purpose and cost center.
- Owner and technical maintainer.
- Environment: sandbox, testnet, pilot, or production.
- Agent framework and callback configuration.
- Requested capabilities.

**Internal behaviour**

- Assign immutable agent ID and organization scope.
- Initialize status as `DRAFT`.
- Require a policy before activation.
- Provision wallet only after required organizational checks.
- Maintain lifecycle states: `DRAFT`, `ACTIVE`, `PAUSED`, `QUARANTINED`, `REVOKED`, `ARCHIVED`.
- Prevent archived or revoked agents from reusing credentials.
- Version material configuration changes.

**Outputs**

- Agent record.
- Lifecycle events.
- Integration configuration.
- Linked wallet and policy assignments.

**Dependencies**

- Identity/RBAC.
- Wallet service.
- Policy service.
- Credential service.
- Audit service.

**UI**

- Agent directory.
- Status and risk badge.
- Owner, purpose, balance, available budget, and recent activity.
- Tabs for Overview, Tasks, Policies, Wallet, Integrations, Activity, and Security.

**Failure states**

- Duplicate name within organization where uniqueness is required.
- Customer signer binding or sandbox-wallet provisioning partial failure.
- Agent activated without policy.
- Credential created for inactive agent.
- Deletion requested while ledger history exists.

**Acceptance criteria**

- An agent cannot become Active without an assigned policy, an execution profile, and at least one active owner.
- Revoking an agent invalidates all of its credentials and blocks new intents within the authorization propagation target.
- Historical transactions remain queryable after an agent is archived.

### 11.3 Wallet and funding service

**Why it exists**

Agents need a secure means to hold or access limited USDC and sign approved actions without receiving private keys.

**Entry points**

- Agent creation.
- Wallet tab.
- Fund agent action.
- Reconciliation worker.
- Wallet-provider webhook.

**Inputs**

- Organization and agent ID.
- Wallet provider and wallet profile.
- Funding amount and source.
- Chain ID and token contract.
- Provider event or transaction hash.

**Internal behaviour**

- Register one customer-controlled signer/wallet binding per agent for the MVP profile; allow a reference testnet signer with FlowOps-owned test funds.
- Verify signer capabilities and bind wallet address, signer instance, environment, and authorization trust configuration.
- Store provider/customer signer identifiers separately from display addresses.
- Never store raw private keys in the application database.
- Restrict network to Base Sepolia or Base mainnet according to environment.
- Restrict asset to configured native USDC contract.
- Verify deposits server-side from chain data.
- Maintain balances as observed, reserved, available, and unresolved.
- Apply provider policy caps where supported as defence in depth.
- Support FlowOps authorization-key and signer trust rotation without changing historical attribution.

**Outputs**

- Wallet address and provider reference.
- Verified funding event.
- Balance snapshot.
- Wallet health and policy status.

**Dependencies**

- Customer signer protocol and reference signer.
- CDP wallet APIs only for testnet, FlowOps-owned funds, or a separately approved delegated-wallet integration.
- Base RPC/indexer.
- Base Pay for optional human funding UX.
- Policy and ledger services.
- Secret manager/HSM-backed application credentials.

**UI**

- Wallet address and QR.
- Available, reserved, pending, and total balance.
- Fund with Base Pay or transfer USDC.
- Network and asset labels.
- Customer signer and wallet-provider health.
- Withdrawal/return-funds flow requiring privileged approval.

**Failure states**

- Customer signer registration/capability negotiation failure.
- Deposit reported by frontend but absent onchain.
- Wrong network or wrong token transfer.
- Chain reorganization.
- Customer signer unavailable or FlowOps authorization trust revoked.
- Balance disagreement.
- Funds sent after agent revocation.

**Acceptance criteria**

- A frontend claim alone never increases available balance.
- USDC on an unsupported chain is not displayed as spendable.
- Wallet credentials cannot be retrieved through any agent-facing API or MCP tool.
- Reserved funds cannot be double-counted as available for another intent.
- Provider outage blocks new signing but preserves task and approval state for safe recovery.

### 11.4 Policy and budget engine

**Why it exists**

This is the primary financial-control moat. It translates organization rules into deterministic authorization decisions before signing.

**Entry points**

- Policy builder UI.
- Policy API.
- Every financial intent.
- Scheduled budget reset.

**Inputs**

- Organization, agent, task, intent, and credential identity.
- Amount, asset, recipient, vendor, endpoint, action category, method, and request digest.
- Current period spend and reservations.
- Risk signals.
- Immutable policy version.
- Time and environment.

**Internal behaviour**

- Evaluate deny rules before allow rules.
- Support per-transaction, per-task, daily, weekly, and monthly caps.
- Support recipient allowlists and denylists.
- Support vendor/tool and capability categories.
- Support method and endpoint restrictions.
- Support approval thresholds and multi-approver rules.
- Support time windows and transaction velocity.
- Reserve budget atomically before approval or execution.
- Return one of `ALLOW`, `DENY`, `APPROVAL_REQUIRED`, or `REVIEW_REQUIRED`.
- Produce a machine-readable explanation and evaluated rule trace.
- Version policies immutably; changes affect only new decisions.
- Prevent users from configuring logically impossible or unsafe combinations.

**Outputs**

- Policy decision.
- Rule trace.
- Budget reservation or denial.
- Required approval specification.
- Decision expiry.

**Dependencies**

- Agent registry.
- Vendor/recipient registry.
- Risk service.
- Ledger/reservation store.
- Approval service.

**UI**

- Plain-language policy summary.
- Advanced rule editor.
- “Test this policy” simulator with sample intents.
- Impact preview before publishing.
- Version history and diff.
- Budget consumption bars.

**Failure states**

- Concurrent intents race for the same remaining budget.
- Policy store unavailable.
- Amount or recipient absent.
- Currency precision mismatch.
- Expired policy assignment.
- Rule conflict.
- Administrator weakens policy while an approval is pending.

**Acceptance criteria**

- Two concurrent $6 intents cannot both reserve against a $10 remaining budget.
- Any material change to amount, recipient, endpoint, method, asset, network, or request digest invalidates the prior decision.
- A policy evaluation never calls an LLM to determine the final authorization result.
- Denial responses include a stable reason code without leaking restricted security configuration.
- Policy relaxation requires an authorized human and is never initiated by an agent credential.

### 11.5 Approval service

**Why it exists**

Some actions are useful but too large, novel, or risky for automatic execution. Approval must pause and resume workflows without changing the approved action.

**Entry points**

- Policy decision.
- Approval inbox.
- Email/Slack notification link in later integrations.
- Approval API.

**Inputs**

- Intent digest and human-readable summary.
- Required role and number of approvers.
- Expiry.
- Risk and budget context.
- Approver identity and step-up authentication.

**Internal behaviour**

- Bind approval to an exact canonical intent digest.
- Support one-of and M-of-N rules.
- Separate request creator from approver where maker-checker is enabled.
- Expire approvals automatically.
- Prevent duplicate or conflicting decisions.
- Re-evaluate policy immediately before execution.
- Record clarification requests without allowing the agent to mutate the original intent invisibly.

**Outputs**

- Approval status and decision record.
- Resume signal for workflow.
- Denial reason or clarification request.

**Dependencies**

- Identity/RBAC.
- Policy engine.
- Notification service.
- Workflow engine.
- Audit service.

**UI**

- Approval inbox sorted by urgency and risk.
- Detail page showing agent, task, purpose, recipient, amount, quote, budget impact, simulation, risk signals, and expiry.
- Approve, Deny, and Request clarification actions.
- Mobile-safe approval experience.

**Failure states**

- Approval link forwarded to unauthorized person.
- Intent changes after approval.
- Approval expires during execution.
- Two approvers act simultaneously.
- Approver session lacks step-up verification.
- Approver attempts to approve their own restricted request.

**Acceptance criteria**

- An approval for $10 to Vendor A cannot authorize $11 or Vendor B.
- Pending approval survives worker restart and can resume exactly once.
- An expired approval cannot be reused.
- Every decision shows who acted, their role, exact scope, timestamp, and authentication strength.

### 11.6 Intent, quote, and execution service

**Why it exists**

Agent requests must be normalized into canonical, immutable financial intents before policy evaluation and signing.

**Entry points**

- SDK or MCP payment request.
- x402 gateway.
- Direct payment API.
- Human-created test intent.

**Inputs**

- Agent credential.
- Task ID.
- Action type.
- Recipient, asset, amount, endpoint, method, request body hash, purpose, and idempotency key.
- External quote or payment requirements.

**Internal behaviour**

- Validate schemas and normalize addresses and decimal amounts.
- Resolve chain and token from environment, not agent free text.
- Create canonical intent digest.
- Snapshot quote and expiry.
- Run policy and approval flow.
- Validate current state immediately before signing.
- Create execution attempt with exactly-once logical semantics.
- Issue the authorized intent to the configured customer-managed/delegated signer and verify the signed execution receipt.
- For directly submitted Base transactions, append the registered ERC-8021 Builder Code where compatible. For x402, register the current `builder-code` client extension so FlowOps is carried as service code `s`. Base has announced x402 Builder Codes as live; still require parsed settlement calldata before classifying an individual transaction as attributed.
- Never automatically rebroadcast a transfer after an ambiguous timeout without first determining onchain state.
- Support dry-run and Base Sepolia simulation modes.

**Outputs**

- Intent ID and status.
- Quote.
- Policy decision.
- Approval handle if required.
- Execution ID, transaction hash, or x402 receipt.
- Typed failure.

**Dependencies**

- Policy, approval, wallet, risk, x402, ledger, and reconciliation services.

**UI**

- Intent timeline.
- Normalized details and truth labels.
- Attempt history.
- Raw transaction details available to advanced users.

**Failure states**

- Invalid amount precision.
- Quote changes.
- Recipient resolution conflict.
- Policy changes before execution.
- Wallet balance changes.
- Signing timeout.
- Broadcast succeeds but API response is lost.
- Transaction reverts or remains pending.

**Acceptance criteria**

- Reusing the same organization-scoped idempotency key with identical input returns the original result.
- Reusing the key with different input returns a conflict and never executes.
- An ambiguous provider timeout moves to reconciliation rather than automatic repayment.
- A completed transaction is verified for chain, sender, asset, recipient, amount, and success before final status.
- Each execution records attribution status as `VERIFIED_SUFFIX`, `DECLARED_EXTENSION_ONLY`, `NOT_SUPPORTED`, or `UNKNOWN`. Only parsed onchain suffix evidence counts as verified attribution.

### 11.7 x402 commerce gateway

**Why it exists**

x402 enables machine-to-machine paid HTTP calls, but agents need a governed gateway that controls price, provider, signing, retries, output, and receipts.

**Entry points**

- MCP `discover_services`.
- MCP/SDK `paid_fetch`.
- Explicit x402 URL.
- Pre-approved tool configuration.

**Inputs**

- Search query or endpoint.
- HTTP method, request body, and headers subject to redaction rules.
- Maximum price.
- Allowed provider/category.
- Task ID and idempotency key.
- Expected output schema and timeout.

**Internal behaviour**

- Support x402 v2 only for new integrations.
- Parse `PAYMENT-REQUIRED` and choose only Base/USDC-compatible requirements.
- Normalize amount, recipient, scheme, network, endpoint, method, and request digest.
- Distinguish standard direct x402 from FlowOps/Tollbooth escrow-compatible services; never imply that an arbitrary Bazaar service is escrow protected.
- Query Bazaar or configured catalogs for discovery metadata.
- Cache discovery results with bounded TTL.
- Treat catalog ranking as a signal, not proof of trust.
- Run FlowOps policy before creating a payment signature.
- Use payment identifiers/idempotency extensions where supported.
- Preserve offer/receipt metadata where available.
- Retry only according to x402 protocol state and FlowOps idempotency rules.
- Verify settlement and validate the response content type, size, and schema.
- Sanitize purchased output before it enters another agent prompt or tool chain.

**Outputs**

- Search results with price and trust indicators.
- Quote snapshot.
- Paid response.
- Payment and settlement receipt.
- Payment-rail label and, when applicable, escrow/release/refund status.
- Vendor performance event.

**Dependencies**

- CDP/x402 SDK.
- x402 facilitator.
- Bazaar discovery API/MCP.
- Wallet signer.
- Policy, risk, and ledger services.

**UI**

- Approved services directory.
- Service detail with price, recipient, observed history, schema, and organization status.
- Per-service spend and success rate.
- Purchased output and receipt viewer.

**Failure states**

- Server changes requirements between attempts.
- Unsupported network, asset, or scheme.
- Facilitator unavailable.
- Payment settles but resource fails to return output.
- Output contains prompt injection or malware-like content.
- Same payment is presented for multiple calls.
- Bazaar metadata is stale or malicious.

**Acceptance criteria**

- The gateway never silently follows a requirement to a non-Base network or non-USDC asset.
- A price above the agent or caller maximum is denied before signing.
- A paid response is linked to payment, endpoint, method, request digest, and task.
- Settled-without-output is represented as a distinct dispute/exception state, not success.
- Purchased text is marked untrusted before being exposed to downstream agents.

### 11.8 MCP server and developer SDK

**Why it exists**

FlowOps must integrate with many agent frameworks without being coupled to one model vendor or orchestration library.

**Entry points**

- Remote MCP server.
- TypeScript SDK.
- Python SDK after the TypeScript contract stabilizes.
- REST API and webhooks.

**Initial MCP tools**

- `flowops_whoami`
- `flowops_get_capabilities`
- `flowops_get_budget`
- `flowops_create_task`
- `flowops_discover_services`
- `flowops_quote_service`
- `flowops_paid_fetch`
- `flowops_request_payment`
- `flowops_get_intent`
- `flowops_get_approval`
- `flowops_get_receipt`

No tool exposes seed phrases, private keys, wallet secrets, unrestricted signing, raw arbitrary calldata, or organization-wide administrative mutation.

**Inputs**

- Scoped agent credential.
- Tool-specific schema.
- Organization idempotency key.
- Correlation/task ID.

**Internal behaviour**

- Authenticate and bind every call to one agent and organization.
- Enforce tool scopes independently of financial policy.
- Validate schemas strictly and reject unknown sensitive fields.
- Rate-limit per credential, agent, organization, and IP risk signal.
- Return stable reason codes and retriable/non-retriable classification.
- Redact secrets and sensitive payloads from logs.
- Support credential rotation and overlap windows.

**Outputs**

- Structured MCP results.
- Correlation IDs.
- Intent/approval/receipt handles.
- Typed errors.

**Dependencies**

- API gateway.
- Identity and credential service.
- All domain modules exposed by tools.
- Documentation portal.

**UI**

- Integration wizard.
- Copy-once credential display.
- Last-used, scopes, environment, and revoke controls.
- Request log with sanitized input and output.
- Example prompts and code snippets.

**Failure states**

- Leaked credential.
- Credential used from unexpected environment.
- Tool schema drift.
- Client retries after timeout.
- MCP transport disconnect during approval.
- Rate-limit abuse.

**Acceptance criteria**

- Revoked credentials fail on the next authorization check within the defined propagation SLO.
- Agent credentials cannot call organization admin APIs.
- A disconnected MCP client can later query the durable intent or approval by ID.
- SDK retries preserve the original idempotency key.

### 11.9 Task and workflow evidence service

**Why it exists**

Money must be tied to work. A transaction without a task or expected outcome cannot support cost control, agent P&L, or meaningful audit.

**Entry points**

- Agent creates task through SDK/MCP.
- Human creates task in dashboard.
- External orchestrator supplies a stable task reference.

**Inputs**

- Task title, purpose, owner, agent, budget, expected output, and external reference.
- Parent task for sub-work.
- Evidence and output attachments.

**Internal behaviour**

- Create immutable task ID.
- Support states `OPEN`, `RUNNING`, `WAITING_APPROVAL`, `BLOCKED`, `COMPLETED`, `FAILED`, `CANCELLED`.
- Reserve task budget through the policy engine.
- Link intents, payments, approvals, and outputs.
- Store hashes for large external artifacts rather than placing sensitive content onchain.
- Distinguish delivered output from verified outcome.

**Outputs**

- Task record and timeline.
- Cost summary.
- Evidence manifest.
- Completion report.

**Dependencies**

- Agent registry.
- Object storage.
- Policy, execution, and ledger services.

**UI**

- Task list and detail timeline.
- Budget vs actual cost.
- Purchased resources and outputs.
- Blocker/approval state.

**Failure states**

- Agent creates infinite nested tasks.
- External task reference collision.
- Output too large or unsafe.
- Task marked complete while payment remains unresolved.

**Acceptance criteria**

- Every financial intent must reference a valid task or a privileged system operation category.
- A task cannot be financially closed while any linked execution is unresolved.
- Output storage enforces size, type, malware, and retention policies.

### 11.10 Recipient, vendor, and service registry

**Why it exists**

Agents need a governed way to distinguish approved vendors from unknown addresses and to accumulate operational evidence about service quality.

**Entry points**

- Finance settings.
- x402 discovery/import.
- Direct payment recipient creation.
- Risk review workflow.

**Inputs**

- Display name, canonical address, endpoint/domain, category, owner, and source.
- Verification evidence.
- Approval status and limits.
- Observed payment and delivery history.

**Internal behaviour**

- Separate declared identity from verified address ownership.
- Canonicalize domains and addresses.
- Detect address/domain changes.
- Maintain organization-specific allow/deny/review status.
- Store observed success, failure, dispute, latency, and settled-without-output metrics.
- Never equate Bazaar listing with organization approval.

**Outputs**

- Vendor/service profile.
- Trust signals with provenance.
- Recipient policy attributes.

**Dependencies**

- Risk service.
- x402 gateway.
- Chain data.
- Policy engine.

**UI**

- Vendor directory.
- Verified/declared/unknown badges.
- Address and endpoint history.
- Organization approval and limits.
- Observed performance.

**Failure states**

- Domain takeover or endpoint redirect.
- Address rotation.
- Similar-name spoofing.
- Conflicting evidence.
- Vendor changes payment recipient after approval.

**Acceptance criteria**

- Automatic payments require an approved vendor/service or an explicit policy allowing reviewed discovery.
- A recipient-address change invalidates prior approval until reviewed.
- Trust indicators always show their evidence source and recency.

### 11.11 Risk and transaction validation

**Why it exists**

Financial policy cannot rely only on declared recipient and amount. FlowOps needs defence-in-depth against sanctions exposure, malicious destinations, abnormal behaviour, and transaction mismatch.

**Entry points**

- Vendor onboarding.
- Intent evaluation.
- Pre-sign validation.
- Post-broadcast monitoring.

**Inputs**

- Actual sender and recipient.
- Token, amount, chain, calldata where applicable.
- Vendor/domain metadata.
- Organization history and transaction velocity.
- Provider KYT/risk response.

**Internal behaviour**

- Screen the real value recipient, not merely a USDC or router contract.
- Reject unsupported contracts and arbitrary calldata in MVP.
- Detect new-recipient, amount, frequency, and time anomalies.
- Compare prepared transaction with canonical intent before signing.
- Verify the broadcast and receipt against expected fields.
- Support risk outcomes `PASS`, `DENY`, `REVIEW`, and `UNAVAILABLE`.
- Default `UNAVAILABLE` to fail closed for autonomous mainnet execution.

**Outputs**

- Risk decision and reason codes.
- Evidence references.
- Quarantine or review action.

**Dependencies**

- Wallet/facilitator KYT.
- Chain data.
- Vendor registry.
- Policy engine.

**UI**

- Risk summary in intent and approval views.
- Security events.
- Quarantine queue.
- Evidence source and timestamp.

**Failure states**

- KYT provider unavailable.
- Conflicting provider results.
- Token contract mistaken for recipient.
- Transaction differs after wallet preparation.
- Chain RPC compromised or stale.

**Acceptance criteria**

- Mainnet autonomous execution fails closed when mandatory screening is unavailable.
- The exact USDC recipient is screened for direct and x402 payments.
- A prepared transaction that differs from the authorized intent is never signed.
- Post-settlement verification uses server-side data and does not trust a client-supplied hash alone.

### 11.12 Ledger and reconciliation

**Why it exists**

Onchain history shows transfers but not task purpose, reservation, approval, categorization, or unresolved operational states. FlowOps needs an append-only subledger that reconciles to Base.

**Entry points**

- Funding confirmation.
- Budget reservation.
- Execution broadcast.
- Settlement confirmation.
- Refund or correction.
- Scheduled reconciliation.

**Inputs**

- Economic event and source.
- Organization, agent, wallet, task, intent, execution, recipient, and transaction identifiers.
- Amount in integer USDC base units.
- Block and confirmation data.
- Chain-health observations: latest block number/hash/time from independent providers, Base status incident state, and last trusted checkpoint.

**Internal behaviour**

- Use balanced double-entry postings for tracked economic events.
- Keep pending/reserved and settled states distinct.
- Post only integer base units; format decimals at presentation.
- Make entries append-only.
- Correct through reversing/compensating entries.
- Reconcile wallet balances and known transactions against independent chain data.
- Detect unknown incoming and outgoing transfers.
- Maintain confirmation state and reorg handling.
- Maintain explicit chain states `HEALTHY`, `SUSPECTED_STALL`, `HALTED`, and `RECOVERING`; do not infer health from one RPC returning HTTP 200.
- On `HALTED`, freeze new settlement finalization and place already-broadcast executions in `PENDING_CHAIN_RECOVERY` without rebroadcasting or releasing reservations.
- Treat escrow deadlines as chain-time conditions. During a halt, show the wall-clock delay but do not invent a release or refund before the contract transition is confirmed.
- Checkpoint the last agreed block number/hash and reconciliation cursor before the stall.
- During recovery, compare independent providers, backfill every block/event from the checkpoint, detect reorg/replacement/nonce outcomes, and reconcile each previously pending execution before retries resume.
- Require a configurable period of consecutive block progression plus cross-provider agreement before leaving `RECOVERING`; high-risk recovery may require an operator release gate.
- Export organization-scoped records with stable identifiers.

**Illustrative account classes**

- Agent USDC asset.
- Reserved agent USDC.
- Pending settlement.
- Agent service expense.
- Refund receivable/received.
- Unclassified incoming funds.
- Reconciliation suspense.

This is an operational subledger, not a claim of complete statutory accounting.

**Outputs**

- Ledger entries.
- Balance views.
- Reconciliation status.
- Chain-liveness state, last trusted block, recovery cursor, and affected execution count.
- CSV/JSON export.
- Exception records.

**Dependencies**

- Wallet and execution services.
- Base RPC/indexer.
- Base public status feed as corroborating operational evidence, never as the sole chain oracle.
- Task and vendor registries.

**UI**

- Transactions and ledger views.
- Filters by agent, task, vendor, status, and date.
- Reconciliation exceptions.
- Persistent chain-halt/recovery banner with last trusted block/time, affected actions, and a link to Base status.
- Export controls.

**Failure states**

- Duplicate provider webhook.
- Chain reorg.
- Unknown outbound transfer.
- Transfer amount differs from intent.
- Export interrupted.
- Balance mismatch.
- Base stops producing blocks while RPC endpoints remain responsive.
- Providers resume at different heads or require node restart/resync.
- A transaction submitted before the halt is included, replaced, dropped, or reverted after recovery.
- Escrow wall-clock expiry passes during the halt while onchain time/state has not advanced.

**Acceptance criteria**

- Reprocessing any provider or chain event is idempotent.
- Sum of postings for each ledger transaction balances to zero.
- Completed payment status requires verified onchain/facilitator evidence.
- Unknown outbound transfers trigger a critical alert and agent quarantine according to policy.
- Corrections preserve the original entry and add a compensating trail.
- A simulated chain halt cannot create a completed settlement, received refund, released reservation, or duplicate broadcast from stale data.
- Recovery backfills from the last trusted checkpoint and resolves every pre-halt broadcast to exactly one canonical outcome before autonomous retry is re-enabled.
- Customer-visible exports preserve the halt/recovery interval and never rewrite historical timestamps to imply continuous chain availability.

### 11.13 Dashboard, alerts, and emergency control

**Why it exists**

Economic autonomy is acceptable only when users can understand current exposure and intervene quickly.

**Entry points**

- Web dashboard.
- Notifications.
- Incident link.

**Inputs**

- Organization/agent state.
- Budgets, balances, tasks, approvals, executions, risk events, and provider health.

**Internal behaviour**

- Aggregate current state without hiding unresolved events.
- Prioritize approvals, anomalies, and low balance.
- Separate observed balance from available budget.
- Provide organization and agent pause.
- Require step-up authentication for unpause and high-risk settings.
- Show external dependency degradation.

**Outputs**

- Operational overview.
- Alerts.
- Pause/revoke commands.
- Incident exposure report.

**Dependencies**

- All product services.
- Notification providers.
- Observability platform.

**UI**

- Overview: balance, available budget, today/month spend, pending approvals, unresolved execution, active agents.
- Agents: status, wallet, policy, spend, recent task.
- Approvals: decision queue.
- Activity: unified economic timeline.
- Security: credentials, sessions, risk events, pause controls.
- Developers: MCP, SDK, webhooks, logs.

**Failure states**

- Dashboard aggregates stale data.
- Pause command partially propagates.
- Notification delivery fails.
- User confuses onchain balance with spendable amount.

**Acceptance criteria**

- Every balance and aggregate shows freshness timestamp.
- Emergency pause produces a durable event and blocks new authorization even if workers are delayed.
- UI clearly distinguishes Available, Reserved, Pending, Settled, and Unresolved.
- Critical alerts remain visible until acknowledged or resolved.

### 11.14 Webhooks and external integrations

**Why it exists**

External agent runtimes need asynchronous state changes such as approval, settlement, denial, and reconciliation completion.

**Entry points**

- Developer settings.
- Event publication.
- Webhook retry worker.

**Inputs**

- HTTPS endpoint.
- Selected event types.
- Signing secret.
- Event payload.

**Internal behaviour**

- Sign payloads with timestamped HMAC or asymmetric signatures.
- Assign globally unique event ID.
- Retry with exponential backoff and bounded retention.
- Preserve ordering per aggregate where promised; otherwise document no global ordering.
- Provide replay from dashboard.
- Redact sensitive fields.
- Disable endpoints after sustained failures only after warning.

**Outputs**

- Delivered webhook.
- Attempt log.
- Replay action.

**Dependencies**

- Event bus/outbox.
- Secret manager.
- Notification/worker infrastructure.

**UI**

- Endpoint status.
- Event subscriptions.
- Signing secret rotation.
- Delivery attempts and replay.

**Failure states**

- Endpoint timeout.
- Duplicate delivery.
- Out-of-order delivery.
- DNS rebinding/SSRF risk.
- Secret rotation mismatch.

**Acceptance criteria**

- Events use an outbox so database commit and publication cannot silently diverge.
- Receivers can deduplicate using event ID.
- FlowOps prevents webhooks to private/link-local infrastructure unless explicitly supported through a secure enterprise mechanism.

### 11.15 Task-bound escrow and delivery assurance

**Why it exists**

Standard x402 confirms payment settlement but does not guarantee that a usable response was delivered. Tollbooth’s existing `CallEscrow` creates a differentiated optional rail in which an opted-in paid call can acknowledge work, release optimistically after delivery, or return funds after expiry.

**Entry points**

- Service registration as escrow-compatible.
- MCP/SDK purchase request selecting `payment_rail: escrowed_call`.
- Escrow status and refund action where the contract permits it.
- Reconciliation worker.

**Inputs**

- Provider and canonical recipient.
- Task, intent, endpoint, method, request digest, and expected output metadata.
- Amount, native USDC address, Base chain ID, acknowledgement window, release/challenge rules, and expiry.
- Ported `CallEscrow` contract address and version.
- Customer signer authorization.

**Internal behaviour**

- Offer escrow only for providers that implement the required acknowledgement and delivery protocol.
- Create one escrow position per canonical call/intent.
- Bind the position to task and request digest so it cannot be reused for another job.
- Preserve Snapfall’s per-job isolation when mapping job authority to FlowOps task authority.
- Track acknowledgement, delivery evidence, release, expiry, and refund as separate states.
- Follow contract state; never invent a refund in the database before the onchain refund is confirmed.
- Route standard Bazaar-only services through direct x402 and label them as non-escrowed.
- Version the contract and adapter so FlowOps can reconcile historical calls after upgrades.

**Outputs**

- Escrowed-call ID and contract position.
- Acknowledgement and delivery events.
- Release or refund transaction.
- Delivery-assurance receipt.
- Ledger entries and provider reliability evidence.

**Dependencies**

- Tollbooth `CallEscrow` ported and tested on Base.
- Customer-managed/delegated signer.
- Policy, task, x402/service, ledger, and reconciliation modules.
- Base RPC/indexer.

**UI**

- Payment rail badge: Direct x402 or Escrowed call.
- Escrow timeline showing funded, acknowledged, delivered, releasable, released, expired, and refunded states.
- Exact deadline and refund eligibility.
- Clear warning that escrow governs payment conditions, not subjective output quality.

**Failure states**

- Provider never acknowledges.
- Provider acknowledges but does not deliver.
- Output arrives after expiry.
- Release and refund attempts race.
- Base transaction is pending near deadline.
- Ported contract behaviour differs from Arc deployment.
- Service claims escrow compatibility but does not implement callbacks/protocol.

**Acceptance criteria**

- The Base port passes the existing Tollbooth suite plus Base-specific asset, timing, reentrancy, authorization, and event-order tests.
- An unacknowledged/unfinished call reaches the contract-defined refund outcome after expiry without an operator moving funds manually.
- A released call cannot also be refunded, and a refunded call cannot also be released.
- Cross-task escrow spending remains structurally inexpressible.
- FlowOps never labels a direct x402 purchase as delivery-assured.
- Mainnet use is blocked until the Base deployment is independently reviewed and its address/version are registered in FlowOps configuration.

### 11.16 Reference signer sidecar

**Why it exists**

Model D is not adoptable if each design partner must independently invent signing infrastructure. FlowOps must ship a production-oriented reference signer sidecar that turns the customer-managed signer protocol into a deployable artifact while keeping keys and final controls inside the customer boundary.

**Entry points**

- Customer deployment through a signed container image and documented configuration.
- FlowOps signer-registration wizard.
- Health/capability endpoint.
- Authorization-envelope execution endpoint.
- Customer-local pause, trust-revocation, and policy configuration.

**Inputs**

- Customer wallet/provider configuration or customer-managed key reference.
- Trusted FlowOps authorization public keys and key IDs.
- Base environment and allowed chain IDs.
- Customer-local caps, recipients, assets, contracts, and expiry maximums.
- Signed one-time FlowOps authorization envelope.
- Durable nonce/replay store.

**Internal behaviour**

- Verify the FlowOps signature and key status.
- Canonicalize and independently validate organization, agent, task, intent digest, chain, asset, recipient, amount, calldata/request digest, expiry, nonce, policy decision, and approval proof.
- Enforce customer-local deny rules and caps even when FlowOps authorizes the action.
- Atomically consume nonces before signing and make retry status queryable.
- Support Base Sepolia and Base mainnet as separately configured environments.
- Enforce a customer-configurable chain-liveness circuit breaker using block-age/progression evidence; do not broadcast a new transaction when the signer classifies Base as halted.
- Sign/broadcast only the verified action; never accept arbitrary calldata through a generic pass-through endpoint.
- Return a signed execution receipt.
- Expose structured audit logs without secrets.
- Allow the customer to pause locally or remove FlowOps trust without calling FlowOps.
- Support versioned upgrades, rollback, data backup, and wallet/provider rotation.

**Outputs**

- Capability/health document.
- Accepted/denied authorization result with stable reason code.
- Signed execution receipt and transaction/user-operation hash.
- Customer-local audit event.
- Attested signer version/configuration fingerprint.

**Dependencies**

- Customer-controlled wallet or signing provider.
- Durable local database for nonces and receipts.
- Base RPC/provider.
- FlowOps authorization-key registry.
- Container signing and software supply-chain controls.

**UI**

- Deployment wizard with Docker/Kubernetes examples.
- Connection and conformance-test status.
- Signer version, environment, wallet address, last heartbeat, and last authorization.
- Local-policy mismatch and trust-revocation status.
- Upgrade/rollback guidance.

**Failure states**

- FlowOps key unknown, expired, rotated, or revoked.
- Duplicate nonce or conflicting retry.
- Envelope mutation or non-canonical encoding.
- Customer-local rule denies a FlowOps-authorized action.
- RPC timeout after broadcast.
- Base chain halt or stale block head while RPC transport remains reachable.
- Sidecar loses local nonce database.
- Container/image provenance cannot be verified.
- Sidecar version is incompatible with the authorization schema.

**Acceptance criteria**

- A design partner can deploy the sidecar from published documentation without FlowOps receiving the signing key.
- The conformance suite proves valid execution and rejects expired, replayed, cross-task, wrong-chain, wrong-token, wrong-recipient, changed-amount, changed-calldata, and invalid-approval envelopes.
- Two concurrent requests with the same nonce result in at most one signing attempt.
- Local pause and removal of the FlowOps authorization key block new execution even when the FlowOps control plane continues sending validly signed envelopes.
- Restart and retry after an ambiguous broadcast return the original attempt or reconciliation state rather than sign again.
- A simulated Base halt blocks new broadcasts locally even if FlowOps continues sending otherwise valid authorization envelopes; recovery still requires nonce and onchain-outcome reconciliation before retry.
- Customer-local maximums can only narrow, never expand, FlowOps authority.
- Release artifacts are reproducible or signed, vulnerability-scanned, SBOM-producing, versioned, and rollback-tested.
- Base mainnet use requires the customer to pass the published signer conformance suite against the exact deployed version.

### 11.17 First-party escrow-compatible reference service

**Why it exists**

An escrow rail with no compatible providers is not a product. FlowOps must bootstrap supply by shipping one genuinely useful first-party paid service that supports both standard x402 and the acknowledgement/release/expiry escrow protocol.

The initial service is **FlowOps Evidence Fetch**: given a public URL, it returns a timestamped normalized text extract, HTTP metadata, source URL, and content hash. This is useful to research agents and produces objective delivery evidence without claiming truth or analysis quality.

**Entry points**

- Public direct-x402 endpoint.
- Escrow-compatible FlowOps service endpoint.
- Bazaar listing for the direct x402 route.
- FlowOps approved-service catalog.
- Demonstration and conformance workflow.

**Inputs**

- Public HTTP/HTTPS URL.
- Extraction mode and bounded output limit.
- Optional selector or JSON output schema from an allowlisted subset.
- Task/intent/request digest.
- Direct x402 payment proof or escrow-call identifier.

**Internal behaviour**

- Resolve and validate the URL against SSRF, private-network, redirect, content-size, and content-type policies.
- For direct x402, declare the current builder-code extension and settle through the selected facilitator.
- For escrowed calls, acknowledge only after input validation and available execution capacity.
- Fetch, normalize, hash, and return the output with objective delivery metadata.
- Submit/record delivery evidence required by the escrow protocol.
- Never mark a failed/empty fetch as delivered merely to release funds.
- Provide a testnet-only forced-failure mode for expiry/refund acceptance tests; it is unavailable in production.
- Publish price, timeout, retention, and supported-input limits.

**Outputs**

- Normalized text/metadata response.
- Source and content hash.
- Direct x402 receipt or escrow delivery evidence.
- Provider-side trace linked to the FlowOps task.

**Dependencies**

- x402 resource-server libraries and selected facilitator.
- Ported Base `CallEscrow` and provider adapter.
- Safe fetch/sandbox infrastructure.
- Object storage and retention controls.
- FlowOps task, receipt, and reconciliation modules.

**UI**

- Service detail and live documentation.
- Direct versus escrowed price/terms.
- Example MCP/SDK calls.
- Delivery, latency, and refund statistics.
- Testnet demo showing success/release and forced-expiry/refund.

**Failure states**

- SSRF/private-network target.
- Redirect changes host or reaches a blocked network.
- Page is too large, unsupported, authentication-gated, or legally restricted.
- Provider acknowledges but worker fails.
- Output storage succeeds but delivery response is lost.
- Builder-code extension is ignored by the hosted facilitator.
- Forced-failure control is accidentally enabled outside testnet.

**Acceptance criteria**

- The public direct route completes a real x402 Base Sepolia settlement and returns a non-mock response for a permitted public URL.
- The escrow route completes acknowledgement, delivery evidence, optimistic release, and reconciliation for the same useful capability.
- A forced testnet failure reaches contract-defined expiry/refund and reconciles without operator fund movement.
- Direct and escrowed outputs include the same canonical request digest and comparable delivery metadata.
- SSRF tests prove loopback, private, link-local, metadata-service, DNS-rebinding, and unsafe redirect targets are blocked.
- The direct x402 route is submitted/indexed through Bazaar when its indexing requirements are met.
- Production deployment has forced-failure controls removed or cryptographically/environmentally disabled.
- The capped pilot may begin with this first-party supply, but general availability is blocked until at least one independent provider passes the published escrow-provider conformance flow; FlowOps must not treat its own service as proof of ecosystem adoption.

---

## 12. State models

### 12.1 Intent state machine

```text
DRAFT
  -> QUOTED
  -> POLICY_EVALUATING
     -> DENIED
     -> REVIEW_REQUIRED
     -> APPROVAL_PENDING
     -> AUTHORIZED
  -> EXECUTING
     -> SUBMITTED
     -> RECONCILIATION_REQUIRED
     -> FAILED
  -> SETTLED
  -> OUTPUT_RECEIVED
  -> COMPLETED
```

Terminal states are `DENIED`, `FAILED`, `COMPLETED`, `CANCELLED`, and `EXPIRED`, except that a later refund or correction may create related compensating events without rewriting the original terminal state.

### 12.2 Approval state machine

```text
PENDING -> APPROVED
        -> DENIED
        -> CLARIFICATION_REQUESTED -> PENDING
        -> EXPIRED
        -> CANCELLED
```

### 12.3 Execution state machine

```text
CREATED -> SIGNING -> SIGNED -> SUBMITTING -> SUBMITTED
                                      |          |
                                      v          v
                                   FAILED   CONFIRMING
                                                 |
                                      +----------+-----------+
                                      v                      v
                                   SETTLED      RECONCILIATION_REQUIRED
```

### 12.4 Agent state machine

```text
DRAFT -> ACTIVE <-> PAUSED
             |          |
             v          v
         QUARANTINED -> REVOKED -> ARCHIVED
```

`QUARANTINED` is system-initiated for suspected compromise or unexplained movement. Only an authorized human can resolve it.

---

## 13. Prioritised requirements

### 13.1 P0 launch blockers

| ID | Requirement |
|---|---|
| P0-01 | Base-only, native-USDC-only execution enforcement |
| P0-02 | Organization tenant isolation and RBAC |
| P0-03 | Agent registry, lifecycle, and scoped credentials |
| P0-04 | Shippable customer-managed reference signer sidecar, deployment docs, and conformance tests |
| P0-05 | Deterministic policy engine with atomic budget reservations |
| P0-06 | Exact-intent human approvals |
| P0-07 | x402 v2 paid fetch on Base Sepolia |
| P0-08 | Idempotency and ambiguous-timeout reconciliation |
| P0-09 | Actual recipient, amount, token, chain, and settlement verification |
| P0-10 | Append-only audit trail and balanced operational ledger |
| P0-11 | Agent and organization emergency pause |
| P0-12 | MCP server with no raw signing capability |
| P0-13 | Mainnet security review, wallet-model decision, and pilot caps |
| P0-14 | Backup, recovery, monitoring, and incident runbooks |
| P0-15 | Snapfall asset inventory and Arc-to-Base invariant port evidence |
| P0-16 | Tollbooth CallEscrow port and automatic-expiry refund proven on Base Sepolia |
| P0-17 | Builder Code issuance-surface mapping plus Base Sepolia facilitator-attribution experiment and calldata evidence |
| P0-18 | One live first-party service supporting direct x402 and escrowed-call success/refund paths |
| P0-19 | Base chain-halt detection, signer circuit breaker, truthful degradation, and deterministic recovery drill |

### 13.2 P1 shortly after pilot

| ID | Requirement |
|---|---|
| P1-01 | Base Pay funding UX |
| P1-02 | Direct USDC payments to approved recipients |
| P1-03 | Vendor/service registry and organization allowlists |
| P1-04 | Webhooks and TypeScript SDK |
| P1-05 | Multi-approver maker-checker policies |
| P1-06 | Policy simulator and impact preview |
| P1-07 | CSV/JSON ledger export |
| P1-08 | Reconciliation exception console |
| P1-09 | Provider-level policy defence in depth |
| P1-10 | Reviewed Base mainnet CallEscrow deployment and opted-in delivery-assured providers |
| P1-11 | User wallet with time-bound delegated signing |

### 13.3 P2 expansion

| ID | Requirement |
|---|---|
| P2-01 | Spend Permission funding/charging integration |
| P2-02 | Agent-to-agent paid workflows |
| P2-03 | Verified public service marketplace |
| P2-04 | Agent revenue, margin, and P&L views |
| P2-05 | Accounting and ERP integrations |
| P2-06 | Additional customer-managed signer providers |
| P2-07 | Outcome attestation and milestone settlement |

---

## 14. MVP definition

### 14.1 MVP user promise

A developer can connect an existing agent to FlowOps, and an organization owner can fund and govern it. The agent can buy an approved x402 resource on Base within a strict USDC budget. Every exceptional purchase pauses for approval. Every completed purchase appears with its task, vendor, amount, policy, approval, transaction, output, and receipt.

### 14.2 Included

- Single organization per initial account, with future-safe organization model.
- Owner, Developer, Finance/Approver, and Viewer roles.
- Agent creation, lifecycle, and scoped credentials.
- Customer-managed signer protocol and reference Base Sepolia signer; any FlowOps-provisioned test wallet contains FlowOps-owned test funds only.
- Native USDC balance and funding detection.
- Policy templates plus advanced limits.
- Per-transaction, per-task, daily, and monthly budgets.
- Approved service/recipient list.
- Approval threshold and one approver.
- MCP tools and REST API.
- x402 v2 exact-payment flow through configured facilitator.
- Optional Tollbooth-derived task-bound escrowed call for integrated providers, including expiry auto-refund on Base Sepolia.
- FlowOps Evidence Fetch as the first useful direct-x402 and escrow-compatible provider.
- Explicit URL and Bazaar service discovery.
- Task creation and task-bound purchases.
- Append-only activity timeline and operational ledger.
- Reconciliation worker.
- Agent pause, credential revoke, and organization emergency pause.
- Basic dashboard, approval inbox, activity, and developer logs.

### 14.3 Excluded

- Arbitrary smart-contract calls.
- Non-USDC assets.
- Non-Base networks.
- Token swaps or DeFi.
- Unrestricted direct payments.
- Agent-created policy changes.
- Agent withdrawal of remaining balance to arbitrary addresses.
- Public marketplace publishing.
- Escrow for arbitrary Bazaar providers that have not integrated the FlowOps escrow protocol.
- Production SLA for external x402 service output quality.

### 14.4 Pilot caps

Until independent security review and operational evidence justify expansion:

- allowlisted design partners only;
- maximum organization-funded balance set conservatively;
- maximum per-agent balance;
- maximum transaction and daily amount;
- native USDC only;
- approved x402 services only;
- mandatory approval above low threshold;
- global kill switch staffed by on-call operators;
- direct x402 purchases clearly disclose that recoverability depends on vendor cooperation; and
- escrowed calls follow the reviewed contract’s release/expiry/refund rules without implying subjective quality arbitration.

Exact dollar limits are an operational launch decision and must not be hardcoded into this PRD.

---

## 15. Base and Coinbase dependency matrix

| Primitive | Current use in FlowOps | MVP status | Important constraint |
|---|---|---|---|
| Base mainnet / Sepolia | Settlement network | Required | Network must be derived from environment and verified |
| Native USDC | Settlement asset | Required | Exact contract address and decimals are configuration, never agent input |
| Base Account | Optional sign-in and human funding UX | P1 | Authentication still requires backend verification and anti-replay |
| Base Pay | Human-approved funding/payment UX | P1 | Server must verify status; frontend success is insufficient |
| Base Sub Accounts | Future user/app wallet option | P2 | Browser/session ownership does not automatically solve background signing |
| Spend Permissions | Future bounded funding/charging path | P2 | Does not replace task policy or authorize arbitrary agent actions |
| Customer-managed signer | Default customer-funds execution boundary | P0 | Requires reference implementation, signed authorization envelopes, conformance suite, and reconciliation |
| CDP non-custodial/API-key wallets | Testnet/FlowOps-owned wallet or separately approved model | Optional | Operational control creates custody-in-substance concern for customer funds |
| CDP delegated signing | User-owned wallet option | P1 | Time-bound delegation, team ownership, policy scope, and x402 compatibility need validation |
| CDP Paymaster | Gas sponsorship where applicable | P1 | Endpoint secret and contract allowlists require protection |
| x402 v2 | Paid HTTP services | P0 | Protocol/dependency may evolve; pin and test versions |
| CDP x402 facilitator | Verify/settle and KYT | P0 default | External availability and policy dependency; adapter must be replaceable |
| x402 Bazaar | Service discovery | P0 limited | Active development; ranking is not proof of trust |
| Bazaar MCP | Optional discovery transport | P1 | FlowOps still applies its own service and spend policies |
| Tollbooth CallEscrow port | Optional delivery-assured call rail | P0 Sepolia / P1 mainnet | Only protects opted-in providers; requires Base port evidence and independent review |
| Base Builder Codes / ERC-8021 | Transaction attribution and Base.dev analytics | P0 verification | Base announced Builder Codes live for x402 on 22 June 2026, and public x402 docs describe the production extension. Verify the selected facilitator/version preserves FlowOps `s`; separately resolve dashboard-minted ERC-721 codes versus wallet-derived agent API codes |
| Base Docs MCP | Development documentation | Development only | Not a production agent payment/control interface |

### 15.1 Dependency rule

All external wallet, facilitator, discovery, RPC, risk, and notification providers must be accessed through internal adapters. Product records use FlowOps canonical IDs and states, not provider states as the sole source of truth.

---

## 16. Security requirements and threat model

### 16.1 Security architecture rules

1. Application database and agent runtime never contain raw wallet private keys.
2. The default mainnet signer is customer-controlled; FlowOps issues authorization envelopes and never receives the raw signing key.
3. Policy authorization is signed, short-lived, one-time, and bound to the canonical intent digest.
4. Customer signer independently verifies chain, asset, amount, recipient, call/request digest, expiry, nonce, policy decision, and approval proof.
5. No financial mutation uses GET requests.
6. Every externally retried mutation requires an idempotency key.
7. All secrets are stored in a managed secret system and rotated.
8. Production, pilot, and testnet credentials are isolated.
9. Logs redact tokens, wallet secrets, authorization payloads, sensitive request bodies, and personal data.
10. Administrative actions require strong authentication and least-privilege RBAC.

### 16.2 Primary threats

#### Prompt injection causes unauthorized purchase

**Scenario:** Purchased content or a website instructs the agent to spend money or reveal credentials.

**Controls:** Agents receive no wallet keys; every payment is a separate structured tool call; purchased output is marked untrusted; policy restricts vendors, categories, prices, and task budget; high-risk actions require approval.

#### Malicious x402 server changes price or recipient

**Scenario:** Server advertises one offer and requests a different payment during retry.

**Controls:** Quote snapshot, maximum price, canonical intent digest, change detection, re-evaluation, recipient policy, payment identifier, and receipt retention.

#### Duplicate payment caused by timeout/retry

**Scenario:** Payment settles but the client loses the response and retries.

**Controls:** Organization-scoped idempotency, payment identifier, attempt state machine, unique transaction consumption, onchain/facilitator lookup before retry, reconciliation state.

#### Agent credential compromise

**Scenario:** Attacker steals MCP token and submits payments.

**Controls:** Scoped credentials, rate/velocity limits, allowlists, budget caps, anomaly review, fast revocation, optional source restrictions, no admin scopes, separate policy authorization.

#### Customer signer or FlowOps authorization key compromise

**Scenario:** Attacker compromises the customer signer, or compromises the FlowOps key used to authorize intents.

**Controls:** Independent signer-side caps and allowlists, one-time authorization envelopes, separate FlowOps authorization key, infrastructure identity, egress restrictions, monitoring, rotation, customer revocation, and global pause. A FlowOps authorization alone must not override customer signer limits.

#### Insider policy bypass

**Scenario:** Administrator weakens limits and pays an attacker.

**Controls:** Maker-checker for policy relaxation, step-up auth, immutable diffs, delayed activation for high-risk changes, alerts, and owner override/pause.

#### Recipient-screening error

**Scenario:** System screens the USDC contract but not the actual payee.

**Controls:** Decode/derive actual value recipient from canonical action; screen sender and recipient; verify post-settlement transfer fields.

#### Cross-tenant access

**Scenario:** Identifier manipulation exposes another organization’s agent or wallet.

**Controls:** Tenant predicate in data-access layer, non-enumerable IDs, authorization at service boundary, negative tests, and audit.

#### Chain/RPC disagreement

**Scenario:** Stale or compromised RPC reports false success or balance.

**Controls:** Confirmation thresholds, provider plus independent RPC/indexer cross-check for high-risk actions, reorg handling, unresolved state.

### 16.3 Required security testing

- External smart contract/wallet integration review before mainnet.
- Threat-model review for every new financial action type.
- SAST, dependency scanning, secret scanning, and infrastructure policy checks in CI.
- Fuzz/property tests for policy and ledger invariants.
- Concurrency tests for reservations and idempotency.
- Replay tests for sign-in, approval, webhook, and payment identifiers.
- SSRF testing for external endpoints and webhooks.
- Prompt-injection red-team scenarios.
- Production-like chaos tests for provider timeout after broadcast.
- Incident drills for wallet credential compromise and unknown outbound transfer.

### 16.4 Security launch blockers

Mainnet is blocked if any of these remain unresolved:

- Signer can execute without a valid FlowOps authorization.
- Policy reservation is non-atomic.
- Payment retry can double-charge.
- Actual recipient is not screened and verified.
- Organization pause is not enforced at authorization and signing layers.
- Unknown outbound transfers are not detected.
- Ledger cannot reconcile to observed wallet activity.
- Production credentials appear in agent-visible context or logs.
- No recovery path exists for wallet/provider credential loss.

---

## 17. Privacy, compliance, and legal product boundaries

FlowOps is a software control and orchestration product, but financial regulation depends on actual wallet control, customer relationship, geography, and business model. Marketing language cannot decide legal status.

### 17.1 Required principles

- Do not pool customer balances.
- Use distinct onchain wallets and clear ownership/control disclosures.
- Keep pilot balances and limits low.
- Screen transactions using the selected provider and FlowOps rules.
- Restrict unsupported jurisdictions based on legal advice.
- Record terms acceptance and material policy changes.
- Provide data retention and deletion controls where compatible with immutable financial records.
- Never place personal or confidential task data onchain.
- Separate product telemetry from customer task content.
- Treat tax/accounting exports as operational data, not certified filings.

### 17.2 Mandatory legal decision before mainnet

Customer-managed signing materially reduces the custody problem but does not eliminate legal review. Counsel must evaluate:

- whether the authorization service, delegated-signing option, escrow contract/interface, fees, or any fallback wallet model makes FlowOps a custodian, money transmitter, escrow provider, payment intermediary, or other regulated actor in target regions;
- responsibility for sanctions/KYT and vendor disputes;
- treatment of customer funds and insolvency risk;
- required disclosures for irreversible payments;
- privacy obligations for agent prompts, outputs, and team identities;
- acceptable-use restrictions for autonomous purchasing.

Mainnet pilot must not launch solely because the technology works.

---

## 18. User experience requirements

### 18.1 Information architecture

Primary navigation:

1. Overview
2. Agents
3. Tasks
4. Approvals
5. Transactions
6. Vendors
7. Developers
8. Settings

### 18.2 Onboarding principles

- Explain “agent wallet,” “available budget,” and “policy” without assuming crypto expertise.
- Default to Base Sepolia until the organization explicitly requests pilot access.
- Show a plain-language permission preview before activation.
- Use progressive disclosure for raw addresses, hashes, and protocol fields.
- Never represent a wallet balance as money protected by a bank or deposit insurance.

### 18.3 Approval design

Approvers must see, above the fold:

- agent and owner;
- task and business purpose;
- vendor/service and actual recipient;
- amount and asset;
- remaining task and period budget;
- whether the vendor/recipient is new;
- risk and simulation/validation result;
- exact approval expiry;
- whether the action is irreversible.

### 18.4 Status language

Use precise states:

- “Authorized” is not “Paid.”
- “Submitted” is not “Settled.”
- “Settled” is not “Output delivered.”
- “Output delivered” is not “Outcome verified.”
- “Balance observed” is not always “Available to spend.”
- “RPC reachable” is not “Base producing canonical blocks.”
- “Block production resumed” is not “FlowOps reconciliation complete.”

### 18.5 Accessibility and responsive requirements

- WCAG 2.2 AA target.
- Keyboard-complete core workflows.
- Visible focus and non-colour status indicators.
- Screen-reader labels for addresses, hashes, charts, and approvals.
- 320px mobile through large desktop layouts.
- 200% zoom support.
- Reduced-motion support.
- Approvals and emergency pause usable on mobile.

---

## 19. Notifications

### 19.1 MVP events

- Approval requested, approved, denied, or expired.
- Agent paused, quarantined, revoked, or reactivated.
- Policy changed.
- Budget 50%, 80%, and 100% consumed.
- Low wallet balance.
- Payment settled, failed, or unresolved.
- Settled without output.
- Unknown inbound or outbound transfer.
- Base suspected stall, confirmed halt, recovery started, and autonomous execution resumed.
- Credential created, rotated, or revoked.
- External dependency degradation affecting execution.

### 19.2 Delivery

- In-app notification center: MVP.
- Email: MVP for humans.
- Webhooks: MVP/P1 for systems.
- Slack/Teams: post-pilot.

Critical security events must not depend on a single delivery channel.

---

## 20. Non-functional requirements

### 20.1 Availability and recovery

- Control-plane target: 99.9% monthly availability after public launch.
- FlowOps authorization services are deployed independently from dashboard rendering; customer signers remain outside the FlowOps availability and key boundary.
- Recovery point objective for financial records: effectively zero committed-event loss through durable database and outbox design.
- Recovery time objective for core authorization/ledger: four hours initially, tightened after pilot.
- Documented provider-degradation, chain-halt, chain-recovery, and manual-reconciliation modes.

Base chain liveness is an external settlement dependency, not part of the FlowOps control-plane SLO. FlowOps must publish both states separately and must not show a green payment status merely because its own API is reachable.

This requirement is evidence-based: Base’s public incident record classifies the 25–26 June 2026 Mainnet Chain Stall as critical. An update states that a consensus problem caused an invalid block to be sequenced and prevented new blocks after block `47806542`; a similar halt recurred on 26 June before final resolution. Until an official postmortem supersedes the incident updates, FlowOps must use that public record without speculating beyond it.

**Required chain-halt posture:**

1. Enter `SUSPECTED_STALL` when block age/progression exceeds the configured threshold or independent observers disagree; pause new autonomous signing while collecting corroboration.
2. Enter `HALTED` when multiple independent observations confirm no canonical progression or Base reports a halt; stop new executable authorizations and customer-signer broadcasts.
3. Preserve approvals, tasks, reservations, quotes, broadcast hashes, and escrow evidence durably. Mark affected transactions `PENDING_CHAIN_RECOVERY`, not failed or settled.
4. Display last trusted block/time, affected rails/actions, current Base status, and the fact that onchain escrow release/refund clocks depend on chain state.
5. Do not rebroadcast, release budget, recognize settlement, or record a refund from cached, single-provider, facilitator-only, or wall-clock evidence.
6. Enter `RECOVERING` only after block production resumes. Cross-check independent heads, backfill from the last trusted checkpoint, resolve nonce/receipt/reorg outcomes, and re-evaluate expired quotes, approvals, policies, and signer authorizations.
7. Resume autonomous execution only after the configured stability window and all pre-halt ambiguous broadcasts have either reached a canonical outcome or been quarantined for manual resolution.

The Base-only MVP does not promise continuity of money movement during a Base halt. It promises truthful degradation, preserved intent/evidence, zero duplicate execution, and deterministic recovery on the same chain.

### 20.2 Performance

- Policy evaluation p95 under 100 ms excluding external risk calls.
- Internal authorization decision p95 under 250 ms when no external review is required.
- Dashboard primary views p95 under 2 seconds at pilot scale.
- Webhook event enqueue within 5 seconds of committed state change.
- Chain and x402 settlement latency displayed separately from FlowOps processing time.

### 20.3 Consistency

- Strong consistency for budget reservation, approval consumption, credential status, and execution authorization.
- Eventual consistency is acceptable for analytics and non-critical dashboard aggregates if freshness is visible.
- Financial state changes use database transactions and outbox publication.

### 20.4 Scale assumptions for v1

Design for, but do not prematurely optimize beyond:

- 1,000 organizations;
- 10,000 active agents;
- 100 financial intents per second burst;
- 1 million economic events per day;
- 90-day interactive event history plus archived retention.

Load testing must validate the actual pilot envelope before launch.

### 20.5 Observability

Every request and event must carry:

- request/correlation ID;
- organization and agent pseudonymous identifiers;
- task, intent, approval, execution, and transaction IDs where applicable;
- provider and environment;
- latency and outcome;
- redaction-safe reason codes.

Required operational dashboards:

- authorization rate and decision distribution;
- policy latency and errors;
- signer requests and denials;
- x402 quote/payment/output funnel;
- ambiguous and unresolved executions;
- reconciliation lag;
- unknown transfers;
- webhook backlog;
- dependency health.

---

## 21. Product analytics and success metrics

### 21.1 North-star metric

**Successfully governed agent transactions:** unique agent-initiated economic actions that were correctly authorized, settled, linked to a task, and reconciled with no manual correction.

Volume alone is not the north star; unsafe or duplicate volume is failure.

FlowOps product metrics must not depend on ERC-8021 attribution. FlowOps can measure an authorized action from its own task/intent records and independently verified Base settlement. Builder Code data is an external attribution/distribution signal, not the canonical source for product usage or financial accounting.

### 21.2 Activation metrics

- Time from organization creation to first active agent.
- Time from agent creation to first successful sandbox purchase.
- Percentage of registered agents completing a paid test workflow.
- Integration completion rate by MCP vs SDK.

### 21.3 Reliability metrics

- Payment success rate excluding valid policy denials.
- Duplicate-charge rate: target zero.
- Reconciliation exception rate.
- Median and p95 time to resolve ambiguous execution.
- Settled-without-output rate by service.
- Unknown transfer detection time.

### 21.4 Control metrics

- Percentage of spend covered by an explicit policy version: 100%.
- Percentage of completed payments linked to a task: 100% except documented system operations.
- Percentage of privileged changes with strong actor attribution: 100%.
- Approval turnaround time.
- Rate of denied or reviewed anomalous actions.
- Emergency-pause propagation time.

### 21.5 Business metrics

- Weekly active organizations.
- Weekly transacting agents.
- Retained transacting organizations after 4 and 12 weeks.
- USDC spend governed by FlowOps.
- Number of active approved services.
- Gross revenue and infrastructure cost per governed transaction.
- Percentage of Base transactions with parsed `VERIFIED_SUFFIX` attribution, reported separately by rail and facilitator.

### 21.6 Guardrail metrics

- Unauthorized settled payments: zero.
- Policy-bypass incidents: zero.
- Cross-tenant data events: zero.
- High-severity unresolved security findings at launch: zero.
- Customer funds exposed above configured pilot caps: zero.

---

## 22. Business model hypotheses

Pricing is a hypothesis to validate, not an MVP engineering dependency.

### 22.1 Candidate model

- Free developer sandbox with Base Sepolia.
- Team subscription based on active agents, users, approval/audit features, and retention.
- Usage fee per successfully governed mainnet payment or a platform volume tier.
- Enterprise plan for customer-managed signer, SSO, advanced policy, retention, private facilitator, and compliance integrations.

### 22.2 Pricing principles

- Do not charge for denied payments.
- Separate FlowOps fees from network, facilitator, wallet-provider, and vendor charges.
- Show fees before authorization.
- Avoid incentives that reward unsafe payment volume.

### 22.3 Value proof

FlowOps must demonstrate at least one of:

- reduced engineering time to add safe agent payments;
- reduced payment and credential risk;
- faster approval and procurement for micro-services;
- accurate per-agent cost attribution;
- ability to automate work that was previously blocked on manual checkout.

---

## 23. Differentiation

### 23.1 Against wallet products

Wallets provide an account and signer. FlowOps provides organization ownership, task context, multi-layer policy, approvals, vendor governance, reconciliation, and agent economics across wallets.

### 23.2 Against agent frameworks

Agent frameworks plan and call tools. FlowOps is model- and framework-neutral, and deterministically authorizes the economic side effects of those calls.

### 23.3 Against x402 infrastructure

x402 defines how a paid HTTP request can settle. FlowOps decides whether a specific agent should make that payment, secures the signer, attaches business context, and accounts for the result.

### 23.4 Against human expense-management tools

Traditional tools assume humans, cards, reimbursements, and monthly review. FlowOps is designed for high-frequency machine actors, sub-dollar calls, durable task handles, tool schemas, autonomous retries, and prompt-injection risk.

### 23.5 Defensible data advantage

With customer consent and privacy controls, FlowOps can accumulate unique operational signals:

- service success after payment;
- price and latency history;
- settled-without-output rate;
- agent cost by task type;
- policy patterns that prevent incidents;
- vendor reliability across agent workflows.

These signals must never be presented as universal reputation without adequate sample size and provenance.

---

## 24. Port-and-extend roadmap for the existing team

FlowOps is not a sixteen-week greenfield build. Snapfall already proves the hardest authorization concepts and Tollbooth already proves the escrow lifecycle. The plan is to preserve those invariants, port the chain-facing parts from Arc to Base, and build only the missing FlowOps product surfaces.

Dates remain estimates contingent on repository evidence, Base translation complexity, customer-signer integration, security review, and design-partner access. Existing code reduces implementation work; it does not waive new-chain testing or review.

### 24.1 Asset audit and disposition

Before editing the existing systems, create `PORT_INVENTORY.md` with one row per reusable component:

- source repository and path;
- immutable commit;
- owner/license;
- deployed Arc address and network;
- ABI/schema and storage assumptions;
- exact test command and current result;
- external dependencies;
- security findings or audit status;
- Arc-specific assumptions;
- disposition: carry unchanged, adapt, port, wrap, or rewrite;
- FlowOps owner and acceptance test.

The initial disposition is:

| Existing asset | Do not rebuild | Required FlowOps work |
|---|---|---|
| Snapfall policy engine | Rule evaluation semantics and deny/allow/approval result | Extend schemas for Base USDC, x402 quote fields, recipient/service identity, and atomic reservations |
| Snapfall Grant pattern | Bounded authority model | Bind grants to FlowOps task/intent digests and customer-signer authorization envelopes |
| Snapfall owner inbox | Approval lifecycle and decision UX | Bind approval to exact x402/direct/escrow intent; add quote, recipient, and delivery-rail context |
| Snapfall kill switch | Pause semantics | Enforce at API, policy, authorization-envelope issuance, and reference signer |
| Snapfall per-job isolation | Structural cross-job spending prevention | Map job to task and preserve the invariant in database, authorization, and contract boundaries |
| Snapfall waterfall | Settlement ordering and event/error expectations | Port and re-prove against Base USDC, Base receipts, gas, and event indexing |
| Tollbooth CallEscrow | Ack, optimistic release, expiry refund, and existing tests | Port/deploy on Base; integrate opted-in provider protocol, UI, ledger, and reconciliation |
| Existing test harnesses | Proven invariant and revert cases | Parameterize chain and add Base-specific fork/Sepolia tests |
| Reference signer sidecar | No existing proof identified | Build as a shippable customer artifact, not an internal sample |
| FlowOps Evidence Fetch provider | No existing proof identified | Build to bootstrap direct-x402 and escrow-compatible supply |

### 24.2 Arc-to-Base translation checklist

The port is not a chain-ID replacement. The team must explicitly resolve:

- Base chain IDs, RPCs, confirmation policy, reorg handling, and explorer verification;
- native USDC contract addresses, decimals, authorization behaviour, and testnet faucet asset;
- ETH gas versus any Arc-specific gas assumptions;
- paymaster compatibility and which transactions remain customer-paid;
- account model, customer signer, delegated signer, and EIP-1271/EOA compatibility where relevant;
- contract constructor/configuration addresses and upgrade/ownership posture;
- timestamp, expiry, replay, nonce, and domain-separation assumptions;
- event ordering, log decoding, custom errors, and indexer differences;
- direct x402 facilitator-submitted transactions versus customer-submitted contract calls;
- ERC-8021 suffixing for direct submissions and the current x402 `builder-code` extension (`a`, `s`, `w` fields);
- which Builder Codes are dashboard-claimed/minted versus returned by the agent wallet API, how each is represented in the Base registry/indexer, and whether one code may be used across direct and x402 Schema 2 attribution;
- whether the selected hosted facilitator and exact deployed version preserve the client-provided FlowOps `s` value and append the announced suffix;
- chain-specific KYT, recipient verification, and transaction simulation/validation;
- deployment scripts, verification, runbooks, and contract-address registry.

Every Snapfall/Tollbooth invariant claimed as proof must have a corresponding Base test. “Passed on Arc” is evidence of design maturity, not evidence of Base correctness.

### Phase 0: Evidence freeze and Base ADRs — 1 week

- Complete the port inventory with commits, tests, addresses, and ownership.
- Run all Snapfall and Tollbooth tests from clean checkouts.
- Freeze the exact invariants FlowOps promises to preserve.
- Write ADRs for customer-managed signing, x402 rail, escrow rail, Base confirmation, chain-halt detection/recovery, and contract ownership.
- Resolve the Builder Code issuance surfaces in the attribution ADR:
  1. claim or identify the dashboard-issued FlowOps app/service Builder Code and record its ERC-721 token/registry evidence, owner, payout address, and offchain metadata;
  2. call the unauthenticated agent Builder Code endpoint for the reference-signer wallet and record the deterministic returned code;
  3. determine from registry/base.dev evidence whether the agent API code is the same kind of minted/registered identifier as the dashboard code or a distinct wallet-derived surface;
  4. define the intended code inventory for direct FlowOps transactions, Evidence Fetch `a`, FlowOps client `s`, reference-signer/agent attribution, and facilitator-owned `w`; and
  5. decide and document where codes are intentionally reused versus separated before production metrics or grant claims combine them.
- Run the Base Sepolia facilitator-attribution experiment:
  1. configure a resource server to declare the x402 `builder-code` extension;
  2. configure the FlowOps client extension to add the FlowOps code as `s`;
  3. settle through the selected hosted facilitator;
  4. inspect the settlement transaction sender and complete calldata;
  5. parse the ERC-8021 marker/schema and confirm the expected `a`/`s`/`w` fields; and
  6. record facilitator URL/version, package version, transaction hash, calldata, decoded suffix, and result in the attribution ADR.

**Exit gate:** Reproducible evidence inventory and approved translation decisions. No component is called “reusable” solely from memory. Builder Code issuance/ownership and role mapping are recorded. The selected hosted-facilitator path is classified as `VERIFIED_SUPPORTED`, `VERIFIED_UNSUPPORTED`, or `UNRESOLVED`; because Base has announced x402 attribution as live, an unsupported result is treated as a facilitator-version/configuration defect or regression to investigate, not evidence that the feature does not exist.

### Phase 1: Base port and customer signer — 2 weeks

- Port Snapfall chain/configuration boundaries to Base Sepolia.
- Preserve policy, Grant, approval, kill-switch, and per-task isolation invariants.
- Implement authorization envelope schema and reference customer signer.
- Re-prove waterfall ordering, event order, and deliberate revert decoding on Base.
- Port Tollbooth `CallEscrow`; run the 43 inherited tests and new Base-specific tests.
- Add Builder Code attribution to compatible directly submitted transactions.
- Package the reference signer as a signed deployable sidecar with documentation and conformance suite.

**Exit gate:** A customer-controlled Sepolia signer executes only a valid one-time FlowOps authorization; paused, cross-task, expired, mutated, and replayed executions fail. The escrow expiry path refunds according to the ported contract.

### Phase 2: New agent-commerce surface — 2 weeks

- Implement x402 v2 client/gateway through the configured facilitator.
- Add Bazaar discovery and the FlowOps approved-service overlay.
- Build the FlowOps MCP server and TypeScript SDK.
- Bind x402 quote, request digest, recipient, and payment identifier to the Snapfall-derived authorization flow.
- Expose Direct x402 and Escrowed call as explicit, non-confusable rails.
- Ship FlowOps Evidence Fetch as the first useful provider supporting both rails.
- Configure the x402 builder-code extension according to the Phase 0 result.

**Exit gate:** An external agent completes one direct x402 purchase and one opted-in escrowed call through the customer signer on Base Sepolia.

### Phase 3: Evidence, reconciliation, and product integration — 2 weeks

- Implement Base transaction/facilitator reconciliation and ambiguous states.
- Extend the operational ledger and task evidence graph.
- Adapt the owner inbox with FlowOps approval context.
- Add escrow acknowledgement/release/refund timeline.
- Build webhooks, developer logs, dashboard, pause, quarantine, and incident views.
- Implement the Base `SUSPECTED_STALL`/`HALTED`/`RECOVERING` state machine, last-trusted-block checkpoint, customer-visible degradation, and controlled resume gate.
- Instrument provider-independent FlowOps metrics and separate verified/unverified Builder Code attribution by rail.

**Exit gate:** Direct, released, refunded, failed, and ambiguous cases reconcile correctly; injected response loss never creates a duplicate payment.

### Phase 4: Mainnet hardening and capped pilot — 2–3 weeks minimum

- External review of Base contracts, authorization protocol, and financial paths.
- Load, concurrency, replay, timeout-after-settlement, and prompt-injection tests.
- Customer-signer conformance testing with at least one design partner.
- Runbooks, backups, recovery, monitoring, and on-call drills.
- Reproduce a production-like Base chain halt with responsive-but-stale RPCs, staggered node recovery, pre-halt broadcasts, and escrow deadlines; prove no stale finalization or duplicate execution.
- Legal review focused on authorization, fees, escrow interface, and any delegated-wallet option.
- Deploy and verify Base mainnet contracts with recorded addresses.
- Launch with allowlisted services, customer-controlled signers, and conservative caps.

**Exit gate:** All P0 criteria pass, the final acceptance scenario succeeds without FlowOps custody of customer signing keys, and named engineering/security/product owners sign the release record.

### 24.3 Capacity-aware workstreams and schedule

The following are workstreams, not an assumption of four two-person teams:

1. **Protocol port:** Snapfall/Tollbooth inventory, Base contracts, invariant tests, deployment, verification.
2. **Signer and control plane:** reference sidecar, authorization envelopes, policy integration, ledger, reconciliation, webhooks.
3. **Agent commerce and supply:** x402, Builder Code experiment, Bazaar, MCP, SDK, and FlowOps Evidence Fetch.
4. **Product and assurance:** owner inbox adaptation, dashboard, UX, end-to-end/adversarial QA, release evidence, operations.

One technical owner controls the shared schemas—task ID, intent digest, authorization envelope, execution receipt, and economic event—regardless of headcount. Workstreams may run in parallel only after those contracts are versioned.

| Available implementation capacity | Planning range to capped pilot | Execution posture |
|---|---:|---|
| One primary builder using coding agents | 12–16 weeks | Run protocol/signer foundations first; overlap docs/tests/research through agents; avoid more than two integration fronts at once |
| Two to three experienced engineers | 9–12 weeks | Parallelize protocol/signer and commerce/provider work after Phase 0; share product/assurance |
| Four to eight coordinated contributors | 8–10 weeks | Run all workstreams with explicit integration gates; external review and design-partner readiness become the critical path |

The range expands if inherited code cannot be reproduced, Arc-specific assumptions are deep, the reference signer needs another wallet provider, or external review finds contract changes. Coding-agent subscriptions can accelerate implementation, test generation, documentation, and review; they do not replace ownership, design-partner operations, independent security assessment, or legal decisions.

Security audit and legal counsel remain external specialist dependencies.

### 24.4 Builder Codes and grant readiness

The mechanical constraint is precise: for EIP-3009 x402, the buyer signs a payment authorization but the facilitator builds and broadcasts the settlement transaction. A client cannot unilaterally append calldata to a transaction it does not construct.

Base publicly announced on 22 June 2026 that Builder Codes were live for x402 on Base, with app-level analytics and project attribution. Current public x402 documentation lists Builder Code as a server, client, and facilitator extension with TypeScript, Go, and Python support. The correct prior is therefore **shipped Base capability**, not merely reference-code potential.

The live mechanism is the `builder-code` extension:

- the resource server can declare app code `a`;
- the FlowOps client can attach its code as service code `s` in the payment payload; and
- a facilitator that registers `BuilderCodeFacilitatorExtension` can append an ERC-8021 Schema 2 suffix containing `a`, `s`, and its wallet code `w` during settlement.

The remaining transaction-level question is narrower: does the exact hosted facilitator and deployed version selected by FlowOps preserve the client-provided FlowOps `s` value and append the expected suffix on the chosen settlement path? The Phase 0 experiment remains mandatory because announcement-level availability is not evidence for a specific transaction, version, or configuration.

The public Base documentation also exposes two issuance surfaces that must not be conflated without evidence:

1. The Base dashboard/base.dev path describes Builder Codes as an ERC-721 collection, with codes claimed/minted for apps, wallets, or services and associated ownership/payout metadata.
2. The agent-developer API accepts a wallet address without authentication and deterministically returns a `bc_...` code for that wallet.

The documents reviewed do not explicitly prove that an agent-API code and a dashboard-minted code have identical token ownership, registry, metadata, and reward semantics. They may be two front doors to one system or distinct attribution products. FlowOps must resolve that mapping and define which registered code occupies each direct-transaction or x402 `a`/`s` role. It must not assume one returned string automatically covers every rail, identity, analytics view, or grant claim.

FlowOps therefore must:

- record the issuance, registry/token evidence, owner, payout metadata, purpose, and environment for every Builder Code it uses;
- append/verify it for customer-submitted direct and escrow contract calls;
- register the current x402 client extension so the FlowOps code is present as `s`;
- run the Phase 0 selected-facilitator calldata experiment before using “attributed x402 volume” in any metric or application;
- classify every transaction’s attribution evidence explicitly; and
- keep product analytics and the operational ledger independent of Builder Code/indexer visibility.

FlowOps is **not grant-ready as a build-candidate PRD**. A shipped-product or retroactive grant application is blocked until FlowOps has:

- a live Base product URL;
- a verified Base contract address where the application requires one;
- real users and honestly measured DAU/WAU;
- real Base transaction volume reconciled by FlowOps and independently observable onchain;
- a registered Builder Code, with attribution claims limited to rails/facilitators that have parsed suffix evidence; and
- a concise description of observed user value.

If the selected facilitator/version fails the announced x402 attribution path, FlowOps must report the defect honestly, test another supported facilitator/version if appropriate, and keep affected settlements in the verified-but-unattributed bucket. It must not label them as Builder Code volume. Grant-readiness is a distribution concern, not a reason to distort the product’s canonical metrics or to operate a facilitator prematurely.

FlowOps must not borrow another product’s contract address, users, DAU/WAU, or volume. Any existing shipped portfolio product may pursue its own application separately, but its traction is not FlowOps traction.

---

## 25. Risks and mitigation plan

| Risk | Impact | Probability | Mitigation |
|---|---|---:|---|
| Product scope expands into generic agent platform | Delivery failure | High | Preserve economic-control boundary and MVP exclusions |
| A fallback/delegated wallet or escrow posture creates unexpected regulatory exposure | Launch blocked or legal risk | Medium | Customer-managed signer default, early legal gate, explicit escrow scope, capped pilot |
| x402 ecosystem/API changes | Integration churn | Medium | v2 only, pinned versions, adapters, conformance tests |
| Agent pays malicious or low-quality service | Financial/data loss | High | Approved registry, low caps, output isolation, vendor evidence, approval |
| Duplicate charge after timeout | Direct loss | Medium | Idempotency, payment identifier, state machine, reconciliation-before-retry |
| Customer signer or FlowOps authorization key compromise | Direct loss | Low/High impact | Independent customer caps, one-time envelopes, key separation, pause, monitoring, rotation, revocation |
| Policy bug | Systemic loss | Medium | Property tests, formal invariants, shadow evaluation, rollout gates |
| Base/Coinbase dependency outage | Service unavailable | Medium | Durable pending states, adapters, fail closed, operational status |
| Base mainnet stops producing blocks, as occurred on 25–26 June 2026 | Settlement/refunds halt; broadcasts become ambiguous; reconciliation cannot advance | Medium | Explicit halt state, signer circuit breaker, last-trusted-block checkpoint, no rebroadcast, cross-provider recovery backfill, customer-visible degradation |
| Bazaar metadata/ranking treated as trust | Poor purchases | High | Organization approval separate from discovery and ranking |
| Tollbooth/Snapfall Arc assumptions do not hold on Base | Contract or accounting error | Medium | Evidence inventory, Base-specific invariant tests, independent review, capped deployment |
| Escrow is marketed as quality arbitration | Trust/legal issue | Medium | Limit promise to contract-defined acknowledgement/release/expiry/refund mechanics |
| First-party escrow supply is mistaken for third-party demand | False product-market signal | High | Treat Evidence Fetch as technical bootstrap only; require one independent conforming provider before general availability |
| Users misunderstand “non-custodial” | Trust/legal issue | Medium | Clear ownership/control disclosures and recovery explanation |
| Onchain payment succeeds but output fails | Customer dispute | Medium | Distinct state, evidence, vendor score, refund workflow |
| Agent spends without measurable value | Poor retention | High | Task binding, cost/outcome reporting, use-case templates |
| Parallel work fragments architecture as capacity grows | Rework/security gaps | Medium | One system owner, module ownership, shared contracts, ADRs, integration gates |

---

## 26. Testing strategy

### 26.1 Unit and property tests

- Policy precedence and conflict resolution.
- Budget windows and concurrent reservations.
- USDC integer arithmetic and formatting.
- Intent digest determinism.
- State-transition legality.
- Ledger balance invariant.
- Approval scope and expiry.
- Idempotency key reuse.
- Builder-code extension declaration, service-code composition, suffix parsing, and attribution-state classification.
- Evidence Fetch URL validation, normalization, content hashing, and private-network rejection.

### 26.2 Integration tests

- Customer signer capability negotiation, authorization verification, execution receipt, nonce-once enforcement, independent caps, revocation, upgrade, rollback, and failure modes.
- Optional CDP delegated/test wallet creation, signing, and failure modes.
- Base Sepolia funding and transfer verification.
- Snapfall waterfall/order/error invariants on Base.
- Tollbooth CallEscrow acknowledgement, release, expiry, refund, and race cases on Base.
- x402 v2 verify/settle and Bazaar discovery.
- Base Sepolia builder-code experiment through the selected hosted facilitator, including raw calldata capture and decoded `a`/`s`/`w` fields.
- FlowOps Evidence Fetch over direct x402 and CallEscrow, including objective delivery evidence and forced expiry.
- Facilitator timeout before and after settlement.
- Base Pay status verification if included.
- Webhook signature, retry, and replay.
- RPC/indexer disagreement.
- Base block production halts while RPC HTTP health checks remain green, followed by staggered provider/node recovery.
- Pre-halt transactions resolve after recovery as included, replaced, dropped, and reverted cases without duplicate payment.
- Escrow wall-clock deadlines pass during a chain halt without FlowOps fabricating release/refund state.

### 26.3 End-to-end tests

- Golden autonomous purchase.
- Approval-required purchase and durable resume.
- Denied purchase.
- Price/recipient change after quote.
- Credential revocation during pending approval.
- Organization pause during signing.
- Payment settled but output unavailable.
- Escrowed call released after valid lifecycle.
- Escrowed call expires and auto-refunds.
- Unknown outbound transfer.
- Chain reorg/reconciliation correction.
- Chain-halt banner, paused autonomous execution, checkpoint backfill, and controlled recovery-resume gate.
- Reference signer sidecar clean install by a design partner, trust revocation without FlowOps cooperation, and recovery after version rollback.

### 26.4 Adversarial tests

- Prompt injection embedded in purchased content.
- Malicious MCP tool metadata.
- SSRF through paid endpoint or webhook URL.
- Evidence Fetch attempts to reach loopback, link-local, private, redirected-private, and non-HTTP targets.
- Cross-tenant ID enumeration.
- Replay of sign-in, approval, payment, and webhook.
- Replay or mutation of customer-signer authorization envelope.
- Approval link forwarding.
- Attempt to pay unsupported token or network.
- Attempt to encode a different recipient in calldata.
- Attempt to release and refund the same escrowed call or spend its authority across tasks.
- Log and error-message secret extraction.
- Compromised or stale RPC falsely claims block progression during an actual halt.

### 26.5 Release evidence

Each release affecting money movement must include:

- changed invariants;
- threat-model delta;
- automated test evidence;
- migration and rollback plan;
- staged rollout/cap plan;
- named approvers from engineering and security.

---

## 27. Final MVP acceptance scenario

The MVP is accepted only when this scenario passes in a production-like environment:

1. An Owner creates a FlowOps organization.
2. The Owner invites a Developer and an Approver with distinct roles.
3. The Developer creates a Research Agent and receives a scoped MCP credential.
4. The Developer connects a customer-controlled Base Sepolia reference signer; FlowOps verifies its capabilities without receiving its key.
5. The Owner funds the customer-controlled test wallet with test USDC, and FlowOps verifies the deposit server-side.
6. The Owner assigns a policy:
   - Base and native USDC only;
   - approved research-service category;
   - $1 maximum automatic transaction;
   - $5 task cap;
   - $10 daily cap;
   - human approval above $1;
   - unknown recipients denied.
7. The external agent creates a task to produce a market brief.
8. It discovers FlowOps Evidence Fetch, confirms it is an approved x402 research service, and snapshots a $0.25 quote.
9. FlowOps authorizes the exact intent; the customer signer pays; FlowOps receives output, confirms settlement, and posts balanced ledger entries.
10. The task timeline shows agent, policy version, quote, recipient, payment, transaction, output, and receipt.
11. The agent requests a second $2 service.
12. FlowOps creates an approval request and does not sign.
13. Approver approves the exact intent; FlowOps re-evaluates and executes once.
14. A simulated lost response causes a retry; FlowOps returns the original execution instead of paying twice.
15. A third service changes its recipient after quote; FlowOps invalidates the authorization and blocks execution.
16. Owner pauses the agent; a new request is denied at both API authorization and signer boundary.
17. The agent calls FlowOps Evidence Fetch through the escrow rail; valid delivery evidence is acknowledged and the optimistic-release lifecycle completes onchain.
18. A second escrowed Evidence Fetch call is forced to fail before acknowledgement; expiry produces the contract-defined refund without manual fund movement or a fabricated database refund.
19. The acceptance run inspects the direct, released, and refunded transactions, records their ERC-8021 attribution classification, and never treats an unparsed suffix as attributed.
20. In a separate recovery segment, Base block production is simulated to stop after a newly authorized transaction is broadcast but before FlowOps confirms it; RPC endpoints remain reachable but stale.
21. FlowOps enters `HALTED`, shows the last trusted block/time and affected execution, stops new signer broadcasts, and records neither settlement nor refund from stale or wall-clock evidence.
22. After simulated block production resumes, FlowOps enters `RECOVERING`, backfills from its checkpoint, determines the original transaction’s single canonical outcome, re-evaluates expired authority, and resumes without rebroadcasting or double-paying.
23. All events—including the halt/recovery interval—can be exported and reconcile to observed Base transactions, escrow state, delivered outputs, and refunds.

No manual database editing, hidden operator signing, or private-key exposure is permitted during the acceptance run.

---

## 28. Launch checklist

### Product

- [ ] MVP included/excluded scope is reflected in UI and marketing.
- [ ] At least three design partners completed sandbox onboarding.
- [ ] At least two real x402 service categories validated.
- [ ] FlowOps Evidence Fetch is live as the first useful provider and passes direct-x402, successful escrow-release, and forced-expiry refund tests.
- [ ] Before general availability, at least one independent provider passes the escrow-provider conformance flow; the capped pilot may start with first-party supply only.
- [ ] Users understand available, reserved, pending, and settled balances.
- [ ] Approval view tested with non-crypto finance users.

### Engineering

- [ ] All P0 requirements pass.
- [ ] Golden acceptance scenario passes repeatedly.
- [ ] Migrations, backups, restore, and rollback tested.
- [ ] Provider outage and ambiguous settlement drills pass.
- [ ] Base chain-halt drill proves signer-side broadcast blocking, truthful degraded status, no stale finalization/refund, checkpoint backfill, and no duplicate execution after recovery.
- [ ] Snapfall/Tollbooth port inventory links every reused claim to a commit, test, and deployed configuration.
- [ ] A design partner deploys the reference signer sidecar without giving FlowOps its key; conformance, nonce-once, cap, revocation, upgrade, and rollback tests pass.
- [ ] Production and test credentials are isolated.
- [ ] The Phase 0 Builder Code experiment records a Base Sepolia transaction, raw calldata, decoded suffix, facilitator version, and one explicit classification: `VERIFIED_SUPPORTED`, `VERIFIED_UNSUPPORTED`, or `UNRESOLVED`.

### Security

- [ ] Threat model reviewed.
- [ ] External security assessment completed for financial paths.
- [ ] No unresolved critical/high findings.
- [ ] Pause and credential compromise drill completed.
- [ ] Actual recipient screening and post-settlement verification proven.

### Legal and operations

- [ ] Customer-managed signer posture, delegated-wallet option, and escrow legal characterization reviewed.
- [ ] Terms, privacy, risk, and irreversible-payment disclosures approved.
- [ ] Supported jurisdictions and acceptable-use policy set.
- [ ] Incident ownership and on-call established.
- [ ] Pilot limits and manual escalation documented.

### Metrics

- [ ] Authorization, payment, settlement, output, and reconciliation funnel instrumented.
- [ ] Duplicate-payment alert exists with target zero.
- [ ] Unknown-transfer alert tested.
- [ ] Dependency health and event freshness visible.
- [ ] DAU/WAU, transacting agents, reconciled transaction count and volume, attribution coverage by rail/facilitator, and FlowOps contract addresses are reported separately without borrowing metrics from another product.

---

## 29. Open product decisions

These decisions must be resolved through spikes or design-partner evidence, not assumptions:

1. Which customer-managed signer implementation and deployment model will the first design partner use?
2. When does the delegated-signing option meet the same customer-control and policy-scope standard as Model D?
3. What are the initial organization, agent, transaction, and daily caps?
4. Should v1 permit only allowlisted x402 services or allow reviewed Bazaar discovery below a micro-limit?
5. Which external risk/KYT evidence is mandatory beyond facilitator screening?
6. What confirmation level is sufficient for low-value Base payments?
7. How long should approvals, quotes, credentials, task data, and purchased outputs be retained?
8. Which task/output data may be stored, and which must remain in customer systems?
9. What is the first use-case template: research procurement, web/data extraction, compute, or developer tooling?
10. What legal and product obligations follow from offering the optional CallEscrow interface, and how is it distinguished from subjective dispute adjudication?
11. Should direct USDC payment enter the first pilot or follow x402-only validation?
12. When should Base Account Spend Permissions be introduced, and for funding or payment?
13. Which hosted x402 facilitator and exact deployed version will FlowOps use, and does a Base Sepolia settlement prove that path preserves FlowOps service code `s` in the ERC-8021 suffix? Base has announced the feature as live; treat this as selected-path conformance evidence, not a question about whether Builder Codes for x402 exist.
14. How do dashboard-claimed ERC-721 Builder Codes and unauthenticated wallet-derived agent API codes map to the Base registry, ownership/payout metadata, analytics, rewards, and x402 `a`/`s` roles? Decide whether FlowOps, Evidence Fetch, and the reference signer reuse or separate codes only after Phase 0 evidence.
15. What block-age threshold, observer quorum, Base-status signal, recovery stability window, and operator release gate move FlowOps among `SUSPECTED_STALL`, `HALTED`, `RECOVERING`, and `HEALTHY` without either unsafe continuation or excessive false pauses?

---

## 30. Glossary

**Agent:** A governed machine principal registered in FlowOps. It may be powered by any external agent framework.

**Agent credential:** A scoped secret or authentication mechanism that identifies an agent to FlowOps. It is not a wallet key.

**Approval:** Human authorization bound to an exact intent digest and expiry.

**Available balance:** Verified wallet balance minus reservations, unresolved outflows, and other holds recognized by FlowOps.

**Base Pay:** Base Account SDK flow for human-friendly USDC payment on Base.

**Budget:** Maximum economic authority available to an agent, task, category, or period.

**Economic Event Graph:** FlowOps record connecting organization, agent, task, intent, policy, approval, execution, settlement, output, and ledger.

**Execution:** An attempt to sign, submit, and settle one authorized intent.

**Intent:** Canonical description of a proposed financial action before authorization.

**MCP:** Model Context Protocol, used to expose structured FlowOps tools to agents.

**Policy:** Immutable versioned rules that determine whether an intent is allowed, denied, reviewed, or sent for approval.

**Reconciliation:** Process of comparing expected economic events with wallet-provider, facilitator, and Base chain evidence.

**Reservation:** Atomic hold against a budget that prevents concurrent overspend before settlement.

**Spend Permission:** Base Account primitive authorizing a designated spender to move bounded assets from an account.

**Task:** Unit of work that provides business purpose, budget, and outcome context for agent actions.

**x402:** Open HTTP payment protocol using `402 Payment Required` and signed payment payloads for programmatic settlement.

---

## 31. Authoritative external references

The following links describe current external capabilities as of this document date. FlowOps must pin dependency versions and revalidate behaviour during implementation.

- [Base Account overview](https://docs.base.org/base-account/overview/what-is-base-account)
- [Base Sub Accounts](https://docs.base.org/base-account/improve-ux/sub-accounts)
- [Base Spend Permissions](https://docs.base.org/base-account/improve-ux/spend-permissions)
- [Base Pay: accept payments](https://docs.base.org/base-account/guides/accept-payments)
- [Base recurring payments](https://docs.base.org/base-account/guides/accept-recurring-payments)
- [Base resources for AI agents](https://docs.base.org/get-started/resources-for-ai-agents)
- [Base documentation MCP](https://docs.base.org/get-started/docs-mcp)
- [CDP non-custodial wallets](https://docs.cdp.coinbase.com/wallets/non-custodial-wallets/overview)
- [CDP delegated signing](https://docs.cdp.coinbase.com/wallets/using-wallets/delegated-signing)
- [CDP Agentic Wallet](https://docs.cdp.coinbase.com/agentic-wallet/cli/welcome)
- [CDP Paymaster](https://docs.cdp.coinbase.com/paymaster/introduction/quickstart)
- [x402 overview](https://docs.cdp.coinbase.com/x402/welcome)
- [x402 protocol flow](https://docs.cdp.coinbase.com/x402/core-concepts/how-it-works)
- [x402 facilitator](https://docs.cdp.coinbase.com/x402/core-concepts/facilitator)
- [x402 Bazaar discovery](https://docs.cdp.coinbase.com/x402/bazaar)
- [x402 network support](https://docs.cdp.coinbase.com/x402/network-support)
- [x402 public Builder Code extension documentation](https://docs.x402.org/extensions/builder-code)
- [x402 builder-code extension specification at audited reference commit](https://github.com/x402-foundation/x402/blob/1d15062628b086b497ca10bb9b4c675a528c864e/specs/extensions/builder_code.md)
- [Base announcement: Builder Codes live for x402, 22 June 2026](https://x.com/buildonbase/status/2069102951960904137)
- [Base Builder Codes overview and dashboard-issued ERC-721 description](https://docs.base.org/apps/builder-codes/builder-codes)
- [Base Builder Codes for app developers](https://docs.base.org/apps/builder-codes/app-developers)
- [Base Builder Codes for agent developers](https://docs.base.org/apps/builder-codes/agent-developers)
- [Base Mainnet Chain Stall incident, 25–26 June 2026](https://status.base.org/incidents/5c4gm1wzbjs4)
- [Base funding pathways and retroactive grant posture](https://docs.base.org/get-started/get-funded)

---

## 32. Product decision

FlowOps should proceed as a **port-and-extend**, Base-only economic operating system for agents. Snapfall supplies the proven authorization kernel; Tollbooth supplies the optional delivery-assurance kernel; FlowOps adds the customer signer boundary, x402/Bazaar/MCP commerce surface, Base reconciliation, and economic evidence product. The team should not begin by building a broad agent runtime or marketplace. It should first prove two trustworthy economic loops:

> **Task → quote → deterministic policy → approval when required → Base USDC payment → purchased output → verified settlement → reconciled ledger.**

> **Task → escrowed call → provider acknowledgement/delivery → optimistic release, or expiry → automatic refund → reconciled ledger.**

If that loop is safe, reliable, understandable, and valuable, FlowOps has the kernel from which the larger AgentOS vision can grow.
