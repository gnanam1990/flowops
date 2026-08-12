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
  Sites session key, Sites exchange token, Base RPC provider URLs, and 32-byte
  operator-control key; and
- the private owner-only Sites project recorded in `.openai/hosting.json`.

The checked-in `Dockerfile` builds `/flowops/control-plane-api`,
`/flowops/flowops-admin`, and `/flowops/flowops-operator`. `railway.json` selects that image, checks `/health`,
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
| `FLOWOPS_BASE_RPC_PROVIDERS_JSON` | Secret strict JSON array of 2–5 unique names and HTTPS hosts |
| `FLOWOPS_BASE_CHAIN_ID` | `84532` for the capped Sepolia pilot; `8453` requires a separate mainnet gate |
| `FLOWOPS_BASE_OBSERVER_INTERVAL` | Poll interval; must exceed the per-poll timeout |
| `FLOWOPS_BASE_OBSERVER_TIMEOUT` | Overall timeout for one concurrent provider snapshot |
| `FLOWOPS_BASE_RECONCILIATION_INTERVAL` | Receipt and finality worker interval; must exceed its query timeout |
| `FLOWOPS_BASE_RECONCILIATION_TIMEOUT` | Per-execution provider-quorum query timeout |
| `FLOWOPS_BASE_OBSERVER_QUORUM` | Required independent responses, 2–5 and no greater than provider count |
| `FLOWOPS_BASE_HALT_CONFIRMATIONS` | Consecutive unhealthy observations before `HALTED` |
| `FLOWOPS_BASE_RECOVERY_OBSERVATIONS` | Consecutive healthy observations before manual resume is permitted |
| `FLOWOPS_BASE_MIN_CONFIRMATIONS` | Sealed-block receipt confirmation floor |
| `FLOWOPS_BASE_REORG_LOOKBACK` | Canonical reorg evidence depth |
| `FLOWOPS_BASE_MAX_HEAD_SKEW` | Maximum provider sealed-head difference |
| `FLOWOPS_BASE_STALL_THRESHOLD` | Maximum age of a provider's latest sealed block |
| `FLOWOPS_BASE_OBSERVATION_MAX_AGE` | Maximum trusted observer-heartbeat age |
| `FLOWOPS_BASE_MAX_FUTURE_CLOCK_SKEW` | Maximum tolerated future timestamp skew |
| `FLOWOPS_OPERATOR_CONTROL_KEY_B64` | Exactly 32 random bytes, base64; global halt/resume authority |
| `FLOWOPS_SIGNER_RECEIPT_KEYS_JSON` | Optional strict customer signer public-key registry; omit for the no-funds deployment |

Do not set `FLOWOPS_CONTROL_ADDR` on the selected runtime; `PORT` produces the
required `0.0.0.0:PORT` listener and still requires explicit trusted-proxy
mode. Apply migrations with a transient privileged database credential through
`flowops-admin`, grant the API role only its required runtime permissions, and
then run the API with `FLOWOPS_APPLY_MIGRATIONS=false`. The default remains
`true` for local development and upgrades must not rely on that default. Do not
place secret-bearing URLs or tokens in process arguments.

Signer receipt keys are public material but remain tenant-scoped security
configuration. Each JSON item must contain exactly `organizationId`,
`customerId`, `keyId`, and a base64-encoded 32-byte Ed25519 `publicKeyB64`.
Never place a customer wallet key, attestation private key, seed, raw
transaction, or keystore in this variable. Leaving it unset keeps
`POST /v1/signer/broadcasts` fail-closed with no effect on the no-funds health
or observer runtime.

## Base observer activation and manual release

1. Store the observer JSON and operator key in the deployment secret manager.
   Never put a credential-bearing RPC URL in a command, log, issue, or commit.
2. Deploy one replica. `/health` must report the configured required observer
   count and a recent observation time. `HTTP 200` alone is not a pass.
3. Wait for `RECOVERING`, the expected responding count, a progressing trusted
   checkpoint, and `readyForManualResume: true`.
4. From a trusted operator environment, set `FLOWOPS_CONTROL_API_URL` and load
   `FLOWOPS_OPERATOR_CONTROL_KEY_B64` from the secret manager. Then run:

   ```sh
   printf '%s\n' '{"operator":"operator_alice"}' \
     | /flowops/flowops-operator chain-resume
   ```

5. Verify `/health` and the signed-in private dashboard show `HEALTHY`, the
   exact responding/quorum count, and a fresh trusted block. A retry with the
   same operator is safe and returns the same healthy state.

For an immediate global stop:

```sh
printf '%s\n' '{"operator":"operator_alice","reason":"provider incident under investigation"}' \
  | /flowops/flowops-operator chain-halt
```

The actor string is journaled audit context. Possession of the operator key is
the operational authority. Rotate the key after suspected exposure; tenant and
Sites credentials are deliberately not accepted by these endpoints.

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
- A provider outage, wrong chain, stale head, or rate limit is a chain-safety
  incident, not an API availability failure. Do not bypass the observer gate.
- Roll back the API image and Sites version independently only when their API
  contract remains compatible.
- Run `/flowops/flowops-admin sites-disable-provider` with the exact owner,
  organization, project, membership, and a fresh audit ID for the immediate
  access kill switch. It invalidates already issued dashboard sessions on their
  next API use. Re-enablement is intentionally a separate future recovery
  design, not a flag available in the pilot CLI.
