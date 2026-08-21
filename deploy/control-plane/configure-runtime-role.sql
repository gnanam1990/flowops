\set ON_ERROR_STOP on
\if :{?runtime_role}
\else
\echo 'runtime_role psql variable is required'
\quit 2
\endif

BEGIN;

ALTER ROLE :"runtime_role"
    NOSUPERUSER NOCREATEROLE NOCREATEDB NOREPLICATION NOBYPASSRLS;

REVOKE CREATE ON SCHEMA public FROM PUBLIC;
REVOKE CREATE ON SCHEMA public FROM :"runtime_role";
GRANT USAGE ON SCHEMA public TO :"runtime_role";

REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA public FROM :"runtime_role";

GRANT SELECT, UPDATE ON organizations, agents TO :"runtime_role";
GRANT SELECT ON credentials, policies, sites_identity_providers,
    sites_memberships, flowops_schema_migrations TO :"runtime_role";
GRANT SELECT, INSERT, UPDATE ON commands TO :"runtime_role";
GRANT SELECT, INSERT ON audit_events, control_events TO :"runtime_role";
GRANT SELECT, INSERT ON ascp_intents, ascp_policy_decisions, ascp_execution_authorizations,
    ascp_budget_reservation_dimensions, ascp_directory_snapshots,
    ascp_directory_quote_evidence TO :"runtime_role";
GRANT SELECT, INSERT, UPDATE ON ascp_approvals, ascp_budget_reservations,
    ascp_directory_heads TO :"runtime_role";
GRANT SELECT, INSERT ON ascp_bearer_handles, ascp_sign_requests,
    ascp_bearer_registry, ascp_signer_outbox TO :"runtime_role";
GRANT SELECT, INSERT ON ascp_payment_operations, ascp_payment_attempts,
    ascp_chain_observations, ascp_ledger_transactions, ascp_ledger_postings TO :"runtime_role";
GRANT SELECT, INSERT ON ascp_keeper_jobs TO :"runtime_role";
GRANT SELECT, INSERT ON ascp_events TO :"runtime_role";
GRANT SELECT ON ascp_event_checkpoints TO :"runtime_role";
GRANT UPDATE (state) ON ascp_bearer_handles TO :"runtime_role";
GRANT UPDATE (prepared_handle, state, prepared_at, activated_at,
    primary_mirror_digest, mirrored_at, acknowledged_at)
    ON ascp_sign_requests TO :"runtime_role";
GRANT UPDATE (primary_mirror_digest, outcome)
    ON ascp_bearer_registry TO :"runtime_role";
GRANT UPDATE (state, attempts, delivered_at)
    ON ascp_signer_outbox TO :"runtime_role";
GRANT UPDATE (state, locked_transaction_hash, locked_block_number, locked_block_hash,
    terminal_action, terminal_transaction_hash, terminal_block_number, terminal_block_hash, updated_at)
    ON ascp_payment_operations TO :"runtime_role";
GRANT UPDATE (state, resolved_at, block_number, block_hash, evidence_digest, canonical_checked_at)
    ON ascp_payment_attempts TO :"runtime_role";

COMMIT;
