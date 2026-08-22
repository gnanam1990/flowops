# ASCP Ring 6 runtime

`cmd/ascp-ring6-runtime` implements the independent verification/HSM
orchestration boundary consumed by `cmd/ascp-signer-runtime`. It owns no signing
private key and opens no TCP listener.

## Exact lifecycle

For `POST /v1/verify-and-sign`, the runtime:

1. requires protocol `ASCP_SIGNER_DEPENDENCY_V1`, strict JSON, and a complete
   activation input;
2. checks pinned key ID, epoch, keeper, validity window, payload/evidence hashes,
   and all activation identities;
3. recomputes the domain-separated activation input hash;
4. binds `(operationId, actionId)` to that input hash, digest, key, epoch, and a
   deterministic HSM idempotency key in the journal;
5. asks the independent verifier to verify the same input and echo its hash;
6. durably records `HSM_REQUESTED`, then asks the HSM component to sign with
   that idempotency key; and
7. verifies digest, operation handle, signature shape, and recovered signer
   before durably recording `SIGNED` and returning the signature.

Exact retries use the same HSM idempotency key. A different input for an
already-bound action returns `BINDING_MISMATCH`. Only a canonical verifier
`422` becomes durable `REFUSED` and maps to `SIGNER_REFUSED`.
After `HSM_REQUESTED`, an ambiguous call may have created a signature, so no
later verifier refusal is classified as permanent. The action stays recoverable
through the same HSM idempotency key. A refusal is exposed to the caller only
after its `REFUSED` journal transition is fsynced successfully.

## Trust boundaries

The runtime listener is a new owner-controlled `0600` Unix socket. Verifier and
HSM dependencies are pre-existing, different `0600` Unix sockets in owner-only
directories. Their paths and device/inode identities must differ. Each exposes
exact health JSON using `ASCP_RING6_COMPONENT_V1` and boundary `verifier` or
`hsm`.

- `POST /v1/verify` receives `{protocol,input,inputHash}` and returns
  `{verified:true,inputHash}`. Exact HTTP `422` `{code}` is the only permanent
  refusal class.
- `POST /v1/sign` receives only protocol, HSM idempotency key, key ID, key epoch,
  and digest. It returns `{operationHandle,digest,signature}`. Raw keys never
  cross the boundary.

Bodies are capped at 2 MiB. Clients disable redirects, compression, and proxies
and require exact JSON. Socket ownership and permissions are rechecked before
each component call.

## Durable journal and recovery

The private journal is opened without following symlinks, locked for the
process lifetime, appended with `O_APPEND`, fsynced after every transition, and
directory-fsynced on creation. Each line carries a monotonic sequence, previous
hash, full binding, and event hash. Startup rejects truncation, tampering,
invalid transitions, duplicate terminalization, and unknown fields. A partial
write or fsync error faults the journal and later writes fail closed.

No signature bytes are stored. `SIGNED` stores only the HSM operation handle,
so the HSM's idempotent operation can recover the signature after a crash
between signing and journal terminalization.

## Runtime configuration

| Variable | Requirement |
|---|---|
| `FLOWOPS_RING6_KEY_ID` | HSM key identifier pinned to the signer shard |
| `FLOWOPS_RING6_KEY_EPOCH` | Positive canonical key epoch |
| `FLOWOPS_RING6_KEEPER_ID` | Keeper identity pinned into activation inputs |
| `FLOWOPS_RING6_SIGNER_ADDRESS` | Nonzero signer recovered from each HSM signature |
| `FLOWOPS_RING6_JOURNAL_PATH` | Absolute journal path in an owner-controlled directory |
| `FLOWOPS_RING6_RUNTIME_SOCKET` | New absolute listener socket path |
| `FLOWOPS_RING6_VERIFIER_SOCKET` | Existing private verifier component socket |
| `FLOWOPS_RING6_HSM_SOCKET` | Existing, distinct private HSM component socket |
| `FLOWOPS_RING6_DEPENDENCY_TIMEOUT` | Optional `1s` through `10s`; default `3s` |

Every path must be clean, absolute, non-root, and distinct. Startup requires
both component sockets and exact health identities before journal replay.

## Verification and production gates

```sh
go test -race ./internal/ascpring6 ./internal/ascpbearer ./internal/ascpsignerruntime ./cmd/ascp-ring6-runtime
go vet ./internal/ascpring6 ./internal/ascpbearer ./internal/ascpsignerruntime ./cmd/ascp-ring6-runtime
```

Tests cover strict protocol substitution, durable restart replay, concurrent
retry convergence, changed evidence, permanent versus transient refusal, wrong
signer, component identity/inode separation, duplicate responses, listener
permissions, existing-path preservation, and the full verifier/HSM wire path.

Production acceptance additionally requires reviewed deployments and evidence
for independent Base RPC verification, hardware key custody, provider-side
idempotency retention, high availability, backup/restore, monitoring, alerting,
and key rotation/epoch ceremonies.
