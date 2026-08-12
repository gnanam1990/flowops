# ADR-0017: Clef direct-USDC reference signer

Status: Accepted for unfunded conformance and Base Sepolia preflight  
Date: 2026-08-12

## Context

ADR-0016 defined the one-way execution state machine but deliberately left the
wallet boundary and installable process open. A useful Model D artifact must be
deployable by a design partner without asking FlowOps to receive a wallet key,
and it must independently prove that a wallet did not change the authority it
was asked to sign.

## Decision

The first concrete adapter targets the customer-run Clef external JSON-RPC API.
The reference signer sends an untrusted `account_signTransaction` request over
loopback HTTP. Clef owns its encrypted keystore, approval rules, and operator
interaction. The reference signer has no configuration field, environment
variable, flag, or API for a wallet private key.

For each `direct_usdc` authorization, the adapter:

1. confirms the primary RPC is on the configured Base chain;
2. simulates the exact ERC-20 `transfer(address,uint256)` call;
3. obtains the pending sender nonce, current priority fee, latest base fee, and
   gas estimate;
4. refuses fees or gas above customer-configured ceilings;
5. asks Clef to sign one EIP-1559 transaction; and
6. decodes the returned bytes with go-ethereum and independently verifies type,
   chain, recovered sender, nonce, USDC contract, recipient, amount, zero native
   value, calldata, gas, fee caps, and empty access list.

Only the validated bytes enter ADR-0016's durable `PREPARED` state. Broadcast
uses one `eth_sendRawTransaction` call with those exact bytes and performs no
internal retry. A transport failure or returned-hash mismatch is ambiguous.

The customer command is deliberately one-shot rather than an inbound service:

- `validate-config CONFIG` validates private files and trust boundaries without
  opening or creating journals;
- `execute CONFIG` reads one strict signed authorization from stdin;
- `resume CONFIG` advances only durable pending attempts; and
- `init-receipt-key PATH` creates a non-overwriting mode-`0600` attestation key
  and prints only its public key.

The sidecar re-reads a private customer freeze file at both verifier gates and
requires at least two distinct Base RPC origins to agree on a fresh canonical
anchor. This is the P0 halt-safe refusal posture. It is not the P1 continuously
running halt-history and automated recovery state machine.

## Dependency and trust boundary

`github.com/ethereum/go-ethereum/core/types` is used only for canonical EIP-1559
transaction decoding, hashing, and sender recovery. It does not receive a
private key or perform network calls. Clef is a separate customer-controlled
process. The primary Base RPC may return fee, nonce, simulation, and broadcast
data, but it cannot bypass the independent signed-byte checks or the
multi-provider pre-broadcast quorum.

The Clef HTTP endpoint must be loopback. In a container deployment, Clef and
the reference signer therefore need the same network namespace, such as
containers in one Kubernetes Pod. A remotely hosted signing service requires a
separate authenticated adapter and ADR.

## Consequences

- FlowOps still cannot operate or extract the wallet key.
- A customer can revoke FlowOps by removing its public trust key or freezing
  locally without FlowOps cooperation.
- The adapter deliberately supports one EOA, one Base chain, and one configured
  USDC contract per process.
- Clef rules and keystore backup remain customer responsibilities.
- Unfunded deterministic conformance is automated. A funded Base Sepolia smoke
  remains separately approved and capped; mainnet remains blocked.

## Acceptance evidence

- successful no-funds end-to-end execution and restart replay;
- recipient, asset, value, nonce, gas, fee, access-list, and signer mutation
  refusal before broadcast;
- simulation, wrong-chain, stale-head, observer-disagreement, unsafe URL,
  redirect, malformed JSON, oversized response, and returned-hash failure;
- freeze-file mutation, duplication, permission loosening, removal, and symlink
  refusal; and
- receipt and nonce keys/journals are private, process-locked where applicable,
  and never emitted by the command.
