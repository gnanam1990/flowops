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

Deploy separately from the API with `configure-keeper-role.sql`. The keeper EOA
must be dedicated, capped and independently funded. Alert on gas floor, lease
stalls, ambiguity/dead letters, RPC disagreement, fee-bump exhaustion and
`claimExpired` eligibility-to-broadcast lag over ten minutes. Correlation IDs
are the durable job and operation IDs. Reconcile every keeper EOA outflow and
every attempt's actual receipt gas against `KeeperRelayerETH`/`NetworkFeeETH`.

## Acceptance criteria

- Exact enqueue replay succeeds and any binding substitution fails.
- Leadership is checked before bearer release; only the bound keeper receives
  the artifact; `claimExpired` never contacts the signer.
- Concurrent claims are exclusive and nonce reservation survives restart.
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
