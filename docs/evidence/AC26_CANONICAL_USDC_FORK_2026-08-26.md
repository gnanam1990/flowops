# AC-26 canonical Base USDC failure and recovery evidence

Date: 2026-08-26
Acceptance identity SHA-256: `58377488c19e2dbe96498e3b61b58048aa236c588620b3810873640fdca3b3f9`

## Evidence boundary

The test fork is pinned to Base mainnet block `50,482,467` and canonical USDC
`0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913`. The upstream RPC was used only
for historical reads. Circle role calls, token balance seeding, contract
deployments, and escrow transactions occurred only in the local Foundry VM.
A deterministic local-only test key signed the verifier attestation. No real
operator key, external signature, transaction submission, or Base broadcast
was used.

At the pinned block the contract reported pauser
`0xD3571B3bc51CECFf49194AD67aFFFC648d5e07b4`, blacklister
`0x1f2e3A640175d20ac31ed523B6733B977173E277`, and `paused() == false`.

## Executed proofs

The canonical-USDC fork suite passed all four cases:

- pause blocks lock and rolls back call state, `totalLocked`, escrow balance,
  and buyer balance; unpause permits the exact lock;
- buyer blacklist blocks lock with the same atomic rollback; removal permits
  the exact lock;
- payout blacklist rolls back release state, locked accounting, payout, and
  verdict-nonce consumption; removal permits one release with the same signed
  attestation; and
- buyer blacklist rolls back expired refund state and accounting; removal
  restores the exact buyer balance.

The PostgreSQL integration proof passed `LOCK`, `RELEASE`, and `REFUND`. A
failed lock preserves `AUTHORIZATION_LIVE` and posts no lock ledger entry. A
failed terminal transfer preserves `COMMITTED_FINALIZED`, preserves the
existing lock ledger, and appends no false terminal posting. Retry fails before
a fresh clean observation in the current asset-health epoch and fails when the
release evidence hash changes. After clean evidence, retry registration
preserves the reverted attempt row and accepts one new transaction hash.
Independent canonical receipt evidence is still required before exactly one
balanced lock/release/refund entry and the correct reservation state are
recorded.

Commands executed:

```text
BASE_MAINNET_FORK_RPC_URL=<read-only Base RPC> forge test --match-path contracts/test/CanonicalUSDCFork.t.sol -vv
FLOWOPS_TEST_DATABASE_URL=<local PostgreSQL> go test -race ./internal/ascpsettlement -run '^TestPostgresStoreRetriesExactReverted(Lock|Transfer)OnlyDuringFreshAssetRecovery$' -count=1 -v
```

Result: 4 fork tests passed; 3 PostgreSQL recovery cases passed.

## Remaining external evidence

This is local executable evidence, not release-stage acceptance. A production
run still requires reviewed paid RPCs, the signed deployment manifest, deployed
contract tuples, managed PostgreSQL evidence, independent operators, and the
authorized mainnet ceremony. A real Circle-controlled pause or blacklist was
not requested and must not be manufactured for testing.
