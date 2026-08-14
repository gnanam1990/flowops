# ADR-0018: Base Mainnet Promotion Gate

Status: Accepted for preparation; promotion remains blocked
Date: 2026-08-14

## Context

The Base Sepolia `CallEscrow` deployment and both terminal outcomes now have
live evidence, but that evidence does not establish mainnet safety. The
contract is ownerless and non-upgradeable, so a deployment mistake cannot be
repaired through an admin action. Mainnet USDC has financial value, and the
current repository has no completed external security review, designated
production deployer, or production observer quorum.

An environment variable or operator checklist alone is too weak a boundary for
the first mainnet deployment. The repository must remain unable to broadcast
until promotion evidence is reviewed as code.

## Decision

The committed Base mainnet deployment script has three independent structural
gates:

1. the designated deployer is the zero address;
2. the external-review digest is zero; and
3. mainnet broadcast is disabled.

`run()` rejects unless all three are changed. It also pins Base chain ID
`8453`, Circle's native Base USDC contract, and the one-hour optimistic release
window before opening a broadcast context.

A separate promotion PR must replace the zero deployer with a documented,
hardware-backed production signing identity, bind the SHA-256 digest of the
exact independent review artifact, and enable broadcast. That PR must also
update the mainnet readiness record from `blocked-no-deployment`; it cannot
claim a contract address or transaction before canonical onchain evidence
exists.

Public Base RPC observations are permitted for read-only preflight, but they
are explicitly marked ineligible for production quorum. Production requires at
least two operationally independent paid providers stored in the deployment
secret manager.

Pilot limits are not recorded until both the control plane and customer signer
enforce them. Documentation cannot turn an unenforced number into a safety
control. Funding, even after a zero-fund deployment, is a separate approval and
evidence ceremony.

## Consequences

- The current mainnet script is useful for compilation, negative tests, fork
  rehearsal, and review, but cannot broadcast.
- Promoting mainnet always produces a visible code diff that names the signing
  identity and review evidence.
- Reusing a Sepolia hot wallet is not an accepted shortcut.
- A zero-fund mainnet deployment is still a real state change and requires
  explicit human approval after every gate is complete.
- The ownerless contract cannot pause or rescue funds. A defect requires a new
  reviewed deployment and migration warnings.

## Acceptance gate

Before the promotion PR may set `MAINNET_BROADCAST_ENABLED` to `true`:

- an independent security review and legal review are complete;
- key ownership and recovery are documented for the designated deployer;
- escrow events are wired into durable intent registration and reorg
  correction;
- a funded reference-signer Sepolia proof is canonically reconciled;
- two independent production RPC providers and thresholds are approved;
- source verification and post-deployment bytecode comparison are rehearsed;
- the capped pilot limits are enforced independently by the control plane and
  customer signer; and
- a fresh, exact broadcast approval is recorded.
