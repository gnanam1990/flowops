\set ON_ERROR_STOP on
\if :{?verifier_role}
\else
\echo 'verifier_role psql variable is required'
SELECT 1/0;
\endif

SELECT COALESCE((SELECT rolcanlogin FROM pg_roles WHERE rolname = :'verifier_role'), false) AS verifier_role_can_login \gset
\if :verifier_role_can_login
\else
\echo 'verifier_role must exist and have LOGIN'
SELECT 1/0;
\endif

SELECT EXISTS (
    SELECT 1 FROM pg_auth_members
    WHERE member = (SELECT oid FROM pg_roles WHERE rolname = :'verifier_role')
       OR roleid = (SELECT oid FROM pg_roles WHERE rolname = :'verifier_role')
) AS verifier_role_has_membership \gset
\if :verifier_role_has_membership
\echo 'verifier_role must not participate in role memberships'
SELECT 1/0;
\endif

SELECT EXISTS (
    SELECT 1 FROM pg_shdepend
    WHERE refclassid = 'pg_authid'::regclass
      AND refobjid = (SELECT oid FROM pg_roles WHERE rolname = :'verifier_role')
      AND deptype = 'o'
) AS verifier_role_owns_database_object \gset
\if :verifier_role_owns_database_object
\echo 'verifier_role must not own database objects'
SELECT 1/0;
\endif

BEGIN;
ALTER ROLE :"verifier_role" NOSUPERUSER NOCREATEROLE NOCREATEDB NOREPLICATION NOBYPASSRLS NOINHERIT;
ALTER ROLE :"verifier_role" SET search_path = public;
SELECT format('REVOKE TEMPORARY ON DATABASE %I FROM PUBLIC', current_database()) \gexec
SELECT format('REVOKE TEMPORARY ON DATABASE %I FROM %I', current_database(), :'verifier_role') \gexec
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
REVOKE CREATE ON SCHEMA public FROM :"verifier_role";
REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA public FROM :"verifier_role";
REVOKE ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public FROM :"verifier_role";
REVOKE ALL PRIVILEGES ON ALL ROUTINES IN SCHEMA public FROM PUBLIC;
REVOKE ALL PRIVILEGES ON ALL ROUTINES IN SCHEMA public FROM :"verifier_role";
ALTER DEFAULT PRIVILEGES REVOKE EXECUTE ON ROUTINES FROM PUBLIC;

GRANT USAGE ON SCHEMA public TO :"verifier_role";
GRANT SELECT, INSERT ON public.ascp_verdict_decisions TO :"verifier_role";
GRANT SELECT ON public.ascp_verifier_key_observations TO :"verifier_role";
GRANT INSERT ON public.ascp_verifier_intake_replays TO :"verifier_role";
GRANT USAGE, SELECT ON SEQUENCE public.ascp_verdict_nonce_seq TO :"verifier_role";
GRANT EXECUTE ON FUNCTION public.prune_ascp_verifier_intake_replays() TO :"verifier_role";
COMMIT;
