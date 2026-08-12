# Operator dashboard module

Status: membership-bound live reads implemented; economic writes remain gated

Package: `apps/dashboard`

## Purpose

The dashboard gives an operator one control room for governed agents and their
money. It answers what is spendable, what is reserved, what is awaiting chain
evidence, what is unresolved, which human decisions are pending, and whether
Base is producing a trusted canonical head.

## Entry and identity flow

The root route reads ChatGPT/Sites identity headers on the server. Anonymous
local preview remains available. For a signed-in viewer, the Sites server
derives a project-bound user hash and exchanges it with the control plane using
a server-only project credential. Live data appears only when the exact user,
email digest, project, and ACTIVE FlowOps membership match. Identity headers
alone never authorize an organization or economic command; ADR-0011 defines
the session boundary.

## Implemented views

- Overview with separately labelled available, reserved, pending, and unresolved balances.
- Exact-intent approval inbox with amount, recipient, rail, expiry, reason, and frozen-intent explanation.
- Agent directory with purpose, current task, signer state, cap, and spend.
- Economic activity timeline spanning approval, settlement, escrow release, refund, and security events.
- Security and recovery surface showing observer agreement, last trusted block, risks, and no-silent-retry posture.
- Developer surface for the MCP connection shape, redacted request outcomes, and dependency health.
- Keyboard-accessible approval drawer and emergency-pause confirmation.

## Live and preview behavior

The adapter maps live organization, governed-agent, pending-approval, and Base
checkpoint fields. It does not infer balances, budgets, logs, signer health, or
facilitator health that the API does not expose; those values are visibly
unavailable. Missing configuration, identity, membership, or valid upstream
data falls back to a fully labelled preview rather than mixing sources.

Approve, deny, and emergency pause controls remain locked in both modes. A live
read session has no step-up claim and only explains how to continue; it sends no
command. Preview interactions likewise produce only a local notice. Filters and
copying the MCP configuration are local UI operations and are allowed.

## Inputs and outputs

The input is a typed `DashboardSnapshot` produced by either an immutable preview
fixture or the strict server adapter. The rendered output is a dynamic server
page and Cloudflare Worker bundle. The snapshot includes
generation time, organization label, chain health, economic buckets, pending
approvals, agents, activity, and risks.

Future economic mutations must use a separate step-up session and return a
durable command reference and authoritative result rather than mutating
dashboard state optimistically.

`GET /api/flowops/enrollment` is the owner bootstrap bridge. For an
authenticated Sites viewer it returns the configured project ID, Sites email,
and derived SHA-256 site-user key. It is non-cacheable, never returns the raw
Sites user ID, and grants no FlowOps access by itself. The endpoint returns 401
without Sites identity and 503 until the project binding is configured.

## Failure states

- Missing Sites identity: render anonymous preview; do not infer membership.
- Missing control plane: show preview mode and keep writes locked.
- Unmapped or revoked membership: show preview mode; do not reveal another
  organization's existence.
- Malformed or cross-organization upstream response: discard it completely and
  show preview mode.
- Stale or disputed Base observations: display the last trusted block and
  unresolved state; do not report settlement or recovery.
- Command timeout or ambiguous response: keep the command unresolved and offer
  correlation evidence; never display success from browser state alone.
- Clipboard denial: show `Copy unavailable` without affecting other controls.

## Verification

```sh
npm ci --prefix apps/dashboard
npm audit --omit=dev --audit-level=high --prefix apps/dashboard
npm run lint --prefix apps/dashboard
npm test --prefix apps/dashboard
make smoke-dashboard
```

The rendered tests verify core FlowOps content, dynamic metadata, the full
server-side identity exchange, derived enrollment-code isolation, live field
mapping, tenant-ID agreement, absence of credentials in HTML, removal of
starter content, and absence of fake success claims. The production dependency
graph must contain no high severity advisory.

The vinext development tool currently depends on an `image-size` release with a
high-severity denial-of-service advisory. It is excluded from the production
dependency audit, and this dashboard accepts no untrusted image input. Upgrade
or remove that tool dependency before any feature accepts user-supplied images.

## Remaining live-write acceptance criteria

- Organization A cannot command organization B.
- Roles and fresh step-up authentication are enforced server-side.
- Approval binds the exact frozen intent and remains idempotent across retries.
- Emergency pause fails closed and exposes a durable audit correlation ID.
- Available, reserved, pending, and unresolved totals reconcile with the Go
  ledger before they replace the current unavailable labels.
- Base halt and recovery states propagate without stale success.
- Accessibility checks cover keyboard focus, dialog dismissal, labels, contrast,
  reduced motion, and mobile navigation.
