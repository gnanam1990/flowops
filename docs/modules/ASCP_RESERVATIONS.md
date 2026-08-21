# ASCP budget reservations

`ascpreservation` is the conservative budget-state boundary. It refuses an
operation unless every configured dimension has sufficient remaining balance,
and it creates one reservation per immutable operation ID.

`RESERVED` is pre-signature only and may be released only after its expiry.
Once the bearer is live, ordinary TTL, signer rotation, a pause, or a lost
relay receipt cannot free budget. Only a consumed lock or an externally proven
expiry/invalidation path may transition it onward. Safe/finalized receipts and
reorgs use distinct states so later accounting and reconciliation cannot
collapse economic truth.

The execution-authorization module now performs current local revalidation,
organization-scoped dimension accounting, and the reservation insert in one
serializable SQL transaction. Pre-signature reservations cannot exceed the
15-minute TTL. PostgreSQL race coverage proves that concurrent approvals cannot
both claim the same final budget slot. Dimension IDs and limits are derived
from trusted PurchaseSpec and active-policy values, never accepted as caller
authority. Successful release consumes all dimensions; refund restores only
the dimensions explicitly marked refundable.
