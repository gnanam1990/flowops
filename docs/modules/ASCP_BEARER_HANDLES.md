# ASCP bearer handles

Prepared signer artifacts are never released directly. A handle progresses
`PREPARED → ACTIVE → RELEASED → TERMINAL`; only `ACTIVE` can release the
encrypted artifact, its first release is durable/idempotent, and terminal
finalization erases encrypted signature material while retaining its identity.

`PREPARED` can expire only after validity elapsed and after reconciliation
proves it was never activated. An `ACTIVE` handle cannot be TTL-released.
The durable schema holds the non-secret binding and is ready for the signer
ledger adapter, which will activate it with the associated reservation and
bearer registry transaction.
