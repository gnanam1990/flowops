# ADR-0011: Sites identity exchange and dashboard sessions

Status: Accepted
Date: 2026-08-12

## Decision

FlowOps treats the ChatGPT/Sites identity headers as authentication evidence for
one Sites project, not as FlowOps organization authority. A viewer reaches live
dashboard data only through an explicit server-to-server exchange:

1. The Sites server hashes the exact Sites project ID and opaque user ID into a
   site-bound user key. The raw Sites user ID never reaches the control plane.
2. The control plane verifies the project-specific exchange credential, the
   site-bound user key, the normalized email digest, and an ACTIVE provisioned
   membership in one lookup.
3. The control plane returns a two-minute HMAC-authenticated session bound to
   the exact membership, project, user key, organization, principal, and role.
4. Every use re-reads that exact membership and the provider's enabled state.
   Provider disablement, membership revocation, or a role change makes an
   already-issued session fail immediately.

The session is explicitly read-only and never contains a step-up claim. Every
write permission is denied even when the membership role is Owner. Approval,
denial, pause, intent creation, and authorization issuance require a separate
fresh authentication ceremony and are not part of this module.

## Trust boundaries

- `FLOWOPS_SITES_EXCHANGE_TOKEN` is a server-only credential shared by one
  Sites deployment and the control plane. Only its SHA-256 digest is stored in
  PostgreSQL.
- `FLOWOPS_SITE_SESSION_KEY_B64` is a 32-byte control-plane signing secret. It
  is independent of the authorization-envelope key and all customer signers.
- The Sites worker receives the short session only on the server. It fetches
  the snapshot server-side and never serializes either credential into HTML or
  client state.
- Membership provisioning and credential rotation remain an owner bootstrap
  operation. Production rows must not be created by ad hoc database editing.

## Failure posture

Missing headers, incomplete runtime configuration, an unmapped identity,
upstream timeout, malformed data, organization substitution, or an expired
session all result in an explicitly labelled preview. The adapter never mixes
live records with preview financial aggregates. Fields not exposed by the
control-plane snapshot are shown as unavailable.

## Consequences

- The dashboard can show live organization, agent, pending-approval, and Base
  checkpoint data without becoming an authority or another ledger.
- Sites membership revocation is checked on every control-plane read rather
  than waiting for token expiry.
- A live viewer still cannot perform economic writes from the read session.
- Deploying this code without the three Sites environment variables remains a
  safe preview deployment.
