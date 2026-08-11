# Security Policy

FlowOps is pre-alpha and is not approved for mainnet funds.

## Reporting

Report vulnerabilities privately through GitHub's private vulnerability reporting for this repository. Do not open a public issue containing an exploit, credential, private key, or customer data.

## Non-negotiable boundaries

- Never send customer private keys, seed phrases, or raw signing credentials to FlowOps.
- Treat every agent, tool result, provider response, webhook, RPC, and indexer as untrusted input.
- Treat a transaction hash as an identifier, not proof of canonical settlement.
- Stop and quarantine on ambiguous broadcasts or conflicting chain observations.

## Mainnet gate

Mainnet remains blocked until threat modeling, external contract review, signer review, dependency review, incident runbooks, and the PRD's Phase 0/1 exit gates are complete.
