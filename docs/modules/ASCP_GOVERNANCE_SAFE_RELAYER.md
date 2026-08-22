# ASCP governance Safe relayer

## Why

Turn one immutable `ascp.governance.execute` command into a customer Safe
transaction without giving the control plane an owner key, allowing a relayer
to change approved calldata, or retrying a stale Safe action after its nonce or
chain preconditions changed.

## Authority boundaries

The control-plane approval is not a Safe signature. The customer-controlled
owner ceremony signs the exact Safe EIP-712 transaction after an independent
chain quorum returns the current Safe nonce, sorted owner set, threshold,
confirmed chain timestamp, and action-precondition payload hash. The relayer
requires exactly the PRD's three-owner, threshold-two governance Safe and
accepts only canonical EOA-owner EIP-712 signatures in strictly increasing
owner order. A 1-of-1, 2-of-2, or other owner topology fails closed. Safe
contract-owner signatures, approved-hash signatures, and
`eth_sign` signatures fail closed pending a separately reviewed proof adapter.

The constructed Safe transaction is fixed to Base mainnet or Base Sepolia, the
organization Safe, the server-derived governance target and exact calldata,
value zero, `CALL`, zero Safe refund fields, and the independently observed
Safe nonce. Threshold signatures are embedded in exact
`Safe.execTransaction` calldata, ABI-decoded, signature-verified, and
re-encoded before every relay.

The executable artifact is stored only behind an authenticated vault handle.
The broadcaster receives the exact calldata and a sanitized binding; it never
receives the vault handle, approval identity, or unrelated job fields.
The chain outcome observer receives only the workflow, Safe, transaction-hash,
payload, and attempt binding; it never receives the vault handle,
authorization key, signature hash, lease, or full persisted relay job.

## Durable lifecycle

Migration `0030` adds one relay job per immutable governance outbox row,
authorization idempotency scoped by organization and key, fenced
relay/observation leases, persisted Safe and outer-transaction bindings, and
append-only Safe retry proofs.

```text
AWAITING_SIGNATURES -> READY -> BROADCASTING -> SUBMITTED
SUBMITTED <-> PENDING
SUBMITTED/PENDING -> RETRYABLE_EXACT -> BROADCASTING
READY/RETRYABLE/SUBMITTED/PENDING -> REAPPROVAL_REQUIRED
SUBMITTED/PENDING/RETRYABLE_EXACT -> FINALIZED_OBSERVED
```

`FINALIZED_OBSERVED` is relayer bookkeeping only. The independent governance
receipt observer remains the sole boundary that finalizes the product
workflow.

The outer transaction is prepared and durably recorded before broadcast. A
crash in `BROADCASTING` first reconciles the exact outer hash. A proven pending
or terminal transaction is recorded without another send; a proven absent or
non-canonical transaction revalidates current Safe bindings before the same
prepared outer artifact is broadcast. A later
dropped or reorged outer transaction may be replaced only when independent
evidence proves that the previous outer transaction is non-canonical, the Safe
nonce and action-precondition payload hash remain current, Safe transaction
and exec-calldata hashes are unchanged, and at least two fresh observers
agree. The worker refreshes this evidence immediately before preparing every
replacement; pending or finalized truth suppresses the replacement. One owner-authorized Safe transaction is capped at ten submitted outer
attempts across crashes and replacements. Once that durable count reaches ten,
the worker requires a fresh approval and owner-signing ceremony before any
additional broadcast; an eleventh attempt is never prepared.

The proof is inserted before the workflow update. The database trigger joins
it to the relay job's persisted Safe binding; direct
`REORGED`/`TIMED_OUT -> SUBMITTED` SQL without the exact proof is rejected. A
changed nonce, owner set/threshold, or precondition requires a fresh workflow
and owner signatures.

The independent receipt observer also scans side-state workflows that have a
recorded submission. A finalized approved action from the current attempt or
any append-only proven-retry attempt atomically reconstructs submission and
confirmation before finalization. Side states with no recorded attempt remain
excluded, and a transaction outside the recorded attempt history is rejected.
If an earlier proven attempt wins while a replacement is submitted or
confirmed, the finalized receipt atomically replaces the workflow's primary
transaction hash with that canonical winner; the losing replacement is never
reported as the completed transaction.

## Runtime

`cmd/ascp-governance-relayer` is a separately supervised, no-listener process.
It uses the role from
`deploy/control-plane/configure-governance-relayer-role.sql` and four distinct
Unix sockets:

- `directory`: authoritative organization-to-Safe binding;
- `chain`: read-only quorum Safe snapshot and outcome evidence;
- `vault`: authenticated seal/open of exact signed Safe calldata; and
- `broadcast`: outer gas-payer transaction preparation and broadcast.

Every sidecar identifies as `ASCP_GOVERNANCE_RELAY_BOUNDARY_V1`, returns strict
bounded JSON, and runs on a non-symlink socket with distinct filesystem
identity. The vault additionally requires the canonical 32-byte capability
from `FLOWOPS_GOVERNANCE_RELAY_VAULT_TOKEN_FILE`.

Daemon mode consumes, observes, and relays durable work. Owner signing is an
explicit two-step ceremony. `ascp-governance-relayer inspect` emits the exact
Safe EIP-712 transaction, Safe transaction hash, sorted owners, threshold,
owner-set hash, payload binding, and quorum evidence that owners must inspect
before signing. Inspect mode touches only the database, directory, and
read-only chain boundaries; it does not require a daemon worker, load the vault
capability, or connect to the broadcast boundary.
`ascp-governance-relayer authorize` connects only to the database, directory,
read-only chain, and authenticated vault boundaries, then reads one private,
runtime-user-owned, non-symlink file containing a canonical base64 bundle of
threshold signatures. It prints only workflow ID, state, and Safe transaction
hash; signature bytes and vault handle are not returned.

Startup verifies the dedicated PostgreSQL role's effective privileges,
including PUBLIC and membership grants. Missing authority, table-wide update,
immutable-column update, sequence use, object ownership, schema creation, and
temporary-table authority all fail closed. The only executable database
routine is the immutable observer-array validator used by the retry proof
constraint; any additional routine authority fails startup.
The relay-job insert trigger also rebinds every consumed command to the exact
approved workflow and immutable governance outbox payload.

## Failure and recovery

- Stale/single-provider snapshots, duplicate or unknown JSON, wrong owners,
  high-s or non-EIP-712 signatures, target/value/operation/refund mutation, and
  ABI trailing bytes fail before broadcast.
- A crash after vault seal but before database authorization can leave an
  unreachable vault artifact; it grants no database authority and is subject
  to vault retention cleanup.
- A crash after outer preparation reconciles the exact outer hash before any
  byte-identical resend.
- Pending transactions wait. Dropped/reorged transactions use the proof-bound
  retry, up to ten submitted outer attempts. Retry exhaustion, mined revert, or
  binding drift requires reapproval.
- A write-RPC response never finalizes the workflow.

The daemon emits structured workflow ID, relay state, attempt count, and
idempotent-consume status for every durable cycle transition. It never logs
signatures, executable calldata, vault capabilities, artifact handles, or
database credentials.

## Verification and production gates

Focused tests cover exact displayed signing requests, Safe hashing/signatures/calldata, substitution,
stale/quorum failure, idempotent owner authorization, exclusive leases,
artifact mutation, dropped retry, and Safe-nonce reapproval. The real
PostgreSQL test applies all migrations in a disposable schema and proves the
retry trigger, immutable evidence, and durable attempt cap when
`FLOWOPS_TEST_DATABASE_URL` is set.

Local code does not prove a deployed 2-of-3 Safe, funded outer gas payer,
independent provider set, production vault/KMS, sidecar ownership, alerting,
backup/restore, or owner ceremony. Those remain external production gates.
