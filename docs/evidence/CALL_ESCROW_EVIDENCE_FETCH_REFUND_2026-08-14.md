# CallEscrow Evidence Fetch failed-delivery refund — 2026-08-14

Status: complete on Base Sepolia; Base mainnet remains prohibited

This evidence records one real FlowOps Evidence Fetch call funded with 0.1 test
USDC and acknowledged by its provider. The requested public resource returned
HTTP 404, so the provider produced no delivery digests and submitted no
delivery transaction. After both independent Base observers confirmed chain
time strictly beyond the delivery deadline, the provider triggered the
permissionless refund. The contract returned the full amount only to the
snapshotted buyer.

## Immutable call snapshot

- Network: Base Sepolia, chain ID `84532`
- CallEscrow: `0x86e145397f58e71c134c0e054320db929483227a`
- Asset: test USDC at `0x036cbd53842c5426634e7929541ec2318f3dcf7e`
- Buyer: `0x079bdde909e28e437768a06d7001eb40896668d4`
- Provider: `0xc2f0967c4df966636e4ac1dad40abda65536cbb6`
- Amount: `100000` atomic units, or 0.1 test USDC
- Call ID: `0xd14f1a03a30bded1258ada9164c6e5cb26b2d984a3030803da89e27414507a35`
- Acknowledge by: `1786705214` (`2026-08-14T11:00:14Z`)
- Deliver by: `1786705334` (`2026-08-14T11:02:14Z`)
- Optimistic release window: 3,600 seconds

The exact task JSON was:

```json
{"service":"flowops-evidence-fetch","version":"v1","objective":"attempt a known-missing public text resource after provider acknowledgement and refuse delivery on HTTP failure","scenario":"acknowledged-refund-v2"}
```

Its Keccak-256 task digest is
`0xffa85591ba50c1bda5da3ff1285d30fa2de146415eea686d3a588893ded0342d`.

The exact Evidence Fetch request JSON was:

```json
{"url":"https://www.rfc-editor.org/rfc/rfc999999.txt","mode":"text","maxOutputBytes":4096,"requestDigest":"0xffa85591ba50c1bda5da3ff1285d30fa2de146415eea686d3a588893ded0342d"}
```

Its Keccak-256 request digest is
`0xfd554688c524c5cae7b7801cb40ff24f53a9f9a95abd86ba5cad2a2b921ef4cb`.

## Failed delivery evidence

The provider ran Evidence Fetch only after the acknowledgement was canonical.
At `2026-08-14T10:32:26Z`, the local handler returned HTTP 502 with error code
`UPSTREAM_FAILURE` and message `upstream returned HTTP 404`.

The exact request and failure are committed in
`docs/evidence/call-escrow-evidence-fetch-refund-2026-08-14.failure.json`.
The artifact explicitly records `deliverable: false` and
`deliverySubmitted: false`. Both observers confirmed that the call remained
`Acknowledged`, with `deliveredAt = 0` and zero response and evidence digests.
No database or onchain delivery was invented for the failed fetch.

## Canonical transitions

| Action | Transaction | Block | Canonical block hash | UTC |
| --- | --- | ---: | --- | --- |
| Fund | `0xd2504fef1deb361f762391ecbdbb453fee1c0612103a5bdd1d32b597239b016e` | 45467579 | `0x7b9cb8f10a16ccee2fa8ac540b32490a834bf086c970dbef35bb32daf105a286` | `2026-08-14T10:30:46Z` |
| Acknowledge | `0x823e37fc5497dcd2ca16f7e583c52aa12b5249e8dd25c6e85929ccfcc0f9acff` | 45467605 | `0xf33edae17b98c3e02e0f0b48b5c3339d3af54db5e533727f41f84b320f05e02c` | `2026-08-14T10:31:38Z` |
| Refund | `0xda5e683c94c417ab520a6114aee3e322b0209d4c1507fd2b86a6b9f65ea397a2` | 45469139 | `0x045d0d5c8feaa11e69a2f41e8a969c1310712f75f939010d7df48a3ee1a3f2be` | `2026-08-14T11:22:46Z` |

The exact 0.1 test-USDC approval was transaction
`0x5769020cfb5d4ad67158416e414c7daad9496ad8157519a3a16eac935e46cf6e`
at block 45467543. Funding consumed the allowance completely.

The refund transaction was sent by the provider, not the buyer. Its USDC
`Transfer` still paid exactly `100000` atomic units from escrow to the buyer,
and its `Refunded` event reported `expiredFrom = 2` (`Acknowledged`). This
proves a permissionless caller cannot choose or divert the refund recipient.

## Reproduction and conservation

The strict lifecycle manifest is
`docs/evidence/call-escrow-evidence-fetch-refund-2026-08-14.lifecycle.json`.
Verify it without a signing key:

```sh
go run ./cmd/escrow-conformance \
  -manifest docs/evidence/call-escrow-evidence-fetch-refund-2026-08-14.lifecycle.json \
  -rpc base=https://sepolia.base.org \
  -rpc publicnode=https://base-sepolia-rpc.publicnode.com
```

At observed head 45469170, both providers reported terminal state `Refunded`,
`totalLocked = 0`, escrow test-USDC balance `0`, buyer balance `19900000`,
provider balance `100000`, and buyer-to-escrow allowance `0` atomic units. The
refund receipt already had 31 confirmations; the manifest requires twelve.

## Boundaries that remain

- This proves one failed-delivery refund on Base Sepolia, not Base mainnet
  safety.
- The contracts remain unaudited; mainnet use is still prohibited.
- Evidence Fetch proves delivered bytes and metadata, not truth or quality.
- The public x402/payment-gated Evidence Fetch route is not yet enabled.
- Escrow transition reconciliation remains a read-only conformance command and
  is not yet wired into the durable production journal or reorg worker.
