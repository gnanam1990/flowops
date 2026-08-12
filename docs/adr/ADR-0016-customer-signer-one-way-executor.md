# ADR-0016: Customer-Signer One-Way Executor

Status: Accepted for adapter integration
Date: 2026-08-12

## Context

A valid FlowOps authorization is not proof that a customer transaction was
broadcast. Conversely, an RPC timeout after submission is not proof that it was
not broadcast. Retrying a wallet call after that uncertainty can pay twice.
The executor therefore needs a crash boundary that prefers an unresolved
payment over a duplicate payment.

The implementation belongs inside the customer environment. FlowOps receives
only the signed broadcast receipt defined by ADR-0015; it never receives the
signed raw transaction, wallet credential, RPC URL, or receipt private key.

## Decision

The customer executor uses this durable state machine:

```text
PREPARED -> BROADCASTING -> SUBMITTED  -> REGISTERED
                         -> AMBIGUOUS  -> REGISTERED
```

`PREPARED` contains the exact signed transaction bytes, their EVM Keccak-256
hash, and sender. The customer journal synchronizes that record before any
network submission. Immediately before `BROADCASTING`, the executor re-verifies
the signed authorization against the customer's current FlowOps trust roots
and repeats the time, local-policy, freeze, and independent chain-health gates.

`BROADCASTING` is synchronized before calling the wallet adapter. Once this
state exists, no code path may call the wallet again for that authorization.
A normal adapter return produces `SUBMITTED`. Every error, timeout,
cancellation, process death, or restart after `BROADCASTING` produces
`AMBIGUOUS`; none grants retry authority.

Both outcomes receive a domain-separated customer receipt. Only receipt
registration is retried. If FlowOps accepts the callback but the local
`REGISTERED` append fails, restart repeats the same signed callback and never
the transaction.

The attempt journal is process-locked, append-only, hash-chained, mode `0600`,
and synchronized before state becomes visible. Corruption, invalid transition,
identity mutation, or concurrent ownership fails closed. Raw transaction bytes
are intentionally retained only in this customer-controlled journal because
they are required to prove that the prepared hash did not change across a
crash. Operators must protect, back up, and eventually expire that journal as
sensitive operational data.

## Adapter contract

- `Prepare` returns one fully signed transaction and performs no submission or
  other network action that could move funds.
- `Broadcast` submits exactly the supplied bytes once and never constructs,
  signs, replaces, bumps, or retries a transaction.
- A concrete adapter must prove sender, chain, nonce, calldata/transfer fields,
  gas policy, simulation posture, and replacement behavior before pilot use.
- This module supplies the engine and callback transport. It does not yet ship
  an EOA, HSM, smart-account, or hosted-wallet adapter, nor an installable signer
  command. Those remain separate customer-boundary modules.

## Callback transport

The HTTP registration sink requires HTTPS except for loopback development,
requires a positive client timeout, refuses redirects and URL credentials,
bounds response bodies, and does not expose server response bodies in errors.
An HTTP success is only callback acceptance; Base reconciliation remains the
source of settlement truth.

## Consequences

- A crash after `BROADCASTING` can strand a transaction as `AMBIGUOUS` even if
  the wallet adapter was never entered. This availability loss is deliberate.
- Customer revocation remains effective until the final durable broadcast
  boundary; it does not require FlowOps cooperation.
- A failed `Prepare` burns the one-time authorization nonce. The control plane
  must issue a newly evaluated authorization rather than reuse it.
- The executor serializes local attempts. Horizontal replicas must not share a
  wallet/journal until a later design supplies an equivalent single-owner
  fencing proof.

## Acceptance evidence

- success and concurrent replay cross the wallet boundary exactly once;
- restart from `PREPARED` broadcasts once;
- restart from `BROADCASTING` broadcasts zero times and records ambiguity;
- timeout and cancellation after wallet entry persist ambiguity;
- callback failure and lost local callback acknowledgement retry only receipt
  registration;
- trust-root removal, freeze, expiry, or unhealthy chain at the second gate
  stops the wallet;
- prepared-hash mismatch, authorization mutation, journal tampering, and a
  concurrent journal owner fail closed; and
- callback redirects, unsafe URLs, unbounded responses, and invalid JSON fail
  closed.
