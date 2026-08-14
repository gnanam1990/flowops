# Evidence Fetch module

Status: implemented locally and delivered through a live Base Sepolia
CallEscrow release; public paid route not yet enabled

Package: `internal/evidencefetch`

Reference process: `cmd/evidence-fetch`

## Purpose

Evidence Fetch turns a public web resource into objective delivery evidence. It returns normalized UTF-8 text, HTTP metadata, a source-body hash, a full normalized-content hash, and a digest binding the fetch parameters to the caller's FlowOps request digest.

It proves what bytes were fetched and what text was delivered. It does not assert that the source is true, current, complete, or suitable for a business decision.

## Request contract

`POST /v1/fetch` accepts JSON with:

- `url`: a public HTTP or HTTPS URL using its scheme's default port;
- `mode`: optional `auto` or `text` value;
- `maxOutputBytes`: optional per-request limit bounded by the service limit;
- `requestDigest`: required canonical lowercase 32-byte hex digest from the parent task, intent, or authorization request.

Unknown fields, multiple JSON values, URL credentials, fragments, non-default ports, unsupported schemes, and invalid digests fail closed.

## Delivery output

Successful output includes:

- original and final URLs;
- UTC fetch timestamp;
- parent request digest and deterministic fetch digest;
- normalized text and a truncation flag;
- SHA-256 of the decoded HTTP representation body;
- SHA-256 of the full normalized text, including text beyond the returned output limit;
- status, content type, byte length, ETag, and Last-Modified metadata when present.

Only successful 2xx responses with non-empty supported UTF-8 text are deliverable. A failed, empty, oversized, auth-gated, or unsupported response never becomes delivery evidence.

## Network safety invariants

- Every initial URL and redirect is validated.
- DNS is resolved before the request and again immediately before the connection.
- The connection uses the already-validated literal address, preventing a second implicit DNS lookup.
- If any DNS answer is private or otherwise non-public, the entire destination is rejected.
- Loopback, private, link-local, metadata, CGNAT, multicast, unspecified, benchmark, documentation, and reserved ranges are rejected for IPv4 and IPv6.
- Environment proxies are ignored; compressed content encodings are refused; redirects, headers, and response bytes are bounded.
- HTTPS-to-HTTP redirect downgrades are rejected.
- Public HTTP is supported for protocol compatibility; operators should require HTTPS by policy for sensitive tasks.

## Current operational boundary

The reference process deliberately accepts only loopback listeners because the route does not yet enforce x402 payment or customer authentication. Run it with:

```sh
go run ./cmd/evidence-fetch
```

The public listener gate may be removed only when the x402 middleware wraps `/v1/fetch`, rate limiting and abuse controls are configured, and the deployment smoke test proves unauthenticated requests receive the expected payment-required response.

## Verification

```sh
go test -race ./internal/evidencefetch ./cmd/evidence-fetch
make smoke-evidence-fetch
```

The test suite covers direct IP and DNS-based SSRF, mixed public/private DNS answers, link-local cloud metadata, DNS rebinding, unsafe redirects, response limits, content types, non-2xx responses, empty evidence, deterministic hashing, UTF-8-safe truncation, strict JSON handling, and loopback-only serving.

## Deferred integrations

- x402 V2 payment middleware and Builder Code attribution;
- Bazaar discovery metadata;
- escrow acknowledgement, release, expiry, and testnet-only forced failure;
- durable evidence-record storage and Base settlement reconciliation;
- allowlisted selector or schema extraction.

These are explicit later-module dependencies, not behavior claimed by this module.

The live delivery output, digest derivation, and four-transition escrow manifest
are recorded in
`docs/evidence/CALL_ESCROW_EVIDENCE_FETCH_LIVE_2026-08-14.md`.
