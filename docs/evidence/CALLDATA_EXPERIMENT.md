# Base Sepolia x402 Builder Code Calldata Experiment

Status: **UNRESOLVED — prepared, no payment sent**  
Recorded: 2026-08-11

## Question

Does the selected hosted facilitator preserve the FlowOps client service code `s` and append an ERC-8021 Schema 2 suffix to the Base Sepolia settlement transaction?

## Selected test path

| Field | Value |
|---|---|
| Network | Base Sepolia, CAIP-2 `eip155:84532`, chain ID `84532` |
| Asset | Test USDC `0x036CbD53842c5426634e7929541eC2318f3dCF7e`, 6 decimals, EIP-3009 |
| Facilitator | `https://x402.org/facilitator` |
| Advertised EVM signer | `0xd407e409E34E0b9afb99EcCeb609bDbcD5e7f1bf` |
| Protocol | x402 V2 `exact` |
| SDK reference | x402 commit `1d15062628b086b497ca10bb9b4c675a528c864e` |
| Minimum test amount | `1000` atomic units = `0.001000` test USDC |

## Evidence collected without moving funds

1. The current x402 documentation defines Builder Code as a server/client/facilitator extension and says the facilitator appends the Schema 2 suffix at settlement.
2. The pinned x402 Go implementation passed its full test suite, including the builder-code encoder/parser and EVM settlement plumbing.
3. A live `GET https://x402.org/facilitator/supported` on 2026-08-11 returned:
   - x402 V2 `exact` on `eip155:84532`;
   - extension list containing `builder-code`; and
   - EVM signer `0xd407e409E34E0b9afb99EcCeb609bDbcD5e7f1bf`.
4. The endpoint exposed no semantic deployment version in the response or headers observed, so the deployed facilitator version is currently **opaque**.
5. The latest 2,000 outgoing Base Sepolia transactions from that signer were scanned through Blockscout. None ended in marker `80218021802180218021802180218021`.

The transaction scan is context, not a negative result. A suffix is only expected when the settlement payload carries Builder Code data or the facilitator supplies its own configured code.

## Required live fixture

The resource server must declare:

```json
{
  "extensions": {
    "builder-code": {
      "info": {
        "a": "<FLOWOPS_OR_EVIDENCE_FETCH_APP_CODE>"
      }
    }
  }
}
```

The client must echo `a` and add:

```json
{
  "extensions": {
    "builder-code": {
      "info": {
        "a": "<FLOWOPS_OR_EVIDENCE_FETCH_APP_CODE>",
        "s": ["<FLOWOPS_CLIENT_SERVICE_CODE>"]
      }
    }
  }
}
```

The settlement transaction must be read directly from Base Sepolia and its complete calldata retained.

## Acceptance procedure

1. Confirm the chosen signer address, current test-USDC balance, payee, amount, and Builder Codes.
2. Obtain explicit confirmation for the test payment.
3. Start an x402 V2 resource server using the pinned/current release and declare app code `a`.
4. Pay with a client registering FlowOps service code `s`.
5. Record the resource response and settlement transaction hash.
6. Fetch the canonical transaction from two Base Sepolia RPC providers.
7. Confirm the transaction sender is the advertised/returned facilitator signer, not the FlowOps client.
8. Preserve the entire input and verify the final 16 bytes equal `80218021802180218021802180218021`.
9. Read the preceding schema byte and require `0x02`.
10. Parse the two-byte CBOR length and decode the CBOR map using the x402 reference parser.
11. Require exact app code `a` and FlowOps code within `s`; record `w` as present/absent and its value.
12. Cross-check the USDC `Transfer` event, payer, payee, and amount.

## Classification rules

| Result | Classification |
|---|---|
| Canonical tx contains marker/schema and expected `a` plus FlowOps `s` | `VERIFIED_SUPPORTED` |
| Canonical settlement succeeds, the payment payload demonstrably carried valid `a`/`s`, but the suffix or FlowOps `s` is absent after retrying a supported configuration | `VERIFIED_UNSUPPORTED` for that facilitator deployment/configuration |
| No funded signer, no registered codes, payment not confirmed, no canonical transaction, or facilitator version cannot be tied to the test | `UNRESOLVED` |

## Current blocker record

The workspace has no configured `EVM_PRIVATE_KEY`, `CDP_API_KEY_ID`, `CDP_API_KEY_SECRET`, `BASE_SEPOLIA_RPC_URL`, `FACILITATOR_URL`, `FLOWOPS_BUILDER_CODE`, or `FLOWOPS_REFERENCE_SIGNER_ADDRESS`. No secret values were inspected. More importantly, no FlowOps identity or reference signer has been designated, and no payment confirmation has been given for a concrete wallet/payee/amount.

Therefore the correct current result is:

```text
classification: UNRESOLVED
reason: identity, funded signer, concrete codes, and confirmed test payment are absent
protocol capability: verified in docs and pinned reference tests
selected facilitator advertisement: builder-code supported
selected facilitator transaction-level conformance: not yet tested
```

## Evidence fields to append after settlement

```text
timestamp_utc:
resource_url:
facilitator_url: https://x402.org/facilitator
facilitator_deployment_version_or_fingerprint:
sdk_package_and_version:
sdk_commit: 1d15062628b086b497ca10bb9b4c675a528c864e
network: eip155:84532
asset: 0x036CbD53842c5426634e7929541eC2318f3dCF7e
payer:
payee:
amount_atomic: 1000
declared_a:
declared_s:
transaction_hash:
transaction_sender:
block_number:
complete_calldata:
erc8021_marker_present:
schema_id:
decoded_a:
decoded_s:
decoded_w:
usdc_transfer_verified:
classification:
notes:
```
