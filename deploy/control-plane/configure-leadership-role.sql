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
    SELECT 1 FROM pg_shdepend
    WHERE refclassid = 'pg_authid'::regclass
      AND refobjid = (SELECT oid FROM pg_roles WHERE rolname = :'leadership_role')
      AND deptype = 'o'
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
REVOKE ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public FROM PUBLIC;
REVOKE ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public FROM :"leadership_role";

GRANT SELECT ON ascp_leadership_epochs, ascp_leadership_events, ascp_leadership_effects,
    ascp_leadership_rejections, ascp_promotion_runs TO :"leadership_role";
GRANT SELECT (operation_id,organization_id) ON ascp_intents TO :"leadership_role";
GRANT SELECT (operation_id,outcome) ON ascp_bearer_registry TO :"leadership_role";
GRANT SELECT (call_id,decision_json) ON ascp_verdict_decisions TO :"leadership_role";
GRANT SELECT (call_id,organization_id) ON ascp_payment_operations TO :"leadership_role";
GRANT INSERT (organization_id,epoch,state,evidence_digest,actor,updated_at)
    ON ascp_leadership_epochs TO :"leadership_role";
GRANT INSERT (organization_id,previous_epoch,new_epoch,previous_state,new_state,evidence_digest,actor,created_at)
    ON ascp_leadership_events TO :"leadership_role";
GRANT UPDATE (epoch,state,evidence_digest,actor,updated_at)
    ON ascp_leadership_epochs TO :"leadership_role";
GRANT UPDATE (state,resolved_at,resolution_actor,resolution_evidence_digest)
    ON ascp_leadership_effects TO :"leadership_role";
GRANT INSERT (run_id,organization_id,source_epoch,target_epoch,state,finality_margin_seconds,
    drain_evidence_digest,started_at) ON ascp_promotion_runs TO :"leadership_role";
GRANT UPDATE (state,ready_evidence_digest,ready_at,cutover_at,completion_evidence_digest,completed_at)
    ON ascp_promotion_runs TO :"leadership_role";
GRANT USAGE, SELECT ON SEQUENCE ascp_leadership_events_event_id_seq TO :"leadership_role";

COMMIT;
