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

As of 2026-08-27, the deployed Safe has the ASCP spend module enabled, the
exact escrow runtime allowlisted, `20.000000` Base Sepolia test USDC, and
`0.0001` Base Sepolia test ETH. Both escrow and module allowances remain zero;
funding alone does not authorize a spend. A separately reviewed capped
allowance ceremony is still required before a spend/refund criterion runs.

The isolated Railway acceptance control plane and PostgreSQL service are live.
The runtime uses `sslmode=verify-full`, TLS 1.3, the least-privilege
`flowops_runtime` role, and passed the managed PostgreSQL readiness inventory.
That environment still contains only the control plane and PostgreSQL. A
production-equivalent run additionally requires independently supervised
directory/registry operators, seller, signer/HSM, verifier, keeper/gas payer,
governance observer, two WORM replicas, recovery service, and controlled fault
infrastructure. A healthy control-plane loop is not evidence that those
dependencies exist.

## Local rehearsal

The same defect classes can be exercised locally without claiming external
acceptance. Configure an isolated PostgreSQL database and run:

```sh
FLOWOPS_TEST_DATABASE_URL='<isolated PostgreSQL URL>' \
  make ascp-local-acceptance-rehearsal
```

`FLOWOPS_LOCAL_ACCEPTANCE_OUTPUT_DIR` may name an absolute private output
directory. The runner executes fresh race-enabled PostgreSQL integration and
scenario suites, the Solidity state machines, the external assertion inventory,
and read-only two-provider Base Sepolia activation observations. It emits a
hash-bound `report.json` classified `LOCAL_REHEARSAL_ONLY`; every one of the 14
rows remains `STILL_REQUIRED`. The report cannot be supplied to the external
verifier, cannot collect owner signatures, and cannot promote the manifest.
