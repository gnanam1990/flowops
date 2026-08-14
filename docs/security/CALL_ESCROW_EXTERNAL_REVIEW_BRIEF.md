# CallEscrow External Review Brief

## Review request

Perform an independent security review of the ownerless `CallEscrow` contract
and its Base mainnet zero-fund deployment ceremony. The exact machine-readable
scope and hashes are in `security/call-escrow/review-manifest.json`. The
contract is explicitly unaudited and mainnet deployment remains prohibited.

## In scope

- `contracts/src/CallEscrow.sol` at commit
  `808caa4c9905334c52d6f237863f5ff33b11ffb0`;
- its complete seven-file transitive OpenZeppelin import closure at the pinned
  dependency commit;
- Solidity `0.8.26`, optimizer 200, Cancun EVM, non-via-IR compilation;
- the pinned Base mainnet constructor and deployment script;
- the aggregate final-readiness audit, hardware-only one-shot broadcast
  wrapper, read-only post-deployment verification, and separately approved
  source submission; and
- unit, fuzz, invariant, negative-mutation, and source-rehearsal evidence.

Customer signer/wallets, policy services, durable reconciliation, production
RPC operations, provider implementation, frontend, and legal analysis are
explicitly out of scope. Findings that cross those boundaries should still be
reported as integration risks, but must not be described as reviewed modules.

## Questions reviewers must answer

1. Can any call be funded twice, replayed across buyer/chain/contract, or have
   any authority field substituted after funding?
2. Can callback or malicious-token behavior produce reentrancy, inconsistent
   state, inaccurate locked accounting, or a payout larger than the liability?
3. Are every inclusive/exclusive timestamp boundary and every reachable state
   transition correct, including Base halt and reorg implications?
4. Can a public finalizer redirect funds, change amount, or finalize the wrong
   lifecycle branch?
5. Does the log order support deterministic reconciliation without claiming a
   terminal state before the asset transfer?
6. What failures arise from Base native USDC proxy upgrades, freezing,
   blacklisting, return values, or non-standard future behavior?
7. Does the absence of owner, pause, upgrade, dispute, and rescue roles create
   unacceptable failure modes for the intended capped pilot?
8. Can forced token transfers, self-destruct-era balance changes, or accounting
   donations affect liabilities or make assets permanently inaccessible?
9. Do objective non-zero response/evidence digests create any misleading
   guarantee beyond byte commitment and delivery timing?
10. Does the mainnet ceremony prevent source substitution, wrong deployer,
    wrong nonce, gas-cap bypass, record substitution, blind retry, secret
    leakage, and premature source submission?
11. Does the aggregate audit bind every canonical record and remain impossible
    to bypass through a record-path override from the hardware wrapper?

## Required deliverables

- reviewer organization and named reviewer(s);
- review start/end dates and exact reviewed commit;
- methods and tools used, including versions;
- finding list with severity, exploit scenario, affected lines, remediation,
  and disposition;
- explicit Critical and High finding counts after remediation;
- retest statement bound to the corrected commit;
- final report artifact and SHA-256 digest; and
- a limitations section that preserves every manifest known limitation or
  explains any challenged assumption.

The review is not accepted if it covers a floating branch, omits dependency or
compiler bindings, reports only automated scanner output, leaves unresolved
Critical/High findings, or lacks a retest of the final commit.

## Reproduction commands

```bash
make test-security-review-package
make test-mainnet-deployer-verification
forge test --match-path contracts/test/CallEscrow.t.sol --gas-report
forge test --match-path contracts/test/CallEscrow.invariant.t.sol -vv
make check
```

`make test-security-review-package` proves the package is internally
consistent and still incomplete. It is not a substitute for the requested
independent review.
