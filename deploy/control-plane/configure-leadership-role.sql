\set ON_ERROR_STOP on
\if :{?leadership_role}
\else
\echo 'leadership_role psql variable is required'
SELECT 1/0;
\endif

SELECT COALESCE((
    SELECT rolcanlogin FROM pg_roles WHERE rolname = :'leadership_role'
), false) AS leadership_role_can_login \gset
\if :leadership_role_can_login
\else
\echo 'leadership_role must exist and have LOGIN'
SELECT 1/0;
\endif

SELECT EXISTS (
    SELECT 1 FROM pg_auth_members
    WHERE member = (SELECT oid FROM pg_roles WHERE rolname = :'leadership_role')
       OR roleid = (SELECT oid FROM pg_roles WHERE rolname = :'leadership_role')
) AS leadership_role_has_membership \gset
\if :leadership_role_has_membership
\echo 'leadership_role must not participate in role memberships'
SELECT 1/0;
\endif

SELECT EXISTS (
    SELECT 1 FROM pg_class
    WHERE relowner = (SELECT oid FROM pg_roles WHERE rolname = :'leadership_role')
    UNION ALL
    SELECT 1 FROM pg_proc
    WHERE proowner = (SELECT oid FROM pg_roles WHERE rolname = :'leadership_role')
    UNION ALL
    SELECT 1 FROM pg_namespace
    WHERE nspowner = (SELECT oid FROM pg_roles WHERE rolname = :'leadership_role')
    UNION ALL
    SELECT 1 FROM pg_database
    WHERE datname = current_database()
      AND datdba = (SELECT oid FROM pg_roles WHERE rolname = :'leadership_role')
) AS leadership_role_owns_database_object \gset
\if :leadership_role_owns_database_object
\echo 'leadership_role must not own database objects'
SELECT 1/0;
\endif

BEGIN;

ALTER ROLE :"leadership_role"
    NOSUPERUSER NOCREATEROLE NOCREATEDB NOREPLICATION NOBYPASSRLS NOINHERIT;

REVOKE CREATE ON SCHEMA public FROM PUBLIC;
REVOKE CREATE ON SCHEMA public FROM :"leadership_role";
GRANT USAGE ON SCHEMA public TO :"leadership_role";
REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA public FROM PUBLIC;
REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA public FROM :"leadership_role";
REVOKE ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public FROM :"leadership_role";

GRANT SELECT ON ascp_leadership_epochs, ascp_leadership_events TO :"leadership_role";
GRANT INSERT (organization_id,epoch,state,evidence_digest,actor,updated_at)
    ON ascp_leadership_epochs TO :"leadership_role";
GRANT INSERT (organization_id,previous_epoch,new_epoch,previous_state,new_state,evidence_digest,actor,created_at)
    ON ascp_leadership_events TO :"leadership_role";
GRANT UPDATE (epoch,state,evidence_digest,actor,updated_at)
    ON ascp_leadership_epochs TO :"leadership_role";
GRANT USAGE, SELECT ON SEQUENCE ascp_leadership_events_event_id_seq TO :"leadership_role";

COMMIT;
