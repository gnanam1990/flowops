# ADR-0047: Isolated seller-egress worker runtime

## Status

Accepted.

## Context

The durable seller-egress library, PostgreSQL store, restricted transport and
leadership fence were not sufficient to run the module. Mounting them inside
the API would share credentials and lifecycle with an Internet-facing process.
A point-in-time database epoch read, one RPC timestamp, or an unsigned local
health flag would also weaken the reviewed pre-egress gates.

## Decision

Ship `/flowops/ascp-seller-worker` as a separate supervised process. It receives
only the rails PostgreSQL credential, two to five independently operated Base
RPC endpoints, the public keys for an isolated event-recovery verifier and that
verifier's HTTPS endpoint. It cannot apply migrations, bootstrap or advance
leadership, sign, broadcast, settle, release, refund, or update accounting.

The worker derives chain time only when the configured provider quorum returns
the exact same anchor number, hash and timestamp. It rejects an agreed anchor
that is stale or implausibly future; wall time can only refuse chain evidence
and cannot advance an escrow deadline. Before every effect it verifies a
short-lived Ed25519 recovery attestation, requires local, signed
checkpoint and remote heads to be equal, and compares that signed head to the
live database through `ascp_current_event_head()`. The migration-owned
`SECURITY DEFINER` function exposes only sequence and hash; PUBLIC execution is
revoked and only the rails role receives explicit execution permission.
Both RPC and recovery-verifier HTTP clients reuse the private-address,
mixed-DNS and DNS-rebinding guard used by seller egress. The cycle and lease
budget includes all three sequential requests needed for each quorum snapshot.

Each bounded cycle finalizes already stored responses before claiming new
dispatches. Any non-empty operational error terminates the worker for the
supervisor to restart with backoff. `ErrNoWork` is the only idle condition.

## Consequences

The recovery-verifier signing key and WORM/remote-head credentials remain
outside this worker. Its public-key registry supports rotation overlap. The
worker still depends on external supervisor backoff, independently operated RPC
providers, a continuously publishing recovery verifier, seller-side call-ID
idempotency and the operational retention rules in ADR-0045. A local test or
running binary is not production-provider, WORM, restore-drill or escrow-seller
evidence.
