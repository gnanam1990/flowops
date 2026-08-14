# CallEscrow Evidence Fetch live lifecycle — 2026-08-14

Status: complete on Base Sepolia; Base mainnet remains prohibited

This evidence records one real FlowOps Evidence Fetch request funded with 0.1
test USDC, acknowledged by the provider, delivered with objective response
digests, and released by explicit buyer acceptance. The four-transition
manifest passed the repository's two-provider read-only conformance verifier.

## Immutable call snapshot

- Network: Base Sepolia, chain ID `84532`
- CallEscrow: `0x86e145397f58e71c134c0e054320db929483227a`
- Asset: test USDC at `0x036cbd53842c5426634e7929541ec2318f3dcf7e`
- Buyer: `0x079bdde909e28e437768a06d7001eb40896668d4`
- Provider: `0xc2f0967c4df966636e4ac1dad40abda65536cbb6`
- Amount: `100000` atomic units, or 0.1 test USDC
- Call ID: `0x7e0eb6cf0c0086428200e368038c2a7c5354af7dd00761cd964b9bbb79cd5edb`
- Acknowledge by: `1786776352` (`2026-08-15T06:45:52Z`)
- Deliver by: `1786949152` (`2026-08-17T06:45:52Z`)
- Optimistic release window: 3,600 seconds

The exact task JSON was:

```json
{"service":"flowops-evidence-fetch","version":"v1","objective":"fetch normalized public text with HTTP metadata and content hashes"}
```

Its Keccak-256 task digest is
`0x184434aa1baa61d4153b1cc7e9b3096e75c35502d4752de595f81ac7c38dbb70`.

The exact Evidence Fetch request JSON was:

```json
{"url":"https://www.rfc-editor.org/rfc/rfc2606.txt","mode":"text","maxOutputBytes":4096,"requestDigest":"0x184434aa1baa61d4153b1cc7e9b3096e75c35502d4752de595f81ac7c38dbb70"}
```

Its Keccak-256 request digest is
`0xdbc0774d4b72eb63492d3fb6cc912dbf4b3dbf063ffb665c82287a88eb88f9eb`.

## Delivery evidence

The provider ran Evidence Fetch only after the acknowledgement was canonical.
At `2026-08-14T06:52:05.729424Z`, the RFC Editor returned HTTP 200,
`text/plain`, and 8,008 source bytes. The returned text was bounded to 4,096
bytes and marked truncated; the full normalized-content digest still covers
the entire normalized response.

- Fetch-parameter digest: `0x6aa249e1a625b0a7b04dec57039c5422da7705d459eaecee751ca43e18910e3c`
- Raw-source SHA-256: `0xb6869c8984701701bc2e6973b6ffc750d497f845cc1a65a106e9301590a13ab0`
- Full normalized-content SHA-256 and onchain response digest: `0xda77a0046da4e3f2275fb7012bccf03cb608252bd5b36750029cb364b019a3e0`
- Exact response-JSON SHA-256 and onchain evidence digest: `0x6b9a81e9e690e497e5934cb5388c4fb9b49345e56d7d6eb9ab54eedac63762af`

The exact response is committed as
`docs/evidence/call-escrow-evidence-fetch-2026-08-14.response.json`. Git stores
one final line feed; reproduce the onchain evidence digest over the original
HTTP response bytes with:

```sh
tr -d '\n' < docs/evidence/call-escrow-evidence-fetch-2026-08-14.response.json \
  | shasum -a 256
jq -r .contentSha256 \
  docs/evidence/call-escrow-evidence-fetch-2026-08-14.response.json
```

The expected results are the evidence and response digests above.

## Canonical transitions

| Action | Transaction | Block | Canonical block hash | UTC |
| --- | --- | ---: | --- | --- |
| Fund | `0x7eae95435827de08f7f28204a035fb34af540a7d8c91114f473f0050569cf153` | 45460894 | `0x3ac4b2cb734f55d304f915abc9d435bb142dd2e5df1a5167b50228ca076466ec` | `2026-08-14T06:47:56Z` |
| Acknowledge | `0xd3b2391645c78f5c3169450f0516d949e38a94d5d3236f777fc70f11bf3c151d` | 45460989 | `0x01aa47d446ed00bd34c580356d391c88129c69532da7bb00be7fc7437a3b65c7` | `2026-08-14T06:51:06Z` |
| Deliver | `0x51104dffadec798e676d53bf264fdaaf673e20d192075ed8b167cfab75c84310` | 45461109 | `0x6acf53a9f5268d34f067956137b2413cb805e33b8b8999089650591cb3a7bcbe` | `2026-08-14T06:55:06Z` |
| Release | `0x421489889b2f7587382eba972302de5ab964e38bbd9c0c75ba40b1feafb4974e` | 45461194 | `0xe4e49c2a7b0c39d13637479358341294cbcc86aa78553dff2930512e0816fc93` | `2026-08-14T06:57:56Z` |

The exact 0.1 test-USDC approval was transaction
`0xf8d7eb02a40c3a4a25b4cdd86e4c962c564ef6e8880bdf67763409e592462615`
at block 45460764. Funding consumed the allowance completely; both observers
reported an allowance of zero afterward.

## Reproduction

The strict lifecycle manifest is
`docs/evidence/call-escrow-evidence-fetch-2026-08-14.lifecycle.json`. Verify it
without a signing key:

```sh
go run ./cmd/escrow-conformance \
  -manifest docs/evidence/call-escrow-evidence-fetch-2026-08-14.lifecycle.json \
  -rpc base=https://sepolia.base.org \
  -rpc publicnode=https://base-sepolia-rpc.publicnode.com
```

On 2026-08-14, both providers agreed on every transaction hash, block number,
block hash, event field, actor, amount, digest, and ordering. The verifier
returned `ready: true` with final action `RELEASE` at a twelve-confirmation
minimum.

At observed head 45461217, both providers also reported terminal state
`Released`, `totalLocked = 0`, escrow test-USDC balance `0`, buyer balance
`19900000`, and provider balance `100000` atomic units.

## Boundaries that remain

- This proves one successful Base Sepolia lifecycle, not Base mainnet safety.
- The contracts remain unaudited; mainnet use is still prohibited.
- Evidence Fetch proves delivered bytes and metadata, not truth or quality.
- The public x402/payment-gated Evidence Fetch route is not yet enabled.
- The forced-expiry refund lifecycle remains a separate required live test.
