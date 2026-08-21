\set ON_ERROR_STOP on
\if :{?keeper_role}
\else
\echo 'keeper_role psql variable is required'
SELECT 1/0;
\endif

SELECT COALESCE((SELECT rolcanlogin FROM pg_roles WHERE rolname = :'keeper_role'), false) AS keeper_role_can_login \gset
\if :keeper_role_can_login
\else
\echo 'keeper_role must exist and have LOGIN'
SELECT 1/0;
\endif

SELECT EXISTS (
    SELECT 1 FROM pg_auth_members
    WHERE member = (SELECT oid FROM pg_roles WHERE rolname = :'keeper_role')
       OR roleid = (SELECT oid FROM pg_roles WHERE rolname = :'keeper_role')
) AS keeper_role_has_membership \gset
\if :keeper_role_has_membership
\echo 'keeper_role must not participate in role memberships'
SELECT 1/0;
\endif

SELECT EXISTS (
    SELECT 1 FROM pg_shdepend
    WHERE refclassid = 'pg_authid'::regclass
      AND refobjid = (SELECT oid FROM pg_roles WHERE rolname = :'keeper_role')
      AND deptype = 'o'
) AS keeper_role_owns_database_object \gset
\if :keeper_role_owns_database_object
\echo 'keeper_role must not own database objects'
SELECT 1/0;
\endif

BEGIN;

ALTER ROLE :"keeper_role"
    NOSUPERUSER NOCREATEROLE NOCREATEDB NOREPLICATION NOBYPASSRLS NOINHERIT;
ALTER ROLE :"keeper_role" SET search_path = public;

SELECT format('REVOKE TEMPORARY ON DATABASE %I FROM PUBLIC', current_database()) \gexec
SELECT format('REVOKE TEMPORARY ON DATABASE %I FROM %I', current_database(), :'keeper_role') \gexec

REVOKE CREATE ON SCHEMA public FROM PUBLIC;
REVOKE CREATE ON SCHEMA public FROM :"keeper_role";
GRANT USAGE ON SCHEMA public TO :"keeper_role";

REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA public FROM PUBLIC;
REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA public FROM :"keeper_role";
REVOKE ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public FROM PUBLIC;
REVOKE ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public FROM :"keeper_role";
REVOKE ALL PRIVILEGES ON ALL ROUTINES IN SCHEMA public FROM PUBLIC;
REVOKE ALL PRIVILEGES ON ALL ROUTINES IN SCHEMA public FROM :"keeper_role";
-- Execute this contract as every migration owner so future routines do not
-- restore PostgreSQL's implicit PUBLIC EXECUTE privilege.
ALTER DEFAULT PRIVILEGES REVOKE EXECUTE ON ROUTINES FROM PUBLIC;

GRANT SELECT, INSERT ON public.ascp_keeper_jobs, public.ascp_keeper_nonce_sequences,
    public.ascp_keeper_tx_attempts TO :"keeper_role";
GRANT SELECT ON public.ascp_leadership_epochs TO :"keeper_role";
GRANT UPDATE (lease_owner, lease_token, lease_expires_at, nonce, state,
    attempt_count, current_attempt, last_error, updated_at)
    ON public.ascp_keeper_jobs TO :"keeper_role";
GRANT UPDATE (next_nonce, updated_at)
    ON public.ascp_keeper_nonce_sequences TO :"keeper_role";
GRANT UPDATE (state, broadcast_at, last_error, evidence_digest, observed_at)
    ON public.ascp_keeper_tx_attempts TO :"keeper_role";

COMMIT;
