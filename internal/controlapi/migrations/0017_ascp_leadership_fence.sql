CREATE TABLE ascp_leadership_epochs (
    organization_id text PRIMARY KEY CHECK (
        length(organization_id) BETWEEN 1 AND 200 AND
        organization_id !~ '[[:cntrl:][:space:]]'
    ),
    epoch bigint NOT NULL CHECK (epoch > 0),
    state text NOT NULL CHECK (state IN ('ACTIVE','DRAINING')),
    evidence_digest text NOT NULL CHECK (evidence_digest ~ '^0x[0-9a-f]{64}$'),
    actor text NOT NULL CHECK (actor ~ '^[A-Za-z0-9][A-Za-z0-9._:@/-]{0,127}$'),
    updated_at timestamptz NOT NULL,
    drain_transaction_id bigint,
    CHECK ((state = 'DRAINING') = (drain_transaction_id IS NOT NULL))
);

CREATE TABLE ascp_leadership_events (
    event_id bigserial PRIMARY KEY,
    organization_id text NOT NULL REFERENCES ascp_leadership_epochs(organization_id),
    previous_epoch bigint,
    new_epoch bigint NOT NULL CHECK (new_epoch > 0),
    previous_state text CHECK (previous_state IS NULL OR previous_state IN ('ACTIVE','DRAINING')),
    new_state text NOT NULL CHECK (new_state IN ('ACTIVE','DRAINING')),
    evidence_digest text NOT NULL CHECK (evidence_digest ~ '^0x[0-9a-f]{64}$'),
    actor text NOT NULL CHECK (actor ~ '^[A-Za-z0-9][A-Za-z0-9._:@/-]{0,127}$'),
    created_at timestamptz NOT NULL,
    UNIQUE (organization_id, new_epoch, new_state),
    CHECK (
        (previous_epoch IS NULL AND previous_state IS NULL AND new_epoch = 1 AND new_state = 'ACTIVE') OR
        (previous_epoch = new_epoch AND previous_state = 'ACTIVE' AND new_state = 'DRAINING') OR
        (previous_epoch + 1 = new_epoch AND previous_state = 'DRAINING' AND new_state = 'ACTIVE')
    )
);

CREATE OR REPLACE FUNCTION flowops_validate_leadership_update()
RETURNS trigger LANGUAGE plpgsql AS $$
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
        NEW.drain_transaction_id := NULL;
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION flowops_lock_leadership_organization()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    PERFORM pg_advisory_xact_lock(hashtextextended(NEW.organization_id, 912034761));
    RETURN NEW;
END;
$$;

CREATE TRIGGER ascp_leadership_epochs_lock
BEFORE INSERT OR UPDATE ON ascp_leadership_epochs
FOR EACH ROW EXECUTE FUNCTION flowops_lock_leadership_organization();

CREATE TRIGGER ascp_leadership_epochs_validate_update
BEFORE UPDATE ON ascp_leadership_epochs
FOR EACH ROW EXECUTE FUNCTION flowops_validate_leadership_update();

CREATE TRIGGER ascp_leadership_epochs_no_delete
BEFORE DELETE ON ascp_leadership_epochs
FOR EACH ROW EXECUTE FUNCTION flowops_reject_immutable_mutation();

CREATE OR REPLACE FUNCTION flowops_require_leadership_event()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    matching_event boolean;
BEGIN
    IF TG_OP = 'INSERT' THEN
        SELECT EXISTS (
            SELECT 1 FROM ascp_leadership_events event
            WHERE event.organization_id = NEW.organization_id
              AND event.new_epoch = NEW.epoch
              AND event.new_state = NEW.state
              AND event.evidence_digest = NEW.evidence_digest
              AND event.actor = NEW.actor
              AND event.created_at = NEW.updated_at
              AND event.previous_epoch IS NULL
              AND event.previous_state IS NULL
        ) INTO matching_event;
    ELSE
        SELECT EXISTS (
            SELECT 1 FROM ascp_leadership_events event
            WHERE event.organization_id = NEW.organization_id
              AND event.new_epoch = NEW.epoch
              AND event.new_state = NEW.state
              AND event.evidence_digest = NEW.evidence_digest
              AND event.actor = NEW.actor
              AND event.created_at = NEW.updated_at
              AND event.previous_epoch = OLD.epoch
              AND event.previous_state = OLD.state
        ) INTO matching_event;
    END IF;

    IF NOT matching_event THEN
        RAISE EXCEPTION 'leadership state change requires matching event' USING ERRCODE = '23514';
    END IF;
    RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER ascp_leadership_epochs_require_event
AFTER INSERT OR UPDATE ON ascp_leadership_epochs
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION flowops_require_leadership_event();

CREATE OR REPLACE FUNCTION flowops_validate_leadership_event()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    current_epoch bigint;
    current_state text;
BEGIN
    SELECT epoch, state INTO current_epoch, current_state
    FROM ascp_leadership_epochs
    WHERE organization_id = NEW.organization_id;

    IF NOT FOUND OR current_epoch IS DISTINCT FROM NEW.new_epoch OR
       current_state IS DISTINCT FROM NEW.new_state THEN
        RAISE EXCEPTION 'leadership event does not match current state' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER ascp_leadership_events_validate_insert
BEFORE INSERT ON ascp_leadership_events
FOR EACH ROW EXECUTE FUNCTION flowops_validate_leadership_event();

CREATE TRIGGER ascp_leadership_events_lock
BEFORE INSERT ON ascp_leadership_events
FOR EACH ROW EXECUTE FUNCTION flowops_lock_leadership_organization();

CREATE TRIGGER ascp_leadership_events_append_only
BEFORE UPDATE OR DELETE ON ascp_leadership_events
FOR EACH ROW EXECUTE FUNCTION flowops_reject_immutable_mutation();
