# ADR-0027: Reconciliation operator read model and unproven outcomes

Status: Accepted

Date: 2026-08-15

## Context

The reconciliation journal already held canonical receipts, corrections,
escrow transitions, chain checkpoints, and unresolved broadcasts, but the
dashboard exposed none of that state. Its financial cards therefore remained
unavailable. The raw ledger accounts are also not asset-scoped, so summing them
and labelling the result USDC would be unsafe when more than one asset or an
unbound manual ledger entry exists.

A missing receipt is not proof that a transaction was dropped. A different
transaction hash is not proof that it replaced the original unless the sender
nonce and executable content are independently bound. Neither an operator nor
the UI may convert those hypotheses into a terminal economic outcome.

## Decision

- Build a tenant-scoped read model directly from the durable reconciliation
  engine while holding its state lock.
- Reconstruct each ledger transaction's asset only from its immutable direct
  execution or escrow-call reference. Exclude unbound entries from asset totals
  and report their count.
- Expose recognized expense, escrow locked, UTC-day and UTC-month expense, and
  unresolved exposure as signed atomic integers. These are operational
  subledger facts, not wallet balances, spendable funds, or statutory accounts.
- Expose candidate resolution, bounded-finality, checkpoint, quarantine, and
  manual-resume progress. “Observed through” is the last trusted observer
  checkpoint; it is not a claim that an indexer scanned every application event
  in the range.
- Protect the cross-tenant reconciliation view and execution-quarantine action
  with the dedicated operator-control key. A tenant or Sites credential cannot
  call them.
- Accept only `DROPPED_UNPROVEN`, `REPLACED_UNPROVEN`, or
  `MANUAL_INVESTIGATION` as quarantine dispositions. Quarantine is a containment
  state, not proof of a dropped or replacement transaction, and never creates a
  settlement, refund, retry, or replacement broadcast.
- Keep the customer dashboard read-only for reconciliation operator actions.
  It may show the organization-scoped evidence and exceptions but cannot carry
  the operator-control key.

## Consequences

- The dashboard can show real economic evidence without inventing custody or
  available balance.
- Multiple assets remain separate. If exactly one proved asset is present the
  UI can show its atomic totals; otherwise it shows item counts and an explicit
  multi-asset state.
- Generic reservation or suspense entries without an immutable asset reference
  remain visible as excluded counts until the ledger schema gains first-class
  asset identity.
- Automated proven dropped/replacement resolution still requires quorum nonce
  and transaction-content evidence and remains future work.

## Verification

- read-model tests cover unresolved, halt, quarantine, canonical settlement,
  signed totals, unclassified entries, and tenant isolation;
- operator API tests prove the dedicated credential boundary, organization
  scoping, and refusal of a falsely proved `DROPPED` disposition;
- operator-client tests prove exact paths and that the execution ID is not
  duplicated into the request body;
- dashboard tests prove journal-derived values and exceptions render while
  credentials remain server-side.
