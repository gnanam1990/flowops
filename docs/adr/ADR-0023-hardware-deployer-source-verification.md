# ADR-0023: Hardware Mainnet Deployer and Source Verification Ceremony

Status: Accepted for implementation; production identity and deployment remain blocked
Date: 2026-08-14

## Context

The Base mainnet Solidity deployment script already refuses unless a designated
deployer, external-review digest, and broadcast flag are changed in code. That
structural stop does not by itself define how a future operator proves key
ownership, limits gas exposure, verifies the exact creation transaction, or
publishes source without accidentally turning a rehearsal into a mainnet write.

The Sepolia MetaMask accounts are test identities and must not become production
deployers. A raw private key, Foundry keystore, or RPC credential in process
arguments would also weaken the intended operating boundary.

## Decision

FlowOps records source rehearsal and promotion authorization separately.

The source rehearsal binds the exact source and Foundry configuration hashes,
dependency gitlinks, compiler and optimizer settings, EVM target, constructor
encoding, creation and deployed-template bytecode hashes, and a canonical hash
of Foundry's standard JSON compiler input. It is generated locally and does not
contact Base or submit anything to an explorer.

The promotion record starts `blocked-unassigned`. A later reviewed PR must name
a new Ledger or Trezor address and derivation path, ownership-attestation hash,
approved recovery runbook, exact reviewed commit, external-review digest,
source-verification approval, and fresh time-bounded broadcast approval. The
approval also caps gas limit, maximum fee, priority fee, and total gas spend.
It pins the expected deployer nonce.

The supported broadcast wrapper:

- rejects software wallets, raw keys, and the two Sepolia identities;
- requires a clean checkout at the exact reviewed commit;
- rechecks the in-code deployer, review digest, and broadcast flag;
- requires admitted production RPC quorum and the read-only mainnet preflight;
- simulates first with Foundry's 130 percent gas-estimate multiplier and keeps
  the emitted total gas estimate within the approved ceiling; and
- requires both providers to agree on the approved latest and pending nonce
  before and after simulation; and
- burns the approval into an atomic local attempt seal before one
  hardware-confirmed broadcast, with no automatic retry or resume path.

After canonical confirmation, a read-only verifier compares the exact creation
input, sender, receipt, deployed address, runtime hash, asset getter, and release
window through both admitted providers. Explorer submission is a distinct
command with its own explicit approval environment variable and secret-managed
API credential.

## Consequences

- The repository now contains a complete, reviewable ceremony shape while still
  having no production key, approval, deployment, or mainnet fund movement.
- A promotion cannot quietly reuse a Sepolia hot wallet or hide material gas
  limits outside reviewable data.
- Unknown broadcast outcomes remain quarantined; the wrapper provides no blind
  retry or resume path.
- The deployed bytecode template cannot prove immutable values by itself, so
  post-deployment evidence must bind the full creation input and live runtime.
- Explorer verification is public evidence, not a substitute for bytecode,
  receipt, or independent security review.

## Remaining gate

Before any mainnet transaction, FlowOps still needs an independent contract
review, legal approval, a real hardware-backed deployer and recovery ceremony,
two selected paid independent RPC providers, a completed funded Sepolia signer
proof, a measured deployment-confirmation depth, and a separate explicit
zero-fund broadcast approval. None is inferred from this ADR.
