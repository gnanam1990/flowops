# x402 Builder Code Experiment

Status: implemented and locally verified; live 0.001 test-USDC settlement awaits exact Builder Codes and explicit user approval.

## Why this module exists

The hosted facilitator, not the buyer, constructs and broadcasts an EIP-3009
x402 settlement transaction. FlowOps therefore needs transaction-level evidence
that the selected hosted deployment preserves the client `s` Builder Code and
appends a valid ERC-8021 Schema 2 suffix. An announcement or `/supported`
response is not enough.

## User entry and inputs

The operator starts `cmd/x402-builder-experiment` with one of four commands:

- `prepare`: fixed Base Sepolia/test-USDC payment, designated payer/payee, and
  operator-supplied app/service Builder Codes;
- `attach-signature`: reads a typed-data signature from a small file or stdin,
  produced by the local Foundry keystore; no private key enters FlowOps and the
  spend-capable signature need not appear in shell history;
- `settle`: verifies first and only then settles through the hosted facilitator;
- `inspect`: proves the canonical transaction through two independent RPCs.

The command is pinned to:

| Field | Value |
|---|---|
| Network | Base Sepolia `eip155:84532` |
| Asset | test USDC `0x036CbD53842c5426634e7929541eC2318f3dCF7e` |
| Amount | 1,000 atomic units = 0.001 test USDC |
| Payer | `0x079bDde909e28E437768A06d7001eb40896668d4` |
| Payee | `0xC2f0967C4Df966636E4Ac1dad40abdA65536cbb6` |
| Facilitator | `https://x402.org/facilitator` |

## Internal flow

1. Generate a single-use EIP-3009 nonce and a 15-minute validity window.
2. Bind network, asset, amount, payer, payee, EIP-712 domain, app code, and
   service code into a deterministic preparation digest.
3. Write the preparation and cast-compatible typed data as new mode-0600 files;
   existing files are never overwritten.
4. Recover the supplied signature and require the fixed payer.
5. Require the exact confirmation phrase before making any facilitator call.
6. Call `/verify`; refuse `/settle` unless verification is valid for the payer.
7. Require a successful Base Sepolia settlement response with a transaction hash.
8. Read transaction and receipt from two independent RPCs and require agreement.
9. Derive the facilitator transaction sender, verify the exact USDC Transfer,
   parse the entire calldata, and require the expected `a` and `s` in a terminal
   ERC-8021 Schema 2 suffix.

## Outputs

- unsigned preparation and typed-data artifacts;
- signed, short-lived authorization artifact;
- hosted facilitator verify/settle result and transaction hash;
- canonical proof containing block, full calldata, transfer evidence, decoded
  `a`, `s`, optional `w`, and `VERIFIED_SUFFIX` attribution state.

The signed artifact is a short-lived spend authorization. Store it outside the
repository, mode 0600, and remove it after the experiment or expiry.

## Failure states

- wrong network, token, amount, payer, payee, transfer method, or EIP-712 domain;
- changed preparation digest, typed data, or Builder Code payload;
- expired or wrong-payer signature;
- missing exact settlement confirmation;
- failed facilitator verification or malformed settlement result;
- RPC disagreement, pending/reverted transaction, wrong facilitator sender;
- missing or substituted USDC Transfer;
- missing/malformed Schema 2 suffix or absent expected `a`/`s`.

Every state fails closed. No database or facilitator response can manufacture
canonical attribution.

## Acceptance criteria

- Mutation and race-enabled tests pass.
- Read-only facilitator conformance reports V2 exact Base Sepolia,
  `builder-code`, and at least one valid signer.
- No command accepts a private key.
- No settlement happens without the exact confirmation phrase.
- A live result becomes supported only after two-RPC transaction, receipt,
  transfer, sender, and calldata verification passes.
- Mainnet is absent from the executable experiment.
