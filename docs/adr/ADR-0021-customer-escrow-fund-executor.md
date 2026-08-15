# ADR-0021: Customer-bound CallEscrow fund executor

Status: accepted; funded Sepolia proof completed 2026-08-15
Date: 2026-08-14

## Context

Durable escrow reconciliation could verify exact events, but funding still
depended on manual transaction-hash intake. That did not prove the transaction
was built from the issued authorization, submitted inside its validity window,
or constrained by the independent signer pilot cap.

## Decision

The customer-run reference signer supports one configured rail per process.
Escrow mode accepts only exact escrow authorizations and prepares only
`CallEscrow.fund(bytes32,address,uint256,bytes32,bytes32,uint64,uint64)`.
Before Clef can sign, it verifies Base chain identity, contract bytecode, the
live immutable asset and release window, sufficient USDC allowance, successful
simulation, gas and fee caps, and the complete signed transaction bytes.

The signer never creates an approval and never receives a private wallet key.
Its existing one-way journal, nonce consumption, freeze/chain gates, signed
broadcast receipt, and conservative per-signer exposure accounting apply to
both direct and escrow attempts.

`POST /v1/signer/escrow-broadcasts` authenticates with the customer receipt
signature, resolves the issued authorization, requires the receipt sender to
be the exact buyer, and durably stores the authorization, signed receipt, and
verification key with the FUND transition. A delayed callback may be retained
after authorization expiry or during a halt when its signed broadcast time was
inside the authorization window; no chain outcome is inferred.
The step-up Owner/Admin transition endpoint explicitly refuses FUND, so it
cannot bypass signer limits or fabricate customer attestation.

## Consequences

- FlowOps never controls or receives the customer wallet key.
- Config version v3 requires an explicit rail; a process cannot silently widen
  from direct transfer to escrow funding.
- Provider and permissionless lifecycle transitions remain outside customer
  spend authority and continue through the strict reconciliation intake.
- Sufficient allowance is a prerequisite, not an instruction to approve.
- A separately approved funded Base Sepolia run proved the signer callback and
  canonical FUND-to-REFUND reconciliation path. Its machine record is
  `docs/evidence/REFERENCE_SIGNER_FUNDED_ESCROW_2026-08-15.json`. Mainnet
  remains blocked by the independent production gates.

## Verification

Run `make smoke-escrow-signer`, `make smoke-pilot-limits`, and `make check`.
Validate the funded proof with
`deploy/call-escrow/check-funded-reference-signer-evidence.sh`.
