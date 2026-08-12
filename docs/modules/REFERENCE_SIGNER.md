# Customer reference signer

Status: verifier, durable nonce store, one-way executor, signed receipt,
strict callback transport, Clef direct-USDC adapter, and runnable command
implemented; live Base Sepolia execution remains separately approved and open

Packages: `pkg/referencesigner`, `pkg/referencewallet`
Command: `cmd/reference-signer`

## Why it exists

FlowOps may authorize a payment, but it must not control customer wallet keys.
This module is the customer-run enforcement boundary between an authorization
and a Base transaction.

## Input

- a domain-separated FlowOps signed authorization;
- customer-configured FlowOps public trust roots;
- exact local organization/customer identity, chain, rail, asset, recipient,
  amount, TTL, freeze, and chain-health policy;
- a customer-run Clef-compatible wallet that prepares one exact signed
  direct-USDC transaction without exposing its key; and
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
go test -race ./cmd/reference-signer ./pkg/referencesigner ./pkg/referencewallet ./pkg/broadcastreceipt
make smoke-reference-signer
```

The smoke target executes the full command wiring against in-memory Base, Clef,
and FlowOps transports. It validates a real signed EIP-1559 transaction but
never contacts Base or moves funds.

## Remaining integration gate

Independently review the adapter and runnable command, then execute a separately
approved, capped Base Sepolia test with a designated customer wallet and
configured receipt public key. This module does not authorize that funded test
by itself, and mainnet remains blocked.
