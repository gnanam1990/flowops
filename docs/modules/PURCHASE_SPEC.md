# FlowOps PurchaseSpec

## Why and entry

`purchasespec.Build` deterministically constructs the seller-request binding
used by SellerQuote intake. It receives the intended request before any rail
adds transport or payment headers.

## Inputs and behavior

It requires an HTTPS URL, request method/body, material headers, response
contract, category, and optional content-addressed reason. It lowercases and
IDNA-normalizes the URL host, removes default HTTPS port/dot segments,
normalizes percent encoding, and sorts query pairs without collapsing duplicate
keys. The returned `Spec`, canonical JSON, hash, and stripped transport header
names must be persisted together with the exact body bytes.

## Failure states and acceptance

- HTTP, credentials, fragments, invalid ports/percent encoding, or malformed
  query pairs reject.
- Agent credentials, cookies, forwarding, payment, connection, and other
  hop-by-hop headers reject; caller-supplied `traceparent`/`accept-encoding`
  are visibly stripped.
- Duplicate material headers, invalid UTF-8/control characters, malformed
  reason hashes, and non-canonical JSON-safe input reject.
- Changing an outbound body byte, material header value, URL, response
  contract, or reason changes the hash.

Run `go test -race ./pkg/purchasespec` for canonical URL, header, body, reason,
and golden-vector coverage.
