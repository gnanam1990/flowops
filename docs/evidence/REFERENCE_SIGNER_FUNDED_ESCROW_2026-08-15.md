# Funded customer reference-signer escrow evidence

Status: complete on Base Sepolia; no mainnet authorization

Date: 2026-08-15

Machine record: `docs/evidence/REFERENCE_SIGNER_FUNDED_ESCROW_2026-08-15.json`

## Result

A customer-run FlowOps reference signer, backed by Clef, consumed one signed
escrow authorization and submitted the exact `CallEscrow.fund(...)` transaction
on Base Sepolia. The durable control-plane worker registered the signer receipt,
independently reconciled the canonical FUND event and USDC transfer through two
RPC observers, created the funding ledger entry, and recorded positive finality.

After the acknowledgement deadline expired, the buyer submitted
`refundExpired(...)`. The same worker reconciled the canonical REFUND and
returning USDC transfer, appended the refund ledger entry, and reached terminal
state `REFUNDED` with no pending transition.

## Bound facts

- Network: Base Sepolia (`84532`)
- CallEscrow: `0x86e145397f58e71c134c0e054320dB929483227a`
- Test USDC: `0x036CbD53842c5426634e7929541eC2318f3dCF7e`
- Buyer: `0x079bDde909e28E437768A06d7001eb40896668d4`
- Provider: `0xC2f0967C4Df966636E4Ac1dad40abdA65536cbb6`
- Amount: `100000` atomic units (`0.1` test USDC)
- Call ID: `0xa9cb4708de15f8f3a9ced649a949aab3539a5c9f1cab00186c48c324f10b8e3e`
- FUND: `0x0bacd7dff777cc646d1f48984e7a240fd914d416f5b93c14831c3fbcedaf89ab`
- REFUND: `0x8813d944c1851279ef5bbc4899f47dba1f87841b6bc2029738dd2647b06107e6`
- Final escrow balance and allowance: zero

The validator recomputes the call ID and both calldata payloads, verifies the
proof's immutable tuple, source and binary identity, dual-observer agreement,
transition ordering, ledger/finality evidence, terminal balances, and explicit
limitations. It makes no network request and reads no signer secret.

Run the offline validator with `make funded-signer-evidence-check`. The mainnet
readiness mutation suite also corrupts each load-bearing proof field and
requires rejection. Run `make smoke-funded-signer-evidence` for a read-only
refresh that requires two distinct RPC hosts to return identical transactions,
receipts and logs, then checks the terminal escrow balance and allowance.

## What this proves

- the escrow reference-signer rail can enforce one exact signed authorization
  and cross the customer wallet boundary with a capped amount;
- the durable production-shaped reconciliation path can recognize its canonical
  FUND and terminal REFUND without inventing settlement; and
- the signer-funded Sepolia evidence gate can close.

## What this does not prove

It does not select production RPC providers, approve a confirmation depth,
complete external security or legal review, prove direct-USDC and escrow rails
together under a production pilot, designate a mainnet hardware wallet, or
authorize deployment or funding on Base mainnet. Public Sepolia RPCs and a
manually submitted refund remain test-only limitations.
