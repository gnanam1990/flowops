# FlowOps operator dashboard

The dashboard is the human control surface for FlowOps. It keeps agent budgets,
exact-intent approvals, Base chain health, economic evidence, and emergency
controls visible in one place.

The current module deliberately renders an immutable preview snapshot. Actions
that would change economic state are disabled until the authenticated FlowOps
control-plane adapter is connected; the UI never reports a preview action as a
real approval, denial, payment, refund, or pause.

## Local development

Requirements: Node.js 22.13 or newer.

```bash
npm ci
npm run dev
```

Open `http://localhost:3000`.

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

- ChatGPT/Sites identity headers may personalize the viewer display. They do
  not grant FlowOps organization membership or authorization by themselves.
- The Go control plane remains the canonical application-data and write path.
- No D1 or R2 binding is configured for this module.
- Runtime dependencies must pass the high-severity audit gate. The vinext build
  tool currently brings a development-only `image-size` advisory; FlowOps does
  not accept untrusted image input and production dependencies audit clean.
