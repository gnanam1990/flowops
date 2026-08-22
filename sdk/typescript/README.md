# ASCP v4 typed-data SDK

This dependency-free TypeScript package implements the six normative ASCP v4 EIP-712 messages pinned by `schemas/ascp-typed-data-v4.registry.json`.

It exports exact type definitions plus `encodedData`, `structHash`, `domainSeparator`, `digest`, and `canonicalJSON`. Inputs are fail-closed: fields must match the registry exactly, addresses and fixed bytes must be lowercase exact-width hex, integers must be canonical and width-bounded, and the domain must be `{name: "ASCP", version: "4"}`.

Run `npm run typecheck` and `npm test` with Node 22.18 or newer. Tests recompute every schema/vector artifact without a runtime crypto dependency.
