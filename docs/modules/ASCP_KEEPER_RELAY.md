# ASCP keeper relay module

## Why

Relay self-enforcing ASCP chain calls without giving the control plane a wallet,
letting the keeper make policy decisions, or letting RPC uncertainty create a
duplicate economic effect.

## Entry

`Store.Enqueue` accepts a durable relay job after signer activation. Callers
provide an opaque signer handle, never signature bytes. `Service.RunOnce`
claims one lease and advances exactly one broadcast boundary. `claimExpired`
is a permissionless job with no signer handle. `ExpiryScanner` creates it only
from fresh multi-provider confirmed chain time and persists that evidence
digest. The first proof is frozen; later fresh observations of the same
economic claim are idempotent and cannot rewrite it. The PostgreSQL claim query
prioritizes the job.

## Inputs

Every job binds job and operation IDs, organization, action, Base chain, keeper
identity, keeper EOA, target, zero value, canonical payload bytes and the exact
module-facing Keccak-256 hash, eligibility time, and exact action data.
Signature-bearing actions additionally bind activation handle, authorization
digest, expected signer, validity window of at most ten minutes, and leadership
epoch.

## Internal behavior

1. Claim a short fenced lease with `FOR UPDATE SKIP LOCKED`.
2. Recheck time, keeper identity and current leadership epoch.
3. Release the activated artifact only over the keeper-bound signer channel.
4. Read quorum pending nonce/fees and preflight-assemble and independently
   decode/verify chain, sender, target, value, action payload, authorization
   digest and signer before any nonce is reserved.
5. Reserve `max(quorum nonce, durable next)` serializably. A crash-retry returns
   the same reservation; if it differs from preflight, reassemble and reverify.
6. Sign, decode the signed raw transaction again, then seal it and persist
   `PREPARED`; persist
   `BROADCASTING`, then call the RPC.
7. Rebroadcast identical bytes after a crash. Quarantine unknown outcomes.
8. Replace only with the same nonce, strictly higher max fee, independent
   replacement-safety proof and a maximum of three fee bumps.

The service enforces deployment-level maximum gas, fee-cap and priority-fee
caps independently of the injected estimator before any wallet call.

## Outputs and interfaces

Durable jobs expose lifecycle, lease, attempt count and errors. Attempts expose
transaction hash, nonce, fee, encrypted raw bytes key ID and the mandatory
`gasPayer`. Interfaces isolate signer release, payload assembly, independent
binding verification, keeper wallet, encryption, RPC, fee policy, nonce quorum,
replacement quorum and leadership source. `Service.ObserveOnce` consumes only
the independent settlement outcome adapter and persists its evidence digest;
settlement continues to own receipt, finality and accounting truth.

## Failure and recovery

- Invalid epoch, expiry, payload substitution or wallet mutation: fail before
  signing/broadcast and never manufacture a chain outcome.
- Crash after nonce reservation: reuse the same reserved nonce.
- Crash after sealing: rebroadcast the exact sealed bytes.
- Timeout or unknown RPC error: `AMBIGUOUS`, excluded from relay work; the
  observation worker may recover only the exact transaction hash.
- Deterministic underpricing: `TIMED_OUT`, eligible for proved same-nonce bump.
- Replacement quorum disagreement or advanced nonce: fail closed.
- Exhausted fee bumps or expired bearer: `DEAD_LETTER`; IncidentResponder owns
  resume. A public stuck-escrow surface may expose explorer-ready
  `claimExpired` calldata without a bearer.
- Reverted/reorged/finalized money state is applied only by independent
  settlement and reconciliation evidence.

## Operations

Apply migrations `0013` and `0022`, then deploy separately from the API with
`configure-keeper-role.sql`. The keeper EOA
must be dedicated, capped and independently funded. Alert on gas floor, lease
stalls, ambiguity/dead letters, RPC disagreement, fee-bump exhaustion and
`claimExpired` eligibility-to-broadcast lag over ten minutes. Correlation IDs
are the durable job and operation IDs. Reconcile every keeper EOA outflow and
every attempt's actual receipt gas against `KeeperRelayerETH`/`NetworkFeeETH`.

## Durable runtime

`cmd/ascp-keeper` is the separately supervised process. It pins one Base chain
and wires the PostgreSQL
store and read-only leadership gate to seven distinct Unix-domain boundary
sockets: activated artifact, assembler, independent verifier, wallet/HSM,
ciphertext sealer/KMS, write-only broadcaster, and independently verified
read-only chain gateway. Startup rejects path duplicates and filesystem aliases
whose device/inode identity would collapse two boundaries onto one socket. It
also rejects a symlink or world-writable socket, a group- or world-writable or
unowned socket directory, a socket not owned by the runtime user or root, or a
health response whose exact boundary identity does not match. No TCP listener,
private key, raw-transaction environment variable, or credential-bearing RPC
URL exists in this process.

Every sidecar implements `ASCP_KEEPER_BOUNDARY_V1`, returns strict JSON with no
unknown fields, and bounds each body to 2 MiB. `GET /healthz` returns exactly
`protocol`, `boundary`, and `status`. The operation routes are:

- artifact: `POST /v1/release`;
- assembler: `POST /v1/assemble`;
- verifier: `POST /v1/verify`;
- wallet: `POST /v1/sign`;
- sealer: `POST /v1/seal` and `POST /v1/open`;
- broadcaster: `POST /v1/broadcast`; and
- chain: `POST /v1/fees/initial`, `/v1/fees/bump`,
  `/v1/nonce`, `/v1/replacement`, `/v1/outcome`, and `/v1/expiries`.

The artifact sidecar additionally requires the canonical base64 32-byte Bearer
capability loaded from `FLOWOPS_KEEPER_SIGNER_TOKEN_FILE` for health and
release. The same value is mounted into the isolated signer through
`FLOWOPS_SIGNER_KEEPER_TOKEN_FILE`; it is never the artifact encryption key.

The chain sidecar is the independently operated read-only quorum/evidence
adapter and cannot broadcast. Its
`outcome`, `replacement`, and `expiries` responses are still revalidated by the
keeper kernel before durable state changes. A non-success broadcast response
is deterministic only when the broadcaster sidecar emits `REJECTED` or
`UNDERPRICED`; every other code and every transport error is ambiguous. The
runtime reserves 10% of each cycle for already-broadcast outcomes, 10% for
expiry proofs, and 80% for relay work, so a slow successful phase cannot starve
the phases behind it. A phase-local deadline safely yields to the next phase;
an uncontained boundary or database failure stops the supervised process.

The runtime is executable integration infrastructure, not a claim that the
external signer, HSM, KMS or Base-provider set has been deployed. Production
admission still requires separately reviewed sidecars, distinct socket
ownership, live funded EOA drills, provider-independence evidence, alerting,
backup/restore evidence, and key ceremonies.

## Acceptance criteria

- Exact enqueue replay succeeds and any binding substitution fails.
- Leadership is checked before bearer release; only the bound keeper receives
  the artifact; `claimExpired` never contacts the signer.
- Concurrent claims are exclusive and nonce reservation survives restart.
- Claims and observation leases bind keeper ID, configured gas payer, and exact
  Base chain, so another EOA or chain cannot poison a worker into a restart
  loop.
- Expiry scanning rejects local-wall-clock-only, stale or single-provider proof
  and emits the exact `claimExpired(bytes32)` selector and call ID.
- Transaction substitution fails before the wallet.
- Signed bytes are sealed before broadcast; restart rebroadcasts identical
  bytes without signer, wallet or nonce calls.
- Unknown RPC outcomes are quarantined and never blindly retried.
- Underpriced attempts use proved same-nonce fee bumps; unsafe replacement and
  the fourth bump fail closed.
- PostgreSQL enforces job/attempt gas-payer and nonce consistency.
- Focused race/vet, migration readiness, repository checks and adversarial PR
  review pass before merge.
