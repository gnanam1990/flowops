# ASCP approval lifecycle

`ascpapproval` creates exactly one pending approval for an escalated intent;
it does not reserve budget. The review snapshot binds commitment, policy and
directory versions/hashes, payee, acknowledgement authority, amount,
verification spec, protection, chain, and asset.

`REQUESTED` transitions by compare-and-swap only to `APPROVED`, `REJECTED`,
`EXPIRED`, or `CANCELLED`. The decision must echo the snapshot hash, terminal
outcomes are never rewritten, and a withdrawal/suspension can only cancel a
still-pending approval. Expiry is evaluated atomically during decision/cancel.

Before turning `APPROVED` into an execution authorization, a later module must
revalidate current policy/directory/seller conditions and reserve budget in its
same database transaction. This package does no signing, reservation, or payment.

`PostgresStore` implements create/replay and all terminal transitions using
conditional SQL updates; the in-memory store exists solely for focused tests.
