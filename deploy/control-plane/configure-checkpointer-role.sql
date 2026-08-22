\set ON_ERROR_STOP on
\if :{?checkpointer_role}
\else
\echo 'checkpointer_role psql variable is required'
\quit 2
\endif

BEGIN;

ALTER ROLE :"checkpointer_role"
    NOSUPERUSER NOCREATEROLE NOCREATEDB NOREPLICATION NOBYPASSRLS;

REVOKE CREATE ON SCHEMA public FROM PUBLIC;
REVOKE CREATE ON SCHEMA public FROM :"checkpointer_role";
GRANT USAGE ON SCHEMA public TO :"checkpointer_role";

REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA public FROM :"checkpointer_role";

GRANT SELECT ON ascp_events TO :"checkpointer_role";
GRANT SELECT, INSERT ON ascp_event_checkpoints TO :"checkpointer_role";
GRANT SELECT ON ascp_leadership_epochs, ascp_checkpoint_write_effects TO :"checkpointer_role";
GRANT INSERT (effect_id,organization_id,epoch,sink,state,started_at)
    ON ascp_checkpoint_write_effects TO :"checkpointer_role";
GRANT UPDATE (state,resolved_at) ON ascp_checkpoint_write_effects TO :"checkpointer_role";
GRANT INSERT (rejection_id,organization_id,sink,presented_epoch,observed_epoch,observed_state,rejected_at)
    ON ascp_checkpoint_write_rejections TO :"checkpointer_role";

COMMIT;
