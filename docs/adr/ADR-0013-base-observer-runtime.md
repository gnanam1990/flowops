# ADR-0013: Continuous Base Observer Runtime and Operator Gate

Status: Accepted for the capped Base Sepolia pilot
Date: 2026-08-12

## Context

The reconciliation engine already rejects stale, disputed, or insufficient
canonical evidence, but the production control-plane did not feed it live Base
observations. A reachable HTTP endpoint is not proof that Base is producing a
canonical chain. The API therefore remained correctly paused in
`SUSPECTED_STALL` after deployment.

Base documents chain ID `84532` and `https://sepolia.base.org` for Base Sepolia,
and explicitly warns that the public endpoint is rate-limited and not intended
for production applications. Base also documents
`https://base-sepolia-rpc.publicnode.com` as a public alternative while
recommending a dedicated provider for production.

## Decision

- The control-plane owns one continuous, read-only observer supervisor.
- Two to five providers are supplied through the secret manager as a strict
  JSON array. Provider names and HTTPS hostnames must be unique.
- Each poll verifies `eth_chainId`, obtains every latest sealed block, selects
  the lowest head as the comparison anchor, and asks every responding provider
  for that exact block.
- The supervisor polls immediately at startup and then at the configured
  interval. Observation timeout must be shorter than the interval, and the
  interval must be shorter than the heartbeat and stall thresholds.
- Every result, including an empty response set, is durably applied to the
  hash-chained reconciliation journal. A journal append or sync failure stops
  the process. Provider failures change chain state but do not crash the
  process.
- RPC URLs and provider error strings are not emitted by the supervisor log or
  API. Only provider counts, quorum, chain state, and canonical checkpoints are
  exposed.
- Recovery never resumes autonomous execution by itself. A dedicated 32-byte
  operator-control key protects global halt and resume endpoints. Tenant
  credentials and read-only Sites sessions cannot call them.
- `flowops-operator` reads the key from the environment, blocks redirects, and
  sends strict JSON over HTTPS. The key never belongs in command arguments.

## Capped-pilot posture

The initial Base Sepolia drill may use the two public hosts named above to
prove integration and failure handling. That pair is not contractual evidence
of production independence, capacity, archive guarantees, or incident support.
Mainnet and customer value remain blocked until dedicated providers are
selected and assessed.

The initial pilot thresholds are configuration, not universal Base truth:

- poll interval: 15 seconds;
- per-poll timeout: 10 seconds;
- required quorum: 2;
- halt after 2 consecutive unhealthy observations;
- recovery readiness after 3 consecutive healthy observations;
- maximum head skew: 2 sealed blocks;
- observation heartbeat age: 45 seconds;
- stale-head threshold: 2 minutes;
- receipt confirmation floor: 2 sealed blocks;
- reorg lookback: 12 blocks.

## Consequences

The live dashboard can truthfully distinguish observer reporting from a trusted
manual release. Startup and restart still pause writes. Public-RPC throttling
can cause a fail-closed halt during the pilot, which is preferable to inventing
availability. Threshold changes require a deployment configuration record and
new drill evidence.

## Acceptance gate

- healthy independent observations reach `RECOVERING` and persist a canonical
  checkpoint;
- no resume succeeds before the stability window and ambiguous-execution gate;
- an authenticated named operator can release `HEALTHY`, and the exact retry is
  idempotent;
- stale, missing, wrong-chain, redirecting, or disagreeing providers pause
  authorization;
- a journal failure terminates the service;
- API and dashboard expose counts and checkpoint state without RPC URLs or
  credentials.
