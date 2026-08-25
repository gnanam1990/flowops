# ADR-0063: Dashboard browser acceptance boundary

Status: accepted

## Context

The private dashboard needs browser-level acceptance coverage for rendering,
responsive layout, and command wiring. Unit rendering alone does not exercise
the browser runtime. Letting acceptance tests call a deployed control plane or
reuse production membership credentials would make CI nondeterministic and
would collapse the test and production trust boundaries.

## Decision

The dashboard pins `@playwright/test` through the dashboard lockfile and updates
it only through reviewed dependency changes. CI installs the matching Playwright
Chromium build and runs the acceptance suite in desktop and mobile viewports.
No other browser channel is part of this gate.

The Playwright web server and its control API fixture listen only on loopback.
The fixture returns deterministic test-only health, membership, snapshot, and
command responses. It does not proxy a real FlowOps environment and does not
hold a wallet, signer, Base RPC credential, production database credential, or
production Sites membership.

Browser tests use a local Sites exchange value and locally issued fixture
session values solely to exercise server-side exchange and command binding.
These values are test-only, have no authority outside the fixture process, and
must never be copied into deployment configuration. Fixture responses are also
test-only and are not evidence of production readiness, on-chain state, or a
successful mainnet payment.

Local authentication used by browser acceptance is restricted to loopback and
does not grant organization membership. Authorization-sensitive assertions
continue to require the fixture's explicit organization, principal, step-up,
approval digest, and chain bindings.

## Consequences

- Browser acceptance remains deterministic and cannot spend funds or mutate a
  production control plane.
- Chromium installation and the Playwright package become reviewed CI supply
  chain inputs covered by the dashboard lockfile.
- Passing browser acceptance proves only the local fixture exchange and UI
  contract; production identity, network, database, and Base mainnet evidence
  remain separate release gates.

## Rejected alternatives

- Reusing production credentials in CI: expands credential exposure and can
  mutate live state.
- Calling a shared staging API: introduces external drift and weakens replay.
- Treating server-rendered unit tests as browser acceptance: misses browser and
  responsive behavior.
