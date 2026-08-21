\set ON_ERROR_STOP on
\if :{?keeper_role}
\else
\echo 'keeper_role psql variable is required'
\quit 2
\endif

BEGIN;

ALTER ROLE :"keeper_role"
    NOSUPERUSER NOCREATEROLE NOCREATEDB NOREPLICATION NOBYPASSRLS;

REVOKE CREATE ON SCHEMA public FROM PUBLIC;
REVOKE CREATE ON SCHEMA public FROM :"keeper_role";
GRANT USAGE ON SCHEMA public TO :"keeper_role";

REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA public FROM :"keeper_role";

GRANT SELECT, INSERT ON ascp_keeper_jobs, ascp_keeper_nonce_sequences,
    ascp_keeper_tx_attempts TO :"keeper_role";
GRANT UPDATE (lease_owner, lease_token, lease_expires_at, nonce, state,
    attempt_count, current_attempt, last_error, updated_at)
    ON ascp_keeper_jobs TO :"keeper_role";
GRANT UPDATE (next_nonce, updated_at)
    ON ascp_keeper_nonce_sequences TO :"keeper_role";
GRANT UPDATE (state, broadcast_at, last_error, evidence_digest, observed_at)
    ON ascp_keeper_tx_attempts TO :"keeper_role";

COMMIT;
