\set ON_ERROR_STOP on
\if :{?rails_role}
\else
\echo 'rails_role psql variable is required'
\quit 2
\endif

BEGIN;

ALTER ROLE :"rails_role"
    NOSUPERUSER NOCREATEROLE NOCREATEDB NOREPLICATION NOBYPASSRLS;

REVOKE CREATE ON SCHEMA public FROM PUBLIC;
REVOKE CREATE ON SCHEMA public FROM :"rails_role";
GRANT USAGE ON SCHEMA public TO :"rails_role";
REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA public FROM PUBLIC;
REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA public FROM :"rails_role";
REVOKE ALL PRIVILEGES ON ALL ROUTINES IN SCHEMA public FROM PUBLIC;
REVOKE ALL PRIVILEGES ON ALL ROUTINES IN SCHEMA public FROM :"rails_role";

GRANT SELECT ON ascp_seller_jobs, ascp_seller_attempts, ascp_seller_responses,
    ascp_seller_proxy_egress_effects TO :"rails_role";
GRANT SELECT ON ascp_payment_operations TO :"rails_role";
GRANT SELECT ON ascp_leadership_epochs TO :"rails_role";
GRANT INSERT ON ascp_seller_attempts, ascp_seller_responses TO :"rails_role";
GRANT INSERT (effect_id,organization_id,epoch,sink,state,started_at)
    ON ascp_seller_proxy_egress_effects TO :"rails_role";
GRANT INSERT (rejection_id,organization_id,sink,presented_epoch,observed_epoch,observed_state,rejected_at)
    ON ascp_seller_proxy_egress_rejections TO :"rails_role";
GRANT UPDATE (state,resolved_at) ON ascp_seller_proxy_egress_effects TO :"rails_role";
GRANT EXECUTE ON FUNCTION public.ascp_current_event_head() TO :"rails_role";
GRANT UPDATE (state,eligible_after,lease_owner,lease_token,lease_expires_at,attempt_count,
    captured_at,capture_evidence_digest,deadline_evidence_digest,last_error,updated_at)
    ON ascp_seller_jobs TO :"rails_role";
GRANT UPDATE (state,completed_at,result_code) ON ascp_seller_attempts TO :"rails_role";

COMMIT;
