# FlowOps operator dashboard

The dashboard is the human control surface for FlowOps. It keeps agent budgets,
exact-intent approvals, Base chain health, economic evidence, and emergency
controls visible in one place.

The module renders membership-authorized live control-plane reads when its
server environment is configured, and otherwise falls back to an immutable,
explicitly labelled preview. In live mode, approval and organization-pause
commands require a separate fresh credential bound to the exact same member,
organization, and role. The read session itself deliberately has no step-up
authority.

## Local development

Requirements: Node.js 22.13 or newer.

```bash
npm ci
npm run dev
```

Open `http://localhost:3000`.

Copy `.env.example` to an ignored local environment file only when testing
against an authorized control plane. The exchange token is server-only and must
be configured through Sites environment variables in hosted deployments.

## Verification

```bash
npm run lint
npm test
npm audit --omit=dev --audit-level=high
```

`npm test` produces the vinext/Cloudflare build and renders it through the
Worker entry point. The rendered test checks core FlowOps content and rejects
starter copy or fabricated preview success states.

## Boundaries

- ChatGPT/Sites identity headers never grant FlowOps membership by themselves.
  Live reads require the exact server-side exchange defined by ADR-0011.
- The Go control plane remains the canonical application-data and write path.
- Step-up credentials are held only in browser memory for one request. Local
  recovery stores only a command ID, or before one exists, a random operation
  ID and non-secret action digest so the exact retry stays idempotent.
- No D1 or R2 binding is configured for this module.
- Runtime dependencies must pass the high-severity audit gate. The vinext build
  tool currently brings a development-only `image-size` advisory; FlowOps does
  not accept untrusted image input and production dependencies audit clean.
