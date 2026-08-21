\set ON_ERROR_STOP on
\if :{?recovery_role}
\else
\echo 'recovery_role psql variable is required'
SELECT 1/0;
\endif

SELECT COALESCE((
    SELECT rolcanlogin FROM pg_roles WHERE rolname = :'recovery_role'
), false) AS recovery_role_can_login \gset
\if :recovery_role_can_login
\else
\echo 'recovery_role must exist and have LOGIN'
SELECT 1/0;
\endif

SELECT EXISTS (
    SELECT 1 FROM pg_auth_members
    WHERE member = (SELECT oid FROM pg_roles WHERE rolname = :'recovery_role')
       OR roleid = (SELECT oid FROM pg_roles WHERE rolname = :'recovery_role')
) AS recovery_role_has_membership \gset
\if :recovery_role_has_membership
\echo 'recovery_role must not participate in role memberships'
SELECT 1/0;
\endif

SELECT EXISTS (
    SELECT 1 FROM pg_shdepend
    WHERE refclassid = 'pg_authid'::regclass
      AND refobjid = (SELECT oid FROM pg_roles WHERE rolname = :'recovery_role')
      AND deptype = 'o'
) AS recovery_role_owns_database_object \gset
\if :recovery_role_owns_database_object
\echo 'recovery_role must not own database objects'
SELECT 1/0;
\endif

BEGIN;

ALTER ROLE :"recovery_role"
    NOSUPERUSER NOCREATEROLE NOCREATEDB NOREPLICATION NOBYPASSRLS NOINHERIT;

REVOKE CREATE ON SCHEMA public FROM PUBLIC;
REVOKE CREATE ON SCHEMA public FROM :"recovery_role";
GRANT USAGE ON SCHEMA public TO :"recovery_role";
REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA public FROM PUBLIC;
REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA public FROM :"recovery_role";
REVOKE ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public FROM PUBLIC;
REVOKE ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public FROM :"recovery_role";
REVOKE ALL PRIVILEGES ON ALL ROUTINES IN SCHEMA public FROM PUBLIC;
REVOKE ALL PRIVILEGES ON ALL ROUTINES IN SCHEMA public FROM :"recovery_role";

GRANT SELECT ON ascp_events, ascp_event_checkpoints TO :"recovery_role";

COMMIT;
