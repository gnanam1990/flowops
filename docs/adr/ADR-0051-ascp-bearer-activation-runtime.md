# ADR-0051: ASCP bearer activation runtime

- Status: accepted
- Date: 2026-08-22

## Context

The two-phase signer kernel persisted requests and safe state transitions but
had no separately runnable recovery process. A crash after signer preparation,
primary mirroring, or signer acknowledgment therefore required an unspecified
operator. A request that expired before activation could also leave its budget
reservation in `RESERVED` indefinitely. Giving the control-plane API all
activation mutation authority would combine request admission, signer
coordination, registry publication, and budget release in one credential.

## Decision

Run `cmd/ascp-bearer-worker` as a separately supervised, non-listening process
pinned to one worker ID, signer key and epoch, and keeper ID. PostgreSQL claims
use `FOR UPDATE SKIP LOCKED`, a random 256-bit fenced lease token, an expiry,
and a retry schedule. Every repository transition revalidates the exact live
lease inside its transaction. Each coordinator call crosses at most one
external or durable boundary, so a lost response is retried against idempotent
signer and WORM operations.

The worker connects to an isolated signer and primary WORM writer over two
strict, distinct Unix sockets. The protocol rejects path aliases, unsafe
ownership or permissions, redirects, proxies, oversized bodies, incorrect
media types, unknown or duplicate fields, excessive nesting, wrong health
identity, invalid opaque handles, and non-exact WORM outcomes. The worker has
no public listener and logs only identifiers and aggregate state counters.

Pre-activation expiry requires a signer-produced, domain-separated proof bound
to request ID, action ID, input hash, optional prepared handle, status, and
proof time. Only then may one serializable transaction release a `RESERVED`
budget, record `EXPIRED_UNACTIVATED`, cancel pending signer work, and clear the
lease. An `AUTHORIZATION_LIVE` reservation is outside this path.

The control-plane role loses signer-request and signer-outbox transition
updates. A dedicated bearer LOGIN role receives only the reads, inserts, and
column updates required by this state machine. Worker startup audits effective
role flags, memberships, ownership, schema and temporary authority, every
public table privilege, every updateable column, routine execution, and
sequence use; missing or surplus authority stops startup.

## Consequences

- Multiple replicas may safely contend for one signer/keeper shard without
  duplicate ownership; stale workers cannot commit a transition after fencing.
- Boundary outages become durable bounded retries. Database or invariant
  failures remain visible and stop the worker for supervisor recovery.
- Expired unactivated work no longer retains budget indefinitely, while active
  authorizations cannot be TTL-released by this worker.
- Production still requires independently implemented and reviewed signer and
  WORM sidecars, HSM/KMS ceremonies, managed PostgreSQL evidence, monitoring,
  recovery drills, and the public request-admission route. Repository tests do
  not prove those services are deployed.
