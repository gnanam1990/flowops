\set ON_ERROR_STOP on
\if :{?governance_relayer_role}
\else
\echo 'governance_relayer_role psql variable is required'
SELECT 1/0;
\endif

SELECT COALESCE((SELECT rolcanlogin FROM pg_roles WHERE rolname = :'governance_relayer_role'), false) AS role_can_login \gset
\if :role_can_login
\else
\echo 'governance_relayer_role must exist and have LOGIN'
SELECT 1/0;
\endif

SELECT EXISTS (
    SELECT 1 FROM pg_auth_members
    WHERE member = (SELECT oid FROM pg_roles WHERE rolname = :'governance_relayer_role')
       OR roleid = (SELECT oid FROM pg_roles WHERE rolname = :'governance_relayer_role')
) AS role_has_membership \gset
\if :role_has_membership
\echo 'governance_relayer_role must not participate in role memberships'
SELECT 1/0;
\endif

SELECT EXISTS (
    SELECT 1 FROM pg_shdepend
    WHERE refclassid = 'pg_authid'::regclass
      AND refobjid = (SELECT oid FROM pg_roles WHERE rolname = :'governance_relayer_role')
      AND deptype = 'o'
) AS role_owns_database_object \gset
\if :role_owns_database_object
\echo 'governance_relayer_role must not own database objects'
SELECT 1/0;
\endif

BEGIN;

ALTER ROLE :"governance_relayer_role"
    NOSUPERUSER NOCREATEROLE NOCREATEDB NOREPLICATION NOBYPASSRLS NOINHERIT;
ALTER ROLE :"governance_relayer_role" SET search_path = public;

SELECT format('REVOKE TEMPORARY ON DATABASE %I FROM PUBLIC', current_database()) \gexec
SELECT format('REVOKE TEMPORARY ON DATABASE %I FROM %I', current_database(), :'governance_relayer_role') \gexec
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
REVOKE CREATE ON SCHEMA public FROM :"governance_relayer_role";
GRANT USAGE ON SCHEMA public TO :"governance_relayer_role";

REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA public FROM PUBLIC;
REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA public FROM :"governance_relayer_role";
REVOKE ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public FROM PUBLIC;
REVOKE ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public FROM :"governance_relayer_role";
REVOKE ALL PRIVILEGES ON ALL ROUTINES IN SCHEMA public FROM PUBLIC;
REVOKE ALL PRIVILEGES ON ALL ROUTINES IN SCHEMA public FROM :"governance_relayer_role";
ALTER DEFAULT PRIVILEGES REVOKE EXECUTE ON ROUTINES FROM PUBLIC;

GRANT SELECT ON public.ascp_workflow_outbox, public.ascp_proposal_workflows,
    public.ascp_workflow_actions, public.ascp_workflow_events,
    public.ascp_governance_relay_jobs, public.ascp_governance_relay_authorizations,
    public.ascp_workflow_safe_retry_proofs TO :"governance_relayer_role";
GRANT INSERT ON public.ascp_governance_relay_jobs,
    public.ascp_governance_relay_authorizations, public.ascp_workflow_safe_retry_proofs,
    public.ascp_workflow_actions, public.ascp_workflow_events,
    public.ascp_workflow_outbox TO :"governance_relayer_role";
GRANT UPDATE (state, prepared_json, artifact_handle, authorization_key, authorization_hash,
    outer_json, last_outcome_json, attempt_count, lease_owner, lease_token, lease_expires_at, updated_at)
    ON public.ascp_governance_relay_jobs TO :"governance_relayer_role";
GRANT UPDATE (state, submission_transaction_hash, submitted_at, confirmed_at,
    terminal_reason, terminal_at)
    ON public.ascp_proposal_workflows TO :"governance_relayer_role";
GRANT EXECUTE ON FUNCTION public.flowops_governance_observers_valid(jsonb)
    TO :"governance_relayer_role";

COMMIT;
