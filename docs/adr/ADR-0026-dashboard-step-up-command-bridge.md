# ADR-0026: Dashboard step-up command bridge

Status: Accepted
Date: 2026-08-15

## Decision

The read-only Sites session remains incapable of economic writes. A live
dashboard action carries a separate fresh step-up bearer credential to a
same-origin server route. Before forwarding a command, the route independently
re-exchanges the current Sites identity and reads safe claims for the step-up
credential from the control plane. Organization, principal, and role must match
exactly; the credential must be a non-read-only human credential whose
server-side step-up expiry is still in the future.

For an approval or denial, the bridge fetches a new membership-authorized
snapshot and selects the full request digest for the requested pending record.
The client never supplies the authoritative digest. A client operation ID is
hashed into a stable idempotency key so retrying the same operation reaches the
same durable control-plane command without incorporating credential material.

The credential exists only in component memory and the one same-origin request.
It is cleared after every attempt. The browser may persist an unresolved command
ID. Before an ID is available, it may persist only the random operation ID and a
digest of non-secret action fields; this lets the exact action reuse its
idempotency key after a timeout without storing the note, reason, or token.
Recovery uses a newly exchanged read session, and the bridge returns the command
only when its organization and actor equal the current Sites membership.

Organization-wide pause is a persistent PostgreSQL organization flag, not a
loop over currently visible agents. Authorization issuance holds a shared
organization-row lock through its final append. Pause takes the exclusive row
lock, sets the flag, and records an append-only audit event before returning the
durable command and audit IDs.

## Consequences

- A valid credential from another FlowOps member cannot authorize the current
  dashboard session.
- Stale browser approval data cannot choose the request digest.
- A timeout is unresolved, never success; operators recover by command ID
  before deciding whether a new operation is appropriate.
- The hosted dashboard remains unable to write until the selected production
  identity provider implements the real short-lived step-up issuance ceremony.
- Organization unpause is intentionally absent from this slice. It requires a
  separately reviewed, step-up-protected recovery workflow.
