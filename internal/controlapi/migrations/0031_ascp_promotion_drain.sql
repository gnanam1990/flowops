ALTER TABLE ascp_leadership_effects
    ADD COLUMN sink text NOT NULL DEFAULT 'LEGACY_CONTROLLED_EFFECT';

ALTER TABLE ascp_leadership_effects
    ADD CONSTRAINT ascp_leadership_effects_sink CHECK (sink IN (
        'LEGACY_CONTROLLED_EFFECT','SIGNER_ISSUANCE','VERIFIER_ATTESTATION',
        'KEEPER_RELAY','SELLER_PROXY_EGRESS','OUTBOX_DISPATCH','CHECKPOINT_WRITE'
    ));

CREATE VIEW ascp_signer_issuance_effects AS
SELECT * FROM ascp_leadership_effects WHERE sink='SIGNER_ISSUANCE' WITH LOCAL CHECK OPTION;
CREATE VIEW ascp_verifier_attestation_effects AS
SELECT * FROM ascp_leadership_effects WHERE sink='VERIFIER_ATTESTATION' WITH LOCAL CHECK OPTION;
CREATE VIEW ascp_keeper_relay_effects AS
SELECT * FROM ascp_leadership_effects WHERE sink='KEEPER_RELAY' WITH LOCAL CHECK OPTION;
CREATE VIEW ascp_seller_proxy_egress_effects AS
SELECT * FROM ascp_leadership_effects WHERE sink='SELLER_PROXY_EGRESS' WITH LOCAL CHECK OPTION;
CREATE VIEW ascp_outbox_dispatch_effects AS
SELECT * FROM ascp_leadership_effects WHERE sink='OUTBOX_DISPATCH' WITH LOCAL CHECK OPTION;
CREATE VIEW ascp_checkpoint_write_effects AS
SELECT * FROM ascp_leadership_effects WHERE sink='CHECKPOINT_WRITE' WITH LOCAL CHECK OPTION;

CREATE TABLE ascp_leadership_rejections (
    rejection_id text PRIMARY KEY CHECK (rejection_id ~ '^0x[0-9a-f]{64}$'),
    organization_id text NOT NULL REFERENCES ascp_leadership_epochs(organization_id),
    sink text NOT NULL CHECK (sink IN (
        'SIGNER_ISSUANCE','VERIFIER_ATTESTATION','KEEPER_RELAY',
        'SELLER_PROXY_EGRESS','OUTBOX_DISPATCH','CHECKPOINT_WRITE'
    )),
    presented_epoch bigint NOT NULL CHECK (presented_epoch > 0),
    observed_epoch bigint NOT NULL CHECK (observed_epoch > 0),
    observed_state text NOT NULL CHECK (observed_state IN ('ACTIVE','DRAINING')),
    rejected_at timestamptz NOT NULL,
    CHECK (presented_epoch <> observed_epoch OR observed_state='DRAINING')
);

CREATE INDEX ascp_leadership_rejections_promotion
ON ascp_leadership_rejections(organization_id,presented_epoch,observed_epoch,rejected_at,sink);

CREATE VIEW ascp_signer_issuance_rejections AS
SELECT * FROM ascp_leadership_rejections WHERE sink='SIGNER_ISSUANCE' WITH LOCAL CHECK OPTION;
CREATE VIEW ascp_verifier_attestation_rejections AS
SELECT * FROM ascp_leadership_rejections WHERE sink='VERIFIER_ATTESTATION' WITH LOCAL CHECK OPTION;
CREATE VIEW ascp_keeper_relay_rejections AS
SELECT * FROM ascp_leadership_rejections WHERE sink='KEEPER_RELAY' WITH LOCAL CHECK OPTION;
CREATE VIEW ascp_seller_proxy_egress_rejections AS
SELECT * FROM ascp_leadership_rejections WHERE sink='SELLER_PROXY_EGRESS' WITH LOCAL CHECK OPTION;
CREATE VIEW ascp_outbox_dispatch_rejections AS
SELECT * FROM ascp_leadership_rejections WHERE sink='OUTBOX_DISPATCH' WITH LOCAL CHECK OPTION;
CREATE VIEW ascp_checkpoint_write_rejections AS
SELECT * FROM ascp_leadership_rejections WHERE sink='CHECKPOINT_WRITE' WITH LOCAL CHECK OPTION;

CREATE TRIGGER ascp_leadership_rejections_append_only
BEFORE UPDATE OR DELETE ON ascp_leadership_rejections
FOR EACH ROW EXECUTE FUNCTION flowops_reject_immutable_mutation();
CREATE TRIGGER ascp_leadership_rejections_no_truncate
BEFORE TRUNCATE ON ascp_leadership_rejections
FOR EACH STATEMENT EXECUTE FUNCTION flowops_reject_immutable_mutation();

CREATE OR REPLACE FUNCTION flowops_validate_leadership_effect_update()
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, pg_temp AS $$
DECLARE current_epoch bigint;
DECLARE current_state text;
BEGIN
    IF OLD.effect_id IS DISTINCT FROM NEW.effect_id OR OLD.organization_id IS DISTINCT FROM NEW.organization_id OR
       OLD.epoch IS DISTINCT FROM NEW.epoch OR OLD.sink IS DISTINCT FROM NEW.sink OR
       OLD.started_at IS DISTINCT FROM NEW.started_at OR OLD.state IS DISTINCT FROM 'IN_FLIGHT' OR
       NEW.state NOT IN ('COMPLETED','ABANDONED') THEN
        RAISE EXCEPTION 'invalid leadership effect resolution' USING ERRCODE='23514';
    END IF;
    IF NEW.state='ABANDONED' THEN
        EXECUTE format('SELECT epoch,state FROM %I.ascp_leadership_epochs WHERE organization_id=$1', TG_TABLE_SCHEMA)
        INTO current_epoch,current_state USING NEW.organization_id;
        IF current_epoch IS NULL OR current_epoch IS DISTINCT FROM NEW.epoch OR current_state IS DISTINCT FROM 'DRAINING' THEN
            RAISE EXCEPTION 'leadership effect abandonment requires its draining epoch' USING ERRCODE='23514';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;

CREATE TABLE ascp_promotion_runs (
    run_id text PRIMARY KEY CHECK (run_id ~ '^0x[0-9a-f]{64}$'),
    organization_id text NOT NULL REFERENCES ascp_leadership_epochs(organization_id),
    source_epoch bigint NOT NULL CHECK (source_epoch > 0),
    target_epoch bigint NOT NULL CHECK (target_epoch = source_epoch + 1),
    state text NOT NULL CHECK (state IN ('DRAINING','READY','CUTOVER','COMPLETE')),
    finality_margin_seconds integer NOT NULL CHECK (finality_margin_seconds BETWEEN 1 AND 3600),
    drain_evidence_digest text NOT NULL CHECK (drain_evidence_digest ~ '^0x[0-9a-f]{64}$'),
    ready_evidence_digest text CHECK (ready_evidence_digest IS NULL OR ready_evidence_digest ~ '^0x[0-9a-f]{64}$'),
    completion_evidence_digest text CHECK (completion_evidence_digest IS NULL OR completion_evidence_digest ~ '^0x[0-9a-f]{64}$'),
    started_at timestamptz NOT NULL,
    ready_at timestamptz,
    cutover_at timestamptz,
    completed_at timestamptz,
    UNIQUE(organization_id,source_epoch),
    CHECK (ready_at IS NULL OR ready_at >= started_at),
    CHECK (cutover_at IS NULL OR ready_at IS NOT NULL AND cutover_at >= ready_at),
    CHECK (completed_at IS NULL OR cutover_at IS NOT NULL AND completed_at >= cutover_at),
    CHECK (
      (state='DRAINING' AND ready_evidence_digest IS NULL AND ready_at IS NULL AND cutover_at IS NULL AND completion_evidence_digest IS NULL AND completed_at IS NULL) OR
      (state='READY' AND ready_evidence_digest IS NOT NULL AND ready_at IS NOT NULL AND cutover_at IS NULL AND completion_evidence_digest IS NULL AND completed_at IS NULL) OR
      (state='CUTOVER' AND ready_evidence_digest IS NOT NULL AND ready_at IS NOT NULL AND cutover_at IS NOT NULL AND completion_evidence_digest IS NULL AND completed_at IS NULL) OR
      (state='COMPLETE' AND ready_evidence_digest IS NOT NULL AND ready_at IS NOT NULL AND cutover_at IS NOT NULL AND completion_evidence_digest IS NOT NULL AND completed_at IS NOT NULL)
    )
);

CREATE OR REPLACE FUNCTION flowops_validate_promotion_run_update()
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, pg_temp AS $$
DECLARE rejected_sinks integer;
BEGIN
    IF ROW(OLD.run_id,OLD.organization_id,OLD.source_epoch,OLD.target_epoch,OLD.finality_margin_seconds,
           OLD.drain_evidence_digest,OLD.started_at) IS DISTINCT FROM
       ROW(NEW.run_id,NEW.organization_id,NEW.source_epoch,NEW.target_epoch,NEW.finality_margin_seconds,
           NEW.drain_evidence_digest,NEW.started_at) OR
       NOT ((OLD.state='DRAINING' AND NEW.state='READY') OR
            (OLD.state='READY' AND NEW.state='CUTOVER') OR
            (OLD.state='CUTOVER' AND NEW.state='COMPLETE')) THEN
        RAISE EXCEPTION 'invalid promotion run mutation' USING ERRCODE='23514';
    END IF;
    IF OLD.state='READY' AND
       (OLD.ready_evidence_digest IS DISTINCT FROM NEW.ready_evidence_digest OR
        OLD.ready_at IS DISTINCT FROM NEW.ready_at) THEN
        RAISE EXCEPTION 'promotion readiness evidence is immutable' USING ERRCODE='23514';
    END IF;
    IF OLD.state='CUTOVER' AND
       (OLD.ready_evidence_digest IS DISTINCT FROM NEW.ready_evidence_digest OR
        OLD.ready_at IS DISTINCT FROM NEW.ready_at OR
        OLD.cutover_at IS DISTINCT FROM NEW.cutover_at) THEN
        RAISE EXCEPTION 'promotion cutover evidence is immutable' USING ERRCODE='23514';
    END IF;
    IF OLD.state='CUTOVER' THEN
        EXECUTE format('SELECT count(DISTINCT sink) FROM %I.ascp_leadership_rejections
          WHERE organization_id=$1 AND presented_epoch=$2 AND observed_epoch=$3
            AND observed_state=''ACTIVE'' AND rejected_at >= $4', TG_TABLE_SCHEMA)
        INTO rejected_sinks USING NEW.organization_id,NEW.source_epoch,NEW.target_epoch,OLD.cutover_at;
        IF rejected_sinks <> 6 THEN
            RAISE EXCEPTION 'promotion completion requires every controlled sink rejection' USING ERRCODE='23514';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER ascp_promotion_runs_validate_update
BEFORE UPDATE ON ascp_promotion_runs
FOR EACH ROW EXECUTE FUNCTION flowops_validate_promotion_run_update();
CREATE TRIGGER ascp_promotion_runs_no_delete
BEFORE DELETE ON ascp_promotion_runs
FOR EACH ROW EXECUTE FUNCTION flowops_reject_immutable_mutation();
CREATE TRIGGER ascp_promotion_runs_no_truncate
BEFORE TRUNCATE ON ascp_promotion_runs
FOR EACH STATEMENT EXECUTE FUNCTION flowops_reject_immutable_mutation();

CREATE OR REPLACE FUNCTION flowops_validate_leadership_update()
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, pg_temp AS $$
DECLARE effect_in_flight boolean;
DECLARE promotion_run_id text;
DECLARE margin_seconds integer;
DECLARE cutover_rows integer;
BEGIN
    IF OLD.organization_id IS DISTINCT FROM NEW.organization_id OR
       OLD.updated_at >= NEW.updated_at OR
       NOT ((OLD.state='ACTIVE' AND NEW.state='DRAINING' AND NEW.epoch=OLD.epoch) OR
            (OLD.state='DRAINING' AND NEW.state='ACTIVE' AND NEW.epoch=OLD.epoch+1)) THEN
        RAISE EXCEPTION 'invalid leadership transition' USING ERRCODE='23514';
    END IF;
    IF OLD.state='ACTIVE' THEN
        NEW.drain_transaction_id := txid_current();
        RETURN NEW;
    END IF;
    IF OLD.drain_transaction_id=txid_current() THEN
        RAISE EXCEPTION 'leadership drain and advance require separate transactions' USING ERRCODE='23514';
    END IF;
    EXECUTE format('SELECT EXISTS(SELECT 1 FROM %I.ascp_leadership_effects
      WHERE organization_id=$1 AND epoch=$2 AND state=''IN_FLIGHT'')', TG_TABLE_SCHEMA)
    INTO effect_in_flight USING OLD.organization_id,OLD.epoch;
    IF effect_in_flight THEN
        RAISE EXCEPTION 'leadership advance requires every effect to be resolved' USING ERRCODE='23514';
    END IF;
    EXECUTE format('SELECT run_id,finality_margin_seconds FROM %I.ascp_promotion_runs
      WHERE organization_id=$1 AND source_epoch=$2 AND state=''READY'' FOR UPDATE', TG_TABLE_SCHEMA)
    INTO promotion_run_id,margin_seconds USING OLD.organization_id,OLD.epoch;
    IF promotion_run_id IS NULL THEN
        RAISE EXCEPTION 'leadership advance requires a ready promotion drain' USING ERRCODE='23514';
    END IF;
    EXECUTE format('SELECT EXISTS(SELECT 1 FROM %I.ascp_bearer_registry b
      JOIN %I.ascp_intents i ON i.operation_id=b.operation_id
      WHERE i.organization_id=$1 AND b.outcome=''LIVE'')', TG_TABLE_SCHEMA,TG_TABLE_SCHEMA)
    INTO effect_in_flight USING OLD.organization_id;
    IF effect_in_flight THEN
        RAISE EXCEPTION 'leadership advance blocked by live bearer instruments' USING ERRCODE='23514';
    END IF;
    EXECUTE format('SELECT EXISTS(SELECT 1 FROM %I.ascp_verdict_decisions v
      JOIN %I.ascp_payment_operations p ON p.call_id=v.call_id
      WHERE p.organization_id=$1 AND
      to_timestamp((v.decision_json #>> ''{attestation,validUntil}'')::bigint)
      + make_interval(secs => $2) > now())', TG_TABLE_SCHEMA,TG_TABLE_SCHEMA)
    INTO effect_in_flight USING OLD.organization_id,margin_seconds;
    IF effect_in_flight THEN
        RAISE EXCEPTION 'leadership advance blocked by live verifier attestations' USING ERRCODE='23514';
    END IF;
    EXECUTE format('UPDATE %I.ascp_promotion_runs SET state=''CUTOVER'',cutover_at=$2
      WHERE run_id=$1 AND state=''READY''', TG_TABLE_SCHEMA)
    USING promotion_run_id,NEW.updated_at;
    GET DIAGNOSTICS cutover_rows = ROW_COUNT;
    IF cutover_rows <> 1 THEN
        RAISE EXCEPTION 'promotion cutover transition failed' USING ERRCODE='23514';
    END IF;
    NEW.drain_transaction_id := NULL;
    RETURN NEW;
END;
$$;

REVOKE ALL ON ascp_leadership_rejections, ascp_promotion_runs FROM PUBLIC;
REVOKE ALL ON ascp_signer_issuance_rejections, ascp_verifier_attestation_rejections,
    ascp_keeper_relay_rejections, ascp_seller_proxy_egress_rejections,
    ascp_outbox_dispatch_rejections, ascp_checkpoint_write_rejections FROM PUBLIC;
REVOKE ALL ON ascp_signer_issuance_effects, ascp_verifier_attestation_effects,
    ascp_keeper_relay_effects, ascp_seller_proxy_egress_effects,
    ascp_outbox_dispatch_effects, ascp_checkpoint_write_effects FROM PUBLIC;
