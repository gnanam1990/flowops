# ADR-0048: Isolated event-recovery verifier runtime

Status: accepted for the local reference implementation

## Context

The event-chain package could verify a database, checkpoint signature, immutable
object and monotonic remote head, while the seller worker could consume a signed
integrity attestation. No process joined those boundaries. An operator could not
run the documented seller-egress gate without an unreviewed proof producer.

## Decision

Ship `/flowops/ascp-event-recovery` as a separate read-only process. It uses a
dedicated PostgreSQL role that can select only `ascp_events` and
`ascp_event_checkpoints`, strict HTTPS readers for the immutable checkpoint and
remote head, retained writer/checkpoint verification keys, and a separate
Ed25519 attestation key loaded from an owner-only file.

Startup and every uncached request replay the complete event chain and require
the local head, signed checkpoint and remote head to be identical. Only that
state produces a short-lived `VERIFIED` attestation. A cache of at most five
seconds coalesces concurrent verification; this does not authorize stale seller
egress because the seller worker independently compares the proof to the live
database head. Verification failures return a generic 503 and remain visible in
private logs. Non-caller-cancellation failures are cached for the same bounded
window so an upstream outage cannot turn queued requests into serial full-chain
replays.

The runtime follows immutable-object references only in the exact checkpoint
namespace. Both external readers require HTTPS on port 443, refuse redirects,
proxies, private/reserved destinations and DNS rebinding, bound response sizes,
and accept only exact media and JSON contracts.

## Consequences

- The seller worker now has a runnable proof producer instead of a configuration
  placeholder.
- The recovery process cannot append events, publish checkpoints, move funds,
  sign escrow verdicts, or perform seller egress.
- TLS termination and access policy remain deployment concerns; the signed
  proof is the authorization material, not network location.
- Production still requires independently operated WORM and monotonic-head
  services, KMS/HSM-backed attestation signing, restore drills, monitoring and
  measured verification latency. The file key adapter is a hardened reference,
  not an HSM claim.
