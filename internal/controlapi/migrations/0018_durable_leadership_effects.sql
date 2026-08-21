CREATE TABLE ascp_leadership_effects (
    effect_id text PRIMARY KEY CHECK (effect_id ~ '^0x[0-9a-f]{64}$'),
    organization_id text NOT NULL REFERENCES ascp_leadership_epochs(organization_id),
    epoch bigint NOT NULL CHECK (epoch > 0),
    state text NOT NULL CHECK (state IN ('IN_FLIGHT','COMPLETED','ABANDONED')),
    started_at timestamptz NOT NULL,
    resolved_at timestamptz,
    resolution_actor text CHECK (
        resolution_actor IS NULL OR
        resolution_actor ~ '^[A-Za-z0-9][A-Za-z0-9._:@/-]{0,127}$'
    ),
    resolution_evidence_digest text CHECK (
        resolution_evidence_digest IS NULL OR
        resolution_evidence_digest ~ '^0x[0-9a-f]{64}$'
    ),
    CHECK (
        (state = 'IN_FLIGHT' AND resolved_at IS NULL AND
            resolution_actor IS NULL AND resolution_evidence_digest IS NULL) OR
        (state = 'COMPLETED' AND resolved_at IS NOT NULL AND
            resolution_actor IS NULL AND resolution_evidence_digest IS NULL) OR
        (state = 'ABANDONED' AND resolved_at IS NOT NULL AND
            resolution_actor IS NOT NULL AND resolution_evidence_digest IS NOT NULL)
    ),
    CHECK (resolved_at IS NULL OR resolved_at >= started_at)
);

CREATE INDEX ascp_leadership_effects_in_flight
    ON ascp_leadership_effects (organization_id, epoch)
    WHERE state = 'IN_FLIGHT';

CREATE OR REPLACE FUNCTION flowops_validate_leadership_effect_insert()
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog AS $$
DECLARE
    current_epoch bigint;
    current_state text;
BEGIN
    EXECUTE format($query$
        SELECT epoch, state
        FROM %I.ascp_leadership_epochs
        WHERE organization_id = $1
    $query$, TG_TABLE_SCHEMA)
    INTO current_epoch, current_state
    USING NEW.organization_id;

    IF current_epoch IS NULL OR current_epoch IS DISTINCT FROM NEW.epoch OR
       current_state IS DISTINCT FROM 'ACTIVE' OR NEW.state IS DISTINCT FROM 'IN_FLIGHT' THEN
        RAISE EXCEPTION 'leadership effect requires the active current epoch' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION flowops_validate_leadership_effect_update()
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog AS $$
DECLARE
    current_epoch bigint;
    current_state text;
BEGIN
    IF OLD.effect_id IS DISTINCT FROM NEW.effect_id OR
       OLD.organization_id IS DISTINCT FROM NEW.organization_id OR
       OLD.epoch IS DISTINCT FROM NEW.epoch OR
       OLD.started_at IS DISTINCT FROM NEW.started_at OR
       OLD.state IS DISTINCT FROM 'IN_FLIGHT' OR
       NEW.state NOT IN ('COMPLETED','ABANDONED') THEN
        RAISE EXCEPTION 'invalid leadership effect resolution' USING ERRCODE = '23514';
    END IF;
    IF NEW.state = 'ABANDONED' THEN
        EXECUTE format($query$
            SELECT epoch, state
            FROM %I.ascp_leadership_epochs
            WHERE organization_id = $1
        $query$, TG_TABLE_SCHEMA)
        INTO current_epoch, current_state
        USING NEW.organization_id;
        IF current_epoch IS NULL OR current_epoch IS DISTINCT FROM NEW.epoch OR
           current_state IS DISTINCT FROM 'DRAINING' THEN
            RAISE EXCEPTION 'leadership effect abandonment requires its draining epoch' USING ERRCODE = '23514';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;

-- PostgreSQL fires same-kind row triggers in alphabetical order. Keep each
-- *_lock name before its *_validate_* peer so direct inserts serialize before
-- checking the leadership state.
CREATE TRIGGER ascp_leadership_effects_lock
BEFORE INSERT ON ascp_leadership_effects
FOR EACH ROW EXECUTE FUNCTION flowops_lock_leadership_organization();

CREATE TRIGGER ascp_leadership_effects_validate_insert
BEFORE INSERT ON ascp_leadership_effects
FOR EACH ROW EXECUTE FUNCTION flowops_validate_leadership_effect_insert();

CREATE TRIGGER ascp_leadership_effects_validate_update
BEFORE UPDATE ON ascp_leadership_effects
FOR EACH ROW EXECUTE FUNCTION flowops_validate_leadership_effect_update();

CREATE TRIGGER ascp_leadership_effects_no_delete
BEFORE DELETE ON ascp_leadership_effects
FOR EACH ROW EXECUTE FUNCTION flowops_reject_immutable_mutation();

CREATE OR REPLACE FUNCTION flowops_validate_leadership_update()
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog AS $$
DECLARE
    effect_in_flight boolean;
BEGIN
    IF OLD.organization_id IS DISTINCT FROM NEW.organization_id OR
       OLD.updated_at >= NEW.updated_at OR
       NOT ((OLD.state = 'ACTIVE' AND NEW.state = 'DRAINING' AND NEW.epoch = OLD.epoch) OR
            (OLD.state = 'DRAINING' AND NEW.state = 'ACTIVE' AND NEW.epoch = OLD.epoch + 1)) THEN
        RAISE EXCEPTION 'invalid leadership transition' USING ERRCODE = '23514';
    END IF;
    IF OLD.state = 'ACTIVE' THEN
        NEW.drain_transaction_id := txid_current();
    ELSE
        IF OLD.drain_transaction_id = txid_current() THEN
            RAISE EXCEPTION 'leadership drain and advance require separate transactions' USING ERRCODE = '23514';
        END IF;
        EXECUTE format($query$
            SELECT EXISTS (
                SELECT 1 FROM %I.ascp_leadership_effects
                WHERE organization_id = $1 AND epoch = $2 AND state = 'IN_FLIGHT'
            )
        $query$, TG_TABLE_SCHEMA)
        INTO effect_in_flight
        USING OLD.organization_id, OLD.epoch;
        IF effect_in_flight THEN
            RAISE EXCEPTION 'leadership advance requires every effect to be resolved' USING ERRCODE = '23514';
        END IF;
        NEW.drain_transaction_id := NULL;
    END IF;
    RETURN NEW;
END;
$$;
