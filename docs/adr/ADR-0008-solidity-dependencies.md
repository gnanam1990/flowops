# ADR-0008: Solidity Runtime and Test Dependencies

Status: Accepted for local and Base Sepolia development
Date: 2026-08-11

## Decision

FlowOps pins Solidity dependencies as Git submodules and compiles with Solidity 0.8.26 for the Cancun EVM target:

- OpenZeppelin Contracts commit `c64a1edb67b6e3f4a15cca8909c9482ad33a02b0` (`v5.4.0`) supplies `IERC20`, `SafeERC20`, `ERC20`, and `ReentrancyGuard`.
- forge-std commit `77041d2ce690e692d6e03cc812b57d1ddaa4d505` (`v1.9.7`) is test-only.
- CI pins `foundry-rs/foundry-toolchain` action commit `908c540300062bd5a7e473851cdb4282204cee09` and installs Foundry `v1.7.1`.

The deployed `CallEscrow` runtime therefore incorporates the selected OpenZeppelin library code. No proxy, registry, owner, pause, fee, resolver, or arbitrary rescue dependency is introduced.

## Trust assumptions

- Deployment configuration must bind `asset` to the independently verified native USDC contract for the selected Base network.
- The contract rejects fee-on-transfer funding but does not claim compatibility with rebasing, callback-bearing, or adversarial tokens.
- Git submodule pointers and the compiler version are part of the reviewed build input. Dependency upgrades require a dedicated commit, full Solidity checks, runtime comparison, and refreshed external review.
- forge-std and the Foundry action are build/test dependencies and are not deployed.

## Acceptance gate

CI must initialize submodules from a clean checkout, verify formatting, compile with size reporting, and pass unit, fuzz, invariant, reentrancy, and smoke tests. Mainnet remains blocked until source/runtime equivalence and external review are recorded.
