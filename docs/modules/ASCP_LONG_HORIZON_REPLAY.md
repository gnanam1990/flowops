# ASCP long-horizon replay boundary

AC-42 is implemented by two independent durable authorities:

- `ascp_financial_tombstones` permanently owns the control-plane idempotency
  scope and SellerQuote nonce. Intake creates the tombstone and full intent in
  one serializable transaction. Exact retries resolve from the tombstone;
  changed input or a second operation using the quote nonce is rejected.
- `pkg/sellerresult` is the seller-side execution guard. It persists
  `STARTED_UNKNOWN` before invoking a resource effect and stores the exact HTTP
  status, headers, body, and content digest on completion. A restored unknown
  record fails closed instead of executing again.

Seller result retention is fixed at `settleBy + 9,600 hours` (400 24-hour days).
The resource-level operation key is mandatory, including for non-idempotent
seller resources. The control-plane runtime role has no privilege on
`ascp_seller_results`; a separately deployed seller service must receive its
own least-privilege role.

Focused conformance evidence:

```text
go test ./internal/ascpintake ./pkg/sellerresult ./internal/dbreadiness
```

The tests restore durable snapshots and retry after 7, 30, and 365 days. They
also cover changed-request substitution, quote-nonce/resource-key reuse, exact
response replay, and crash recovery that never starts a second effect.

Production enablement still requires backup/restore evidence for both tables
and a configured seller deployment. Those operational controls are release
gates; this module does not claim them from unit tests.
