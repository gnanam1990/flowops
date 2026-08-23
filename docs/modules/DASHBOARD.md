# Operator dashboard module

Status: membership-bound PostgreSQL reads, step-up command bridge, ASCP subledger projection, and journal-backed reconciliation aggregates implemented; hosted step-up issuance remains gated

Package: `apps/dashboard`

## Purpose

The dashboard gives an operator one control room for governed agents and their
economic controls. It answers what policy capacity remains per agent, what is reserved, what is awaiting chain
evidence, what is unresolved, which human decisions are pending, and whether
Base is producing a trusted canonical head.

The live snapshot includes the tenant-scoped reconciliation read model:
canonical checkpoint progress, unresolved and quarantined outcomes, pending
finality, operator-resume readiness, and journal-derived asset summaries. It
shows recognized expense and escrow-locked amounts only in atomic units for an
asset proved by the immutable execution or escrow reference. It never labels
the operational subledger as a wallet balance or available treasury. Entries
without a proved asset binding are excluded and counted visibly.

## Entry and identity flow

The root route reads ChatGPT/Sites identity headers on the server. Anonymous
requests receive public operational health or an explicit unavailable state—never example organization data. For a signed-in viewer, the Sites server
derives a project-bound user hash and exchanges it with the control plane using
a server-only project credential. Live data appears only when the exact user,
email digest, project, and ACTIVE FlowOps membership match. Identity headers
alone never authorize an organization or economic command; ADR-0011 defines
the session boundary.

## Implemented views

- Overview with separately labelled ASCP subledger delta, reserved, pending, and unresolved amounts.
- Exact-intent approval inbox with amount, recipient, rail, expiry, reason, and frozen-intent explanation.
- Agent directory with purpose, current task, signer state, cap, and spend.
- Economic activity timeline spanning approval, settlement, escrow release, refund, and security events.
- Security and recovery surface showing observer agreement, last trusted block, risks, and no-silent-retry posture.
- Developer surface for the MCP connection shape, redacted request outcomes, and dependency health.
- Keyboard-accessible approval drawer and emergency-pause confirmation.

## Live and unavailable behavior

The adapter maps live organization, governed-agent, legacy and ASCP pending
approvals, active-policy daily capacity, reservation exposure, finalized ASCP
ledger effects, durable payment activity, and Base checkpoint fields. It does
not infer an ERC-20 wallet balance, spendable treasury, or chain outcome from a
database state. Missing configuration, identity, membership, or valid upstream
data falls back to public health or a fully labelled unavailable state.

Public and unavailable mutations remain locked. In live mode, approve, deny, and organization
pause accept a fresh step-up credential in a password field held only in client
memory. A same-origin server bridge exchanges the Sites identity again, reads
the step-up credential's safe claims from the control plane, and requires an
exact organization, principal, and role match before sending a command. The
bridge re-reads the pending approval and supplies its current full request
digest; the browser cannot choose the authoritative digest.

The browser records an unresolved command ID, or before an ID is known, a random
operation ID plus a digest of the non-secret action fields. It stores neither
the action body nor the credential. An exact retry reuses that operation ID and
therefore the same server idempotency key; a different command is blocked until
the unresolved action is recovered. Commands with an ID are recovered through
the read-only Sites session, and the bridge rejects any command whose actor is
not the exact Sites principal. Success is never inferred from a click, timeout,
or local state. The step-up credential is not stored, logged, returned, embedded
in HTML, or used in the idempotency key. A hosted deployment remains write-inert
until a production identity system issues the required short-lived credential.

## Inputs and outputs

The input is a typed `DashboardSnapshot` produced by the strict server adapter.
Tests use deterministic fixtures, but no fixture is compiled into runtime paths. The rendered output is a dynamic server
page and Cloudflare Worker bundle. The snapshot includes
generation time, organization label, chain health, economic buckets, pending
approvals, agents, activity, and risks.

Economic mutations use a separate step-up credential and return a durable
command reference and authoritative result rather than mutating dashboard state
optimistically. Organization pause persists in PostgreSQL, serializes against
in-flight authorization issuance, blocks future authorization checks, and
returns both command and append-only audit correlation IDs.

`GET /api/flowops/enrollment` is the owner bootstrap bridge. For an
authenticated Sites viewer it returns the configured project ID, Sites email,
and derived SHA-256 site-user key. It is non-cacheable, never returns the raw
Sites user ID, and grants no FlowOps access by itself. The endpoint returns 401
without Sites identity and 503 until the project binding is configured.
`GET /enrollment` provides the same identity as an authenticated owner-facing
HTML page so operators can complete bootstrap without raw API navigation. It
also excludes the raw Sites user ID and grants no control-plane access.

## Failure states

- Missing Sites identity: render public health or unavailable status; do not infer membership.
- Missing control plane: show unavailable mode and keep writes locked.
- Unmapped or revoked membership: show unavailable mode; do not reveal another
  organization's existence.
- Malformed or cross-organization upstream response: discard it completely and
  show unavailable mode.
- Stale or disputed Base observations: display the last trusted block and
  unresolved state; do not report settlement or recovery.
- Command timeout or ambiguous response: keep the command unresolved and offer
  correlation evidence; never display success from browser state alone.
- Step-up token belongs to another principal, organization, or role: reject
  before the economic endpoint is called.
- Approval disappeared between render and action: reject and require refresh;
  never submit the stale browser digest.
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
mapping, tenant-ID agreement, exact-principal step-up binding, server-refetched
approval digests, durable command recovery, absence of credentials in HTML,
removal of starter content, and absence of fake success claims. The production
dependency graph must contain no high severity advisory.

The vinext development tool currently depends on an `image-size` release with a
high-severity denial-of-service advisory. It is excluded from the production
dependency audit, and this dashboard accepts no untrusted image input. Upgrade
or remove that tool dependency before any feature accepts user-supplied images.

## Remaining acceptance criteria

- A production identity provider issues and revokes the short-lived step-up
  credential; a static pre-provisioned credential is not an accepted ceremony.
- Reconcile the ASCP PostgreSQL subledger projection against periodic independent
  ERC-20 balance observations without relabelling subledger delta as wallet balance.
- Base halt and recovery states propagate without stale success.
- Accessibility checks cover keyboard focus, dialog dismissal, labels, contrast,
  reduced motion, and mobile navigation.
