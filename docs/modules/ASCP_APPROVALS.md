# ASCP approval lifecycle

`ascpapproval` creates exactly one pending approval for an escalated intent;
it does not reserve budget. The review snapshot binds commitment, policy and
directory versions/hashes, payee, acknowledgement authority, amount,
verification spec, protection, chain, and asset.

`REQUESTED` transitions by compare-and-swap only to `APPROVED`, `REJECTED`,
`EXPIRED`, or `CANCELLED`. The decision must echo the snapshot hash, terminal
outcomes are never rewritten, and a withdrawal/suspension can only cancel a
still-pending approval. Expiry is evaluated atomically during decision/cancel.

Before turning `APPROVED` into an execution authorization, `ascpexecauth`
reconstructs and verifies the exact approved review hash, revalidates current
policy/directory/seller conditions, and reserves budget in the same database
transaction. This package itself does no signing, reservation, or payment.

`PostgresStore` implements create/replay and all terminal transitions using
conditional SQL updates; the in-memory store exists solely for focused tests.
