\set ON_ERROR_STOP on
\if :{?asset_health_role}
\else
\echo 'asset_health_role psql variable is required'
\quit 2
\endif

SELECT COALESCE((SELECT rolcanlogin FROM pg_roles WHERE rolname = :'asset_health_role'), false) AS asset_health_role_can_login \gset
\if :asset_health_role_can_login
\else
\echo 'asset_health_role must exist and have LOGIN'
\quit 2
\endif

SELECT EXISTS (
    SELECT 1 FROM pg_auth_members
    WHERE member=(SELECT oid FROM pg_roles WHERE rolname=:'asset_health_role')
       OR roleid=(SELECT oid FROM pg_roles WHERE rolname=:'asset_health_role')
) AS asset_health_role_has_membership \gset
\if :asset_health_role_has_membership
\echo 'asset_health_role must not participate in role memberships'
\quit 2
\endif

SELECT EXISTS (
    SELECT 1 FROM pg_shdepend
    WHERE refclassid='pg_authid'::regclass
      AND refobjid=(SELECT oid FROM pg_roles WHERE rolname=:'asset_health_role')
      AND deptype='o'
) AS asset_health_role_owns_database_object \gset
\if :asset_health_role_owns_database_object
\echo 'asset_health_role must not own database objects'
\quit 2
\endif

BEGIN;

ALTER ROLE :"asset_health_role"
    NOSUPERUSER NOCREATEROLE NOCREATEDB NOREPLICATION NOBYPASSRLS NOINHERIT;
ALTER ROLE :"asset_health_role" SET search_path = public;
SELECT format('REVOKE TEMPORARY ON DATABASE %I FROM PUBLIC', current_database()) \gexec
SELECT format('REVOKE TEMPORARY ON DATABASE %I FROM %I', current_database(), :'asset_health_role') \gexec
REVOKE CREATE ON SCHEMA public FROM :"asset_health_role";
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
GRANT USAGE ON SCHEMA public TO :"asset_health_role";
REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA public FROM PUBLIC;
REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA public FROM :"asset_health_role";
REVOKE ALL PRIVILEGES ON ALL ROUTINES IN SCHEMA public FROM PUBLIC;
REVOKE ALL PRIVILEGES ON ALL ROUTINES IN SCHEMA public FROM :"asset_health_role";
ALTER DEFAULT PRIVILEGES REVOKE EXECUTE ON ROUTINES FROM PUBLIC;

GRANT SELECT, INSERT ON ascp_asset_health TO :"asset_health_role";
GRANT UPDATE (state,epoch,evidence_digest,providers,finalized_block,observed_at,updated_at)
    ON ascp_asset_health TO :"asset_health_role";
GRANT SELECT, INSERT ON ascp_asset_health_observations, ascp_asset_recovery_proofs,
    ascp_asset_reclassifications TO :"asset_health_role";
GRANT SELECT ON ascp_payment_operations, ascp_payment_attempts, ascp_ledger_transactions,
    ascp_classified_ledger_postings, ascp_token_blocked_positions TO :"asset_health_role";

COMMIT;
