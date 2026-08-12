# Control-plane capped-pilot deployment

## Allowed posture

This runbook deploys one control-plane replica for owner-only live dashboard
reads. Economic writes remain disabled in the Sites dashboard. Do not provision
agent credentials, active policies, step-up sessions, wallet signers, or
production Base value as part of this procedure.

## Required infrastructure

- managed PostgreSQL with encrypted connectivity, backups, point-in-time
  recovery, monitoring, and a least-privilege runtime role;
- one persistent volume mounted for the reconciliation journal;
- one public HTTPS edge whose service port is not directly reachable;
- a secret manager for the database URL, Ed25519 envelope private key, 32-byte
  Sites session key, and Sites exchange token; and
- the private owner-only Sites project recorded in `.openai/hosting.json`.

The checked-in `Dockerfile` builds both `/flowops/control-plane-api` and
`/flowops/flowops-admin`. `railway.json` selects that image, checks `/health`,
allows graceful draining, and restarts only failed processes. The runtime
entrypoint prepares the mounted journal directory and drops to UID/GID 10001
before the API starts.

## Runtime variables

The API service requires:

| Variable | Requirement |
|---|---|
| `FLOWOPS_DATABASE_URL` | PostgreSQL URL supplied by the managed database |
| `FLOWOPS_ENVELOPE_KEY_ID` | Versioned identifier for the capability-signing key |
| `FLOWOPS_ENVELOPE_PRIVATE_KEY_B64` | Canonical 64-byte Ed25519 private key, base64 |
| `FLOWOPS_SITE_SESSION_KEY_B64` | Exactly 32 random bytes, base64 |
| `FLOWOPS_TRUST_PROXY_HEADERS` | `true` only behind the exclusive HTTPS edge described in ADR-0012 |
| `FLOWOPS_APPLY_MIGRATIONS` | `false` for the least-privilege API runtime after an operator applies migrations |
| `PORT` | Injected or fixed positive service port |
| `RAILWAY_VOLUME_MOUNT_PATH` | Injected persistent mount; the entrypoint derives the journal path |

Do not set `FLOWOPS_CONTROL_ADDR` on the selected runtime; `PORT` produces the
required `0.0.0.0:PORT` listener and still requires explicit trusted-proxy
mode. Apply migrations with a transient privileged database credential through
`flowops-admin`, grant the API role only its required runtime permissions, and
then run the API with `FLOWOPS_APPLY_MIGRATIONS=false`. The default remains
`true` for local development and upgrades must not rely on that default. Do not
place secret-bearing URLs or tokens in process arguments.

## Owner enrollment and bootstrap

1. Deploy the private Sites version with only `FLOWOPS_SITES_PROJECT_ID` set.
2. As the sole allowed owner, read `/api/flowops/enrollment`. Verify the project
   ID and email and retain the returned 64-character site-user key.
3. Generate a new high-entropy URL-safe exchange token. Store it in a temporary
   owner-only secret file; never place it in shell history or CI logs.
4. Create a strict JSON request containing a fresh audit ID, the owner principal
   ID, organization ID and name, project ID, enrollment key, email, membership
   ID, and exchange token.
5. Run the deployed image with `/flowops/flowops-admin sites-bootstrap-owner`
   as its explicit command and feed the file over stdin. Supply
   a transient migration-capable `FLOWOPS_DATABASE_URL` from the deployment
   secret manager. The operational authority to run this command is the
   platform operator's database access; the actor ID is audited context, not a
   second proof of human identity.
6. Destroy the temporary request file after the command returns `status: ok`.
7. Store the same exchange token as the secret Sites variable
   `FLOWOPS_SITES_EXCHANGE_TOKEN`; set the API HTTPS URL as
   `FLOWOPS_CONTROL_API_URL`; keep `FLOWOPS_SITES_PROJECT_ID` unchanged.
8. Deploy a new private Sites version so the new environment revision is active.

Bootstrap is successful only when a signed-in owner sees `Live control plane`,
the exact organization name, no preview financial totals, and the chain's
fail-safe startup state. An unauthenticated request, wrong email, wrong project,
wrong user key, old token, or revoked membership must show no live tenant data.

## Rotation

Generate a replacement token and prepare a strict stdin request for
`flowops-admin sites-rotate-token`. The request must name the exact active owner
membership, actor, organization, project, and a fresh audit ID.

Update Sites with the new token immediately after the database rotation. A
brief fail-closed preview window is acceptable; accepting both old and new
tokens is not. Verify the old token fails exchange and the new token succeeds.

## Recovery and rollback

- A failed container health check never replaces the last healthy deployment.
- If Sites cannot exchange membership, leave the dashboard in preview mode and
  inspect API health, provider status, membership status, and environment
  revision. Never substitute a different tenant to make the dashboard render.
- Preserve the PostgreSQL database and reconciliation volume during rollback.
  Starting against a missing or modified journal must fail closed.
- Roll back the API image and Sites version independently only when their API
  contract remains compatible.
- Run `/flowops/flowops-admin sites-disable-provider` with the exact owner,
  organization, project, membership, and a fresh audit ID for the immediate
  access kill switch. It invalidates already issued dashboard sessions on their
  next API use. Re-enablement is intentionally a separate future recovery
  design, not a flag available in the pilot CLI.
