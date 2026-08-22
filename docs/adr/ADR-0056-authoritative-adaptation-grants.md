# ADR-0056: Authoritative single-use adaptation grants

Status: Accepted
Date: 2026-08-22

## Decision

Issue adaptation grants only from a tenant-scoped, persisted policy denial.
Derive grant scope from the immutable operation, policy version, current
budget projection, and current directory evidence. Do not expose a general
grant-mint endpoint and do not accept signed grant bytes from an agent.

Use a dedicated platform key behind an idempotent local HSM boundary. It is not
a customer wallet key, spend-authorizer key, Safe owner, verifier key, or
directory publisher. The control plane stores only the signed public artifact
and configured signer address.

Persist at most one grant for an original intent. Intake receives its ID,
resolves it under authenticated organization and agent scope, verifies the
artifact independently, and consumes it in the same serializable transaction
that creates the adapted intent. Exact retries replay the committed result.
Adaptive policy rejection escalates once; prohibited categories never become
approvable through this mechanism.

## Consequences

- A caller cannot choose a larger amount, different category, foreign tenant,
  or arbitrary seller set.
- Loss of the HSM response is recoverable without minting another logical
  grant or repeating the HSM operation after the grant is durable.
- The policy decision and external signature are not falsely described as one
  network-spanning atomic transaction. Eligible evaluation fails retriably
  until signing succeeds; unsigned grants are never exposed.
- Deployment now needs a separately reviewed adaptation HSM socket, key ID,
  epoch, and recovered signer address. Local tests do not prove that external
  custody or managed-database operations are production ready.
