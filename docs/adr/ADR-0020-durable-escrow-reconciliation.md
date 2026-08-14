# ADR-0020: Durable CallEscrow intent and transition reconciliation

Status: accepted
Date: 2026-08-14

## Context

The existing CallEscrow decoder could verify a completed local manifest, but it
did not register an approved call before broadcast, persist transition
candidates, drive continuous receipt/finality checks, or correct ledger effects
after a reorg. The authorization envelope also named only the generic escrow
rail; it did not sign the contract, buyer, provider, call ID, digests, or
deadlines that determine the actual calldata.

## Decision

- An escrow payment intent carries optional `escrow` terms only when its rail is
  `escrow`. The same terms are copied into and signed by the issued
  authorization. Non-escrow rails reject them.
- The terms bind contract, buyer, provider, canonical call ID, task and request
  digests, acknowledgement and delivery deadlines, and release window. The
  provider must equal the approved payment recipient.
- Durable admission additionally requires the exact configured reviewed
  deployment tuple: chain, CallEscrow address, asset address, and immutable
  release window. Omitting the whole tuple disables escrow admission; partial
  or mismatched configuration fails startup or registration.
- `POST /v1/escrow/intents/{authorizationID}` derives every durable field from
  the still-valid issued authorization and control-plane journal. It does not
  accept an authoritative tenant or economic body from the caller.
- Transition registration accepts only the dynamic action fields and an
  already-broadcast transaction hash. The durable registry reconstructs the
  complete expected receipt from the immutable call and enforces the contract
  order. It never signs, broadcasts, retries, releases, or refunds.
- The reconciliation worker requires independent receipt quorum before moving
  the position. Funding posts `escrow_locked / pending_settlement`; release
  moves locked value to `agent_service_expense`; refund clears the locked and
  pending balances. A receipt containing more than one lifecycle event for the
  same call is refused, so a batched transaction cannot move the contract ahead
  of durable state. Database state never creates a release or refund without the
  corresponding canonical contract and USDC events.
- Each confirmed transition remains under bounded canonical-block monitoring.
  A reorg appends exact corrections for that transition and every dependent
  ledger effect, quarantines the position, and returns the chain gate to
  `RECOVERING`. Removing a previously reverted receipt quarantines the call but
  does not reverse later independently confirmed effects. FlowOps does not
  guess a replacement lifecycle.
- During a halt, an already-broadcast transition hash may be retained as
  `PENDING_CHAIN_RECOVERY`, but it cannot be recognized as funded, released, or
  refunded until the chain and quorum gates recover.

## Consequences

- The manual lifecycle manifest remains useful as portable evidence, while the
  production worker now owns durable transition progress.
- Existing non-escrow authorization canonical bytes remain unchanged because
  the new signed field is omitted when absent. Older consumers fail closed on
  escrow authorizations they do not understand.
- Transition hashes are a temporary step-up-protected Owner/Admin intake until
  an independently enforcing customer escrow executor can attest broadcasts.
  A typo or lost transaction remains pending and must not trigger a blind
  replacement.
- This ADR does not authorize mainnet, create a customer escrow signer, or
  prove that a transaction was broadcast within an authorization window. The
  reference signer still rejects escrow; an independently enforcing escrow
  executor and funded Sepolia proof remain required before pilot admission.

## Verification

```sh
make smoke-escrow-durable
go test -race ./internal/reconciliation ./internal/controlapi ./internal/controlplane ./pkg/envelope
make check
```
