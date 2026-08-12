# Customer reference signer

Status: verifier, durable nonce store, one-way executor, signed receipt, and
strict callback transport implemented; wallet adapter, runnable packaging, and
live Base Sepolia execution remain open

Package: `pkg/referencesigner`

## Why it exists

FlowOps may authorize a payment, but it must not control customer wallet keys.
This module is the customer-run enforcement boundary between an authorization
and a Base transaction.

## Input

- a domain-separated FlowOps signed authorization;
- customer-configured FlowOps public trust roots;
- exact local organization/customer identity, chain, rail, asset, recipient,
  amount, TTL, freeze, and chain-health policy;
- a customer wallet adapter that prepares and broadcasts one exact signed
  direct-USDC transaction; and
- a separate customer Ed25519 key used only to attest the broadcast result.

## Internal behavior

1. Verify signature, identity, limits, time, freeze, chain health, and a durable
   one-time nonce.
2. Ask the wallet adapter to prepare, but not submit, one exact transaction.
3. Validate and durably store the raw transaction/hash in `PREPARED`.
4. Re-verify the current trust root and all local gates.
5. Durably enter `BROADCASTING`, then invoke the wallet exactly once.
6. Persist `SUBMITTED` or `AMBIGUOUS` even if the caller was cancelled.
7. Sign and retry only the FlowOps receipt callback until `REGISTERED`.

## Output

The local output is a durable attempt. The only output sent to FlowOps is the
signed receipt containing authorization identity/digest, transaction hash,
sender, outcome, and broadcast time. FlowOps still waits for canonical Base
evidence before recognizing settlement.

## Failure states

- authorization or local gate refusal: no wallet preparation or broadcast;
- preparation failure: nonce remains consumed, no attempt is broadcast;
- second-gate refusal: durable `PREPARED`, no network submission;
- any uncertainty after `BROADCASTING`: durable `AMBIGUOUS`, never rebroadcast;
- callback failure: `SUBMITTED`/`AMBIGUOUS` remains durable and callback-only
  retry is safe;
- corrupt or concurrently owned journal: startup/append fails closed.

## Verification

```sh
go test -race ./pkg/referencesigner ./pkg/broadcastreceipt
make smoke-signer-executor
```

The smoke target uses deterministic fake wallet and callback adapters. It never
loads a wallet key, contacts Base, or moves funds.

## Remaining integration gate

Implement and independently review a concrete customer wallet adapter and
runnable sidecar. Then execute a separately approved, capped Base Sepolia test
with a designated customer wallet and configured receipt public key. This
module does not authorize that funded test by itself.
