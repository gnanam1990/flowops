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
GRANT SELECT, INSERT ON ascp_intents, ascp_execution_authorizations,
    ascp_budget_reservation_dimensions, ascp_directory_snapshots,
    ascp_directory_quote_evidence TO :"runtime_role";
GRANT SELECT, INSERT, UPDATE ON ascp_approvals, ascp_budget_reservations,
    ascp_bearer_handles, ascp_directory_heads TO :"runtime_role";

COMMIT;
