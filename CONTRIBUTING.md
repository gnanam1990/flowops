# Contributing

FlowOps is currently a private pre-alpha repository.

## Change flow

1. Start from the latest `main`.
2. Keep a change limited to one module or coherent migration.
3. Add positive and negative tests with the implementation.
4. Run `make check`.
5. Commit with a conventional commit message.
6. Open a pull request explaining the invariant affected, failure states, and evidence produced.

Security and money-movement changes must explain replay, expiry, substitution, freeze, restart, ambiguous-broadcast, and chain-halt behavior.
