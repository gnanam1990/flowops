# AC-9 Structural Isolation Evidence

Status: deterministic local evidence implemented; production release evidence remains governed by the consolidated acceptance manifest.

`internal/architecturedeps` parses every production Go import and applies transitive reachability rules. Test-only imports are excluded so integration tests can assemble isolated components without granting those capabilities to production binaries.

The executable policy proves:

- MCP and agent application packages cannot reach approval decisions, spend authorization, bearer storage, signer runtime, keeper relay, seller egress, or Owner API packages;
- the verifier executable cannot reach intent creation, spend signing, seller egress, or relay packages;
- the control plane, signer runtime, seller worker, and verifier runtime cannot import the keeper relay, legacy wallet/broadcast packages, or direct go-ethereum RPC clients;
- agent intake, authorization revalidation, and seller egress converge on the shared `purchasespec` JCS/URL implementation, while directory reads converge on `directoryproof` Merkle verification; and
- no production command or internal package embeds `eth_sendRawTransaction` outside the keeper relay boundary.

`TestMetaTestCatchesPlantedDirectAndTransitiveViolations` injects direct and indirect forbidden edges plus a shared-library bypass and requires all three to fail with the complete dependency path.

Run:

```text
go test ./internal/architecturedeps
```

This evidence establishes compile-time reachability, not deployment-network policy. Process credentials, Unix-socket permissions, egress policy, and production topology remain separate release gates.
