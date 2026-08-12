# ADR-0012: Control-plane production runtime and owner bootstrap

Status: accepted for the capped read-only pilot

## Context

The control-plane API already bound bearer credentials to PostgreSQL principals,
but it could not be deployed safely without manual database edits. It also
accepted only a loopback listener behind a same-host TLS proxy. The selected
pilot runtime supplies a dynamic port and terminates public TLS at an edge
proxy. The private Sites dashboard needs one exact site-bound owner membership
before it can exchange its server-only credential for a short-lived read
session.

## Decision

FlowOps ships one production container containing the control-plane API and a
separate administrative executable. The API process runs as an unprivileged
user. A minimal root entrypoint prepares the root-mounted reconciliation volume,
drops privileges, and never reads or prints application secrets. It rejects a
relative, traversing, root, or out-of-volume journal path before changing any
filesystem ownership.

Migrations are an operator action. Production runs the API with
`FLOWOPS_APPLY_MIGRATIONS=false` after a privileged administrative invocation
applies the immutable migrations and the database grants a separate runtime
role only its required permissions. The default remains enabled for local
development, so production configuration must set the variable explicitly.

The API may bind a non-loopback address only when
`FLOWOPS_TRUST_PROXY_HEADERS=true`. In that mode every route except `/health`
requires the first `X-Forwarded-Proto` value to be `https`. This mode is valid
only when the service port is reachable exclusively through the selected
platform edge. It must not be enabled on a directly reachable host or a network
where another tenant can connect to the container port and forge proxy headers.

`flowops-admin sites-bootstrap-owner` reads one strict JSON object from stdin,
applies immutable migrations, and atomically creates or verifies:

- the organization;
- one enabled Sites identity provider with a hashed exchange token;
- one exact site-user/email-bound owner membership; and
- an append-only audit event.

Replaying the exact desired state is a no-op. A different organization name,
token, email, principal, membership, role, project, or revoked state fails with
a conflict. Bootstrap never rotates an existing token.

`flowops-admin sites-rotate-token` requires the named ACTIVE owner membership to
match both the actor and organization. It atomically replaces only the token
digest and appends an audit event. Neither command accepts secrets in command
line arguments or writes a plaintext token to the database or command output.

`flowops-admin sites-disable-provider` is the fail-closed access kill switch.
The exact ACTIVE owner may atomically disable the provider and append an audit
event. Existing dashboard sessions are rejected on their next API request.
Re-enablement is deliberately absent from the capped-pilot command surface.

Each Sites identity provider is structurally bound to exactly one organization.
Bootstrap rejects reuse of a project across organizations, and session exchange,
session authentication, rotation, and disablement all require the membership's
organization to equal the provider binding.

The private Sites app exposes `/api/flowops/enrollment` to an authenticated
viewer. It returns the project ID, email supplied by Sites, and the
SHA-256 site-user key required by the bootstrap command. It never returns the
raw Sites user ID and is explicitly non-cacheable. The Sites access policy,
not this endpoint, remains responsible for restricting the pilot to its owner.

## Consequences

- Production bootstrap and rotation no longer require hand-edited SQL.
- The exchange token must be generated outside FlowOps and delivered through
  stdin to the admin command and through Sites secret storage to the dashboard.
- The pilot uses one API replica because the file-backed reconciliation journal
  has an exclusive lock and a persistent volume. Multi-replica control-plane
  operation remains blocked until reconciliation persistence is moved to a
  shared single-writer store.
- A platform edge outage or missing forwarded-protocol header fails protected
  routes closed while leaving the non-sensitive deployment health check usable.
- This decision authorizes live organization-scoped reads only. It does not
  activate step-up writes, wallet operations, or Base transaction submission.

## Rejected alternatives

- Manual production inserts: unaudited, substitution-prone, and not repeatable.
- A public bootstrap HTTP endpoint: creates a high-value remote administration
  surface before the membership boundary exists.
- Storing the exchange token plaintext for later recovery: unnecessary and
  expands the credential breach surface.
- Trusting proxy headers by default: makes a direct deployment vulnerable to
  forged transport claims.
