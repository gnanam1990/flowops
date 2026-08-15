# FlowOps Base Mainnet Experimental Proposal Anchor v1

Status: proposed; no Base mainnet deployment exists

## Purpose

FlowOps may publish one evidence-only contract on Base mainnet so reviewers can
verify that a specific public proposal and source revision were anchored
onchain. The anchor is not the FlowOps payment product and is not evidence of
production readiness, users, transaction volume, audited safety, or approval by
Base, Coinbase, or any other third party.

## Permanent limitations

The proposal anchor:

- is permanently labelled `EXPERIMENTAL_UNAUDITED_NO_FUNDS`;
- is bound to Base mainnet chain ID `8453`;
- records only a proposal digest, a Git source commit, and its deployer;
- cannot create a vault, factory child, escrow, wallet, token, or signer;
- cannot accept a payment through a payable entry point;
- cannot pull, approve, transfer, release, refund, or withdraw ETH or tokens;
- has no owner, administrator, pause role, upgrade path, proxy, arbitrary call,
  delegatecall, or self-destruct path; and
- always reports `productionReady`, `acceptsFunds`, and
  `vaultCreationEnabled` as `false`.

Anyone can transfer an ERC-20 token directly to any address without the
recipient contract's cooperation. Users must not send ETH or tokens to the
proposal-anchor address. The anchor has no recovery method and the FlowOps UI
must never solicit an approval, deposit, payment, vault creation, or other
economic action against it.

## Public presentation

Before deployment, every public FlowOps surface must say that no Base mainnet
proposal anchor is deployed. After a canonical deployment is independently
verified, the surface may show its address and explorer link only with all of
the following adjacent warnings:

- experimental and unaudited;
- evidence-only proposal deployment;
- not approved for production;
- vault creation disabled;
- USDC deposits disabled; and
- do not send ETH or tokens.

The address must not be described as a live factory, vault, escrow, payment
contract, audited release, customer deployment, or evidence of traction.

## Separate production release

The existing `CallEscrow` mainnet promotion gate remains unchanged and blocked.
If the proposal is approved, FlowOps must complete the independent security and
legal reviews, production signer and RPC admission, source verification, and
fresh deployment authorization required by ADR-0018. Any production contract
must be deployed separately at a new address from the exact audited source. The
proposal anchor can never be promoted, upgraded, or configured into that
production release.

## Deployment ceremony

Deployment is a real Base mainnet state change and gas spend. A separate
promotion commit must bind the exact proposal digest, source commit, designated
proposal-only deployer, and a fresh human approval digest before enabling the
structurally blocked script. Because the anchor grants no authority after
deployment, this one-time signer may be a dedicated software EOA with only a
minimal gas balance, exact nonce and predicted-address checks, and a permanent
prohibition on production reuse. Production contracts still require the
hardware-backed deployment posture in ADR-0018. Simulation, code review, CI,
and explicit final human approval are required before a single broadcast. No
token approval, deposit, vault creation, or funding transaction is part of that
ceremony.
