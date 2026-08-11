# Operator dashboard module

Status: preview-safe interface implemented; live control-plane integration gated

Package: `apps/dashboard`

## Purpose

The dashboard gives an operator one control room for governed agents and their
money. It answers what is spendable, what is reserved, what is awaiting chain
evidence, what is unresolved, which human decisions are pending, and whether
Base is producing a trusted canonical head.

## Entry and identity flow

The root route reads optional ChatGPT/Sites identity headers on the server and
uses them only to personalize the viewer label. Anonymous local preview remains
available. Identity headers alone never authorize a FlowOps organization or
economic command; ADR-0009 defines the production membership boundary.

## Implemented views

- Overview with separately labelled available, reserved, pending, and unresolved balances.
- Exact-intent approval inbox with amount, recipient, rail, expiry, reason, and frozen-intent explanation.
- Agent directory with purpose, current task, signer state, cap, and spend.
- Economic activity timeline spanning approval, settlement, escrow release, refund, and security events.
- Security and recovery surface showing observer agreement, last trusted block, risks, and no-silent-retry posture.
- Developer surface for the MCP connection shape, redacted request outcomes, and dependency health.
- Keyboard-accessible approval drawer and emergency-pause confirmation.

## Preview behavior

All records are explicitly marked as preview data. Approve, deny, and emergency
pause controls can demonstrate the interaction but only produce a local notice
that the authenticated control plane is not connected. They never claim that a
real command succeeded. Filters and copying the MCP configuration are local UI
operations and are allowed.

## Inputs and outputs

The current input is a typed immutable `DashboardSnapshot`. The rendered output
is a dynamic server page and Cloudflare Worker bundle. The snapshot includes
generation time, organization label, chain health, economic buckets, pending
approvals, agents, activity, and risks.

The live adapter must preserve these types while replacing fixtures with
authorized control-plane responses. Economic mutations must return a durable
command reference and authoritative result rather than mutating dashboard state
optimistically.

## Failure states

- Missing Sites identity: render anonymous preview; do not infer membership.
- Missing control plane: show preview mode and keep writes locked.
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

The rendered tests verify core FlowOps content, dynamic metadata, Base and
emergency-control visibility, removal of starter content, and absence of fake
preview success claims. The production dependency graph must contain no high
severity advisory.

The vinext development tool currently depends on an `image-size` release with a
high-severity denial-of-service advisory. It is excluded from the production
dependency audit, and this dashboard accepts no untrusted image input. Upgrade
or remove that tool dependency before any feature accepts user-supplied images.

## Live-mode acceptance criteria

- Organization A cannot read or command organization B.
- Roles and step-up authentication are enforced server-side.
- Approval binds the exact frozen intent and remains idempotent across retries.
- Emergency pause fails closed and exposes a durable audit correlation ID.
- Available, reserved, pending, and unresolved totals reconcile with the Go ledger.
- Base halt and recovery states propagate without stale success.
- Accessibility checks cover keyboard focus, dialog dismissal, labels, contrast,
  reduced motion, and mobile navigation.
