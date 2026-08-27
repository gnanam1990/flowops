# ASCP external acceptance ceremony

Status: **gate implemented; the 14 external criteria are not yet accepted**.

The repository deliberately does not promote AC-1, AC-2, AC-5, AC-12, AC-19,
AC-24, AC-31, AC-33, AC-37, AC-46, AC-47, AC-53, AC-68, or AC-84 from a local
test result. Their release evidence must come from the production-equivalent
Base Sepolia environment, controlled failure infrastructure, operator drills,
and the existing 2-of-3 Safe ceremony.

`deployments/base-sepolia-ascp-external-acceptance-profile-v1.json` pins the
target source commit, deployed ASCP record and SHA-256 digest, Base Sepolia
chain, Safe, owner set, threshold, provider quorum, evidence lifetime, and exact
criterion inventory. Changing any of those inputs creates a new reviewed
profile; operators must not edit a completed bundle to accommodate drift.

## Fail-closed evidence contract

A completed bundle must contain:

- every required assertion for all 14 criteria, with no missing, renamed, or
  failed assertion;
- a hash-verified event-chain export and run manifest;
- raw operator, RPC, Safe, chaos, signer-recovery, and replica evidence as
  applicable;
- observations made during the run through at least two distinct HTTPS RPC
  provider hosts;
- the exact pinned deployment record and source commit; and
- valid EIP-191 signatures from at least two distinct owners in the pinned
  2-of-3 Safe owner set.

Artifact paths must remain below the supplied evidence root. Symlink escape,
path traversal, digest substitution, stale completion, same-provider quorum,
assertion mutation, untrusted signers, duplicate signers, and post-signature
bundle mutation all fail verification.

The verifier never receives a wallet private key. It prints one exact signing
message; each owner signs that visible message in their own wallet after
independently checking the artifacts.

## Operator sequence

1. Print the exact assertion inventory:

   ```sh
   go run ./cmd/ascp-external-acceptance requirements
   ```

2. Create a non-claiming template. It has no artifacts, completion time, passed
   assertions, provider observations, or signatures:

   ```sh
   go run ./cmd/ascp-external-acceptance template \
     deployments/base-sepolia-ascp-external-acceptance-profile-v1.json \
     <reviewed-run-id> > /secure/external-acceptance/bundle.json
   ```

3. Run the scenarios in a production-equivalent environment. Preserve raw
   receipts, event exports, journal/trial-balance output, Safe records, process
   termination evidence, controlled reorg/fault-injection records, signer
   recovery records, and replica-failure records. Hash every artifact and fill
   the bundle only from observed results. A failed scenario stays failed.
4. Set `completedAt` only after all scenarios finish and record two independent
   RPC observations made within the run window.
5. Print the immutable signing message:

   ```sh
   go run ./cmd/ascp-external-acceptance digest /secure/external-acceptance/bundle.json
   ```

6. Obtain two owner signatures over that exact message, append them to the
   bundle, and verify:

   ```sh
   FLOWOPS_EXTERNAL_ACCEPTANCE_BUNDLE=/secure/external-acceptance/bundle.json \
   FLOWOPS_EXTERNAL_ACCEPTANCE_EVIDENCE_ROOT=/secure/external-acceptance \
     make verify-ascp-external-acceptance
   ```

7. Commit the immutable, credential-free evidence and only then change the 14
   manifest rows to `accepted`. Never commit wallet material, database URLs,
   RPC credentials, session cookies, or mutable provider-console links.

## Current external prerequisites

The deployed Safe currently has the ASCP spend module enabled and the exact
escrow runtime allowlisted, but it has zero native balance and zero test USDC.
No spend/refund criterion can be run until a separately reviewed capped testnet
funding and allowance ceremony succeeds.

The existing Railway production control plane and PostgreSQL services are live,
but the configured runtime database URL does not declare
`sslmode=verify-full`. The managed PostgreSQL verifier therefore fails before
connecting. A production-equivalent run also requires the latest reviewed
source image and the complete ASCP directory, registry, escrow, spend-module,
governance-observer, signer, verifier, keeper, WORM, and recovery configuration;
the currently deployed service must not be treated as that environment merely
because its health loop is running.
