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
`/flowops/flowops-admin`, `/flowops/flowops-operator`, and
`/flowops/ascp-leadership`, `/flowops/ascp-seller-worker`, and
`/flowops/postgres-readiness`. `railway.json` selects that image, checks `/health`,
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
| `FLOWOPS_BASE_RPC_PROVIDERS_JSON` | Secret strict JSON array of 2–5 unique names and credential-bearing HTTPS URLs |
| `FLOWOPS_BASE_RPC_ADMISSION_JSON` | Base mainnet only: schema-v1 non-secret bindings for every provider's distinct operator, failure domain, paid tier, and production eligibility; must be unset on Sepolia |
| `FLOWOPS_BASE_CHAIN_ID` | `84532` for the capped Sepolia pilot; `8453` requires a separate mainnet gate |
| `FLOWOPS_ESCROW_CONTRACT` | Reviewed canonical lowercase CallEscrow deployment; omit the whole escrow tuple to disable escrow admission |
| `FLOWOPS_ESCROW_ASSET` | Exact canonical lowercase asset held by the configured CallEscrow deployment |
| `FLOWOPS_ESCROW_RELEASE_WINDOW_SECONDS` | Exact immutable optimistic-release window of the configured deployment |
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
| `FLOWOPS_ASCP_KEEPER_CALLBACK_KEY_B64` | Exactly 32 random bytes, base64; distinct from operator/session secrets; may register ASCP transaction identity only |
| `FLOWOPS_SIGNER_RECEIPT_KEYS_JSON` | Optional strict customer signer public-key registry; omit for the no-funds deployment |
| `FLOWOPS_ASCP_DIRECTORY_CONTRACT` | Optional canonical lowercase ServiceDirectory address. When unset, durable agent intake remains mounted but returns a fail-closed 503 |
| `FLOWOPS_ASCP_DIRECTORY_MAX_AGE` | Maximum age of the quorum observation used at intake; default `1m`, hard maximum `5m` |
| `FLOWOPS_PILOT_MAX_PER_ACTION_ATOMIC` | Required canonical positive integer; initial Base mainnet profile is `1000000` |
| `FLOWOPS_PILOT_MAX_OUTSTANDING_ATOMIC` | Required canonical positive integer; initial Base mainnet profile is `10000000` |

The outstanding ceiling is scoped to one organization/customer pair in the
control plane and one customer-owned signer journal. It is not a global
platform ceiling. Mainnet remains blocked until pilot admission restricts the
deployment to one customer or a reviewed allocation layer bounds the aggregate.

Enabling durable ASCP intake also requires a current head and matching quote
evidence already recorded in `ascp_directory_heads` and related snapshot
tables by the finalized directory observer. The API never treats a database
write timestamp or caller-provided proof as a freshness substitute. Missing,
stale, wrong-version, and unknown-leaf cases fail before operation creation.
The existing `FLOWOPS_BASE_MAX_FUTURE_CLOCK_SKEW` also bounds small positive
observer/API host clock skew; larger future observations fail closed.

The same directory switch enables durable ASCP policy/approval/authorization
orchestration. `FLOWOPS_ESCROW_CONTRACT` supplies the immutable commitment
domain and `FLOWOPS_ESCROW_RELEASE_WINDOW_SECONDS` supplies the settlement
window; startup rejects an incomplete escrow tuple. This creates only a
pre-signature reservation and execution authorization. Signer activation,
broadcast, settlement, reconciliation, and ledger readiness remain separate
gates and must not be inferred from a successful authorization response.

Do not set `FLOWOPS_CONTROL_ADDR` on the selected runtime; `PORT` produces the
required `0.0.0.0:PORT` listener and still requires explicit trusted-proxy
mode. Apply migrations with a transient privileged database credential through
`flowops-admin`, grant the API role only its required runtime permissions, and
then run the API with `FLOWOPS_APPLY_MIGRATIONS=false`. The default remains
`true` for local development and upgrades must not rely on that default. Do not
place secret-bearing URLs or tokens in process arguments. Production database
URLs must set `sslmode=verify-full`; an encrypted session without server
identity verification is not the capped-pilot posture.

## Managed PostgreSQL gate

Use two credentials. The migration credential may create and alter schema
objects and is available only to a transient operator job. The API credential
is the `flowops_runtime`-style role and must never be used to apply migrations
or run owner bootstrap commands.

1. Using the migration credential, run `/flowops/flowops-admin migrate`. This
   applies every embedded migration and records its exact checksum
   transactionally without provisioning a tenant or owner.
2. From a trusted machine with `psql`, apply the reviewed grant contract after
   substituting only the already-created role identifier:

   ```sh
   psql "$FLOWOPS_DATABASE_ADMIN_URL" \
     --set=runtime_role=flowops_runtime \
     --file=deploy/control-plane/configure-runtime-role.sql
   ```

3. Set `FLOWOPS_DATABASE_URL` to the runtime credential with
   `sslmode=verify-full`, then run `/flowops/postgres-readiness sql`. A pass
   proves the current backend negotiated TLS, the runtime role and every role
   reachable through `SET ROLE` lack escalation flags, schema DDL is denied,
   all embedded migration checksums match with no extras, and the exact table
   grant matrix has neither missing nor surplus privileges.
4. Separately collect provider-console evidence for enabled backups, PITR,
   encryption at rest, and monitoring. References must be credential-free HTTPS
   URLs with no query string. Create a short-lived JSON record whose
   `signature` is empty, sign it in a transient operator job with
   `/flowops/postgres-readiness provider-evidence-sign`, and verify it with
   `/flowops/postgres-readiness provider-evidence`. The signing private key is
   supplied only to the transient signing job; the verifier receives only the
   corresponding public key.
5. Run `make smoke-postgres-readiness`. The smoke always tests the code and
   grant contract. It performs the live SQL and provider proofs only when
   `FLOWOPS_DATABASE_URL`, `FLOWOPS_DB_EVIDENCE_PUBLIC_KEY_B64`, and
   `FLOWOPS_DB_EVIDENCE_FILE` are present; otherwise it prints `NOT RUN` and the
   deployment gate stays open.

Seller egress and other controlled effects use two additional credentials.
Apply `configure-leadership-role.sql` to the isolated leadership-controller
role; it alone may bootstrap, drain, and advance epochs. Apply
`configure-rails-role.sql` to the seller-egress worker role. The rails and API
roles can only read the active epoch, while the controller receives only the
column updates, event inserts, and event sequence access required by the
state machine:

```sh
psql "$FLOWOPS_DATABASE_ADMIN_URL" \
  --set=leadership_role=flowops_leadership \
  --file=deploy/control-plane/configure-leadership-role.sql
psql "$FLOWOPS_DATABASE_ADMIN_URL" \
  --set=rails_role=flowops_rails \
  --file=deploy/control-plane/configure-rails-role.sql
```

Do not share the leadership credential with the API, seller worker, signer,
keeper, or reconciliation services. Run the following only as transient trusted
operator jobs with `FLOWOPS_LEADERSHIP_DATABASE_URL` set through the secret
manager; it must use `sslmode=verify-full`. Bootstrap once with
`/flowops/ascp-leadership bootstrap` and strict JSON on stdin. Use `status` as a
read-only verification query. A cutover runs the shipped `drain` command and,
only after its documented checks succeed, the `advance` command:

```sh
printf '%s\n' '{"organizationId":"org-id","actor":"operator-id","evidenceDigest":"0xcccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"}' \
  | /flowops/ascp-leadership bootstrap
printf '%s\n' '{"organizationId":"org-id"}' \
  | /flowops/ascp-leadership status
printf '%s\n' '{"organizationId":"org-id","expectedEpoch":1,"actor":"operator-id","evidenceDigest":"0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}' \
  | /flowops/ascp-leadership drain
# Wait for success, then stop and verify all old-epoch hosts.
printf '%s\n' '{"organizationId":"org-id","expectedEpoch":1,"actor":"operator-id","evidenceDigest":"0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}' \
  | /flowops/ascp-leadership advance
```

The drain command does not return until prior fenced effects have exited. Never
advance merely because a client timeout occurred; first query
`/flowops/ascp-leadership status`, reconcile the durable state/event evidence,
and verify old-host shutdown. Status includes durable `inFlightEffectIds` when
completion persistence was lost. Those IDs are a fail-closed recovery queue,
not permission to clear work on a timer. First reconcile the effect-specific
idempotency key and durable seller/payment outcome. Only after independently
proving the old effect host dead, preserve that proof as a lower-case SHA-256
digest and run:

```sh
printf '%s\n' '{"organizationId":"org-id","expectedEpoch":1,"effectId":"0xdddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd","actor":"recovery-operator","evidenceDigest":"0xeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"}' \
  | /flowops/ascp-leadership abandon-effect
```

Re-run status and require an empty in-flight list before advance. Abandonment is
allowed only in `DRAINING` and is permanently attributable; the rails worker
cannot perform it. Seller callback database writes use the rails credential,
never the isolated controller credential. Database URLs and credentials
belong only in the environment/secret mount, never JSON, arguments, logs, or
tickets.

Run `/flowops/ascp-seller-worker` as a separate supervised process, never as an
API subcommand. It requires:

| Variable | Requirement |
|---|---|
| `FLOWOPS_RAILS_DATABASE_URL` | Rails-role PostgreSQL URL with exactly one `sslmode=verify-full` and a database path |
| `FLOWOPS_SELLER_WORKER_ID` | Unique stable replica identifier |
| `FLOWOPS_SELLER_CHAIN_ID` | `84532` or separately admitted `8453` |
| `FLOWOPS_SELLER_RPC_PROVIDERS_JSON` | Strict array of 2–5 unique names and distinct-host HTTPS endpoints; keep credential-bearing URLs in the secret manager |
| `FLOWOPS_SELLER_RPC_QUORUM` | Integer from 2 through the configured provider count |
| `FLOWOPS_SELLER_RPC_REQUEST_TIMEOUT` | Optional per-request timeout, default `5s`, maximum `10s`; a snapshot budgets three sequential requests per provider |
| `FLOWOPS_SELLER_MAX_CHAIN_LAG` | Optional fail-closed maximum age of the agreed Base anchor, default `30s`, maximum `10m`; wall time can reject stale/future chain evidence but never advance an escrow deadline |
| `FLOWOPS_SELLER_INTEGRITY_URL` | HTTPS port-443 endpoint for the isolated recovery verifier's latest signed proof; redirects are refused |
| `FLOWOPS_SELLER_INTEGRITY_KEYS_JSON` | Map of recovery-verifier key ID to canonical base64 Ed25519 public key; never a private key |
| `FLOWOPS_SELLER_INTEGRITY_TIMEOUT` | Optional positive duration, default `5s`, maximum `30s` |
| `FLOWOPS_SELLER_INTEGRITY_MAX_TTL` | Optional accepted signed-proof lifetime, default `2m`, maximum `5m` |
| `FLOWOPS_SELLER_INTERVAL` | Optional cycle interval, default `50s`, maximum `1m` |
| `FLOWOPS_SELLER_CYCLE_TIMEOUT` | Optional whole-cycle timeout below the interval, default `45s` |
| `FLOWOPS_SELLER_BATCH_SIZE` | Optional finalization and dispatch limit per cycle, default `20`, maximum `100` |
| `FLOWOPS_SELLER_LEASE_DURATION` | Optional durable lease, default `55s`, maximum `1m` |
| `FLOWOPS_SELLER_HTTP_TIMEOUT` | Optional seller request timeout, default `10s`; plus the 5s persistence margin must fit the lease |
| `FLOWOPS_SELLER_RETRY_DELAY` | Optional exact-call retry delay, default `15s`, maximum `1h` |
| `FLOWOPS_SELLER_MAX_OBSERVATION_AGE` | Optional RPC observation age, default `30s`, maximum `1m` |

The recovery endpoint signs the `ASCP_EVENT_RECOVERY_ATTESTATION_V1` payload
defined by `ascprails.IntegrityAttestation`. It must publish `VERIFIED` only
after `ascpevents.VerifyRecovery` has verified the complete local chain, signed
checkpoint, exact WORM bytes and monotonic remote head. Local and remote heads
and checkpoint sequence must be identical; bounded uncheckpointed tails remain
fail-closed for seller egress. Apply migration 0019 before granting the rails
role. A process start proves configuration and connectivity only; pilot
admission still requires live RPC-independence, recovery-verifier, WORM,
remote-head, seller-idempotency and restore-drill evidence.

SQL cannot prove provider backup retention, PITR, encryption at rest, or
monitoring. A signed operator record is tamper evidence and an accountable
snapshot, not an independent provider attestation. Before pilot admission,
also execute one restore drill into an isolated database and one runtime-
credential rotation drill; neither is inferred from the readiness report.

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
   For mainnet, separately review the URL-free admission JSON. Run
   `make smoke-rpc-admission` during packaging. Passing proves the configuration
   shape, not the provider contract or infrastructure claim; retain evidence
   for both paid plans and their distinct operational failure domains.
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

Inspect one organization's durable recovery projection from the trusted
operator environment:

```sh
printf '%s\n' '{"organizationId":"org_acme"}' \
  | /flowops/flowops-operator reconciliation-status
```

If an unresolved direct transaction must be contained while its external
outcome is still unproved, quarantine it explicitly:

```sh
printf '%s\n' '{"organizationId":"org_acme","executionId":"exec_123","operator":"operator_alice","disposition":"REPLACED_UNPROVEN","reason":"receipt absent and sender nonce outcome requires independent investigation"}' \
  | /flowops/flowops-operator execution-quarantine
```

This is not a declaration that the transaction was replaced or dropped. It
does not create a retry, settlement, refund, or replacement transaction. Keep
the execution quarantined until separately collected nonce and transaction
evidence supports a reviewed resolution.

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
