# Customer reference signer

Status: verifier, durable nonce store, one-way executor, signed receipt,
strict rail-specific callback transport, Clef direct-USDC and CallEscrow FUND adapters, and runnable command
implemented; independent durable signer pilot exposure limits are
enforced; one capped Base Sepolia escrow FUND-to-REFUND execution is funded and
canonically reconciled

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
  direct-USDC transfer or CallEscrow FUND without exposing its key; and
- a separate customer Ed25519 key used only to attest the broadcast result.

## Internal behavior

1. Verify signature, identity, limits, time, freeze, chain health, and a durable
   one-time nonce.
2. Refuse the configured per-action or conservative aggregate pilot ceiling
   before wallet preparation. Reconstruct the aggregate from the durable
   attempt journal after restart.
3. Ask the wallet adapter to prepare, but not submit, one exact transaction.
4. Validate and durably store the raw transaction/hash in `PREPARED`.
5. Re-verify the current trust root and all local gates.
6. Durably enter `BROADCASTING`, then invoke the wallet exactly once.
7. Persist `SUBMITTED` or `AMBIGUOUS` even if the caller was cancelled.
8. Sign and retry only the FlowOps receipt callback until `REGISTERED`.

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
make smoke-pilot-limits
make smoke-rpc-admission
```

The smoke target executes the full command wiring against in-memory Base, Clef,
and FlowOps transports. It validates a real signed EIP-1559 transaction but
never contacts Base or moves funds.

The production RPC gate changed the strict file schema to
`flowops.reference-signer.v4`. Versions `v1`, `v2`, and `v3` are rejected rather
than guessed; operators must select exactly one `rail`, escrow mode must pin the
reviewed contract and immutable release window, and Base mainnet must bind every
secret provider endpoint to distinct paid operator and failure-domain metadata.
Base Sepolia rejects that production metadata.

## Funded integration evidence

The separately approved capped Base Sepolia escrow run is recorded in
`docs/evidence/REFERENCE_SIGNER_FUNDED_ESCROW_2026-08-15.md`. It binds the
designated test wallet, configured receipt key, signed authorization, exact
FUND calldata, canonical FUND and REFUND receipts, ledger entries, finality,
and terminal zero escrow balance and allowance.

The command rejects `x402`. This single escrow proof does not complete
direct-USDC funded pilot evidence, production dependency admission, external
review, or any Base mainnet authorization.
