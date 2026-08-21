CREATE TABLE IF NOT EXISTS ascp_events (
    sequence bigint PRIMARY KEY CHECK (sequence > 0),
    event_id text NOT NULL UNIQUE CHECK (length(event_id) BETWEEN 8 AND 200),
    organization_id text NOT NULL REFERENCES organizations(id),
    occurred_at_unix_micro bigint NOT NULL CHECK (occurred_at_unix_micro > 0),
    event_type text NOT NULL CHECK (length(event_type) BETWEEN 3 AND 200),
    actor text NOT NULL CHECK (length(actor) BETWEEN 3 AND 200),
    causation_id text CHECK (causation_id IS NULL OR length(causation_id) BETWEEN 8 AND 200),
    correlation_id text NOT NULL CHECK (length(correlation_id) BETWEEN 8 AND 200),
    entity_refs bytea NOT NULL CHECK (octet_length(entity_refs) BETWEEN 2 AND 16384),
    payload bytea NOT NULL CHECK (octet_length(payload) BETWEEN 1 AND 1048576),
    supersedes_event_id text CHECK (supersedes_event_id IS NULL OR length(supersedes_event_id) BETWEEN 8 AND 200),
    previous_hash text NOT NULL CHECK (previous_hash ~ '^[0-9a-f]{64}$'),
    event_hash text NOT NULL UNIQUE CHECK (event_hash ~ '^[0-9a-f]{64}$' AND event_hash <> repeat('0', 64)),
    writer_key_id text NOT NULL CHECK (length(writer_key_id) BETWEEN 8 AND 128),
    writer_mac bytea NOT NULL CHECK (octet_length(writer_mac) = 32)
);

CREATE INDEX IF NOT EXISTS ascp_events_org_sequence_idx
ON ascp_events (organization_id, sequence);

CREATE TABLE IF NOT EXISTS ascp_event_checkpoints (
    checkpoint_id text PRIMARY KEY CHECK (length(checkpoint_id) BETWEEN 8 AND 200),
    last_sequence bigint NOT NULL UNIQUE CHECK (last_sequence > 0),
    last_event_hash text NOT NULL CHECK (last_event_hash ~ '^[0-9a-f]{64}$' AND last_event_hash <> repeat('0', 64)),
    journal_trial_balance_hash text NOT NULL CHECK (journal_trial_balance_hash ~ '^[0-9a-f]{64}$' AND journal_trial_balance_hash <> repeat('0', 64)),
    created_at_unix_micro bigint NOT NULL CHECK (created_at_unix_micro > 0),
    signing_key_id text NOT NULL CHECK (length(signing_key_id) BETWEEN 8 AND 128),
    canonical_document bytea NOT NULL CHECK (octet_length(canonical_document) BETWEEN 1 AND 16384),
    signature bytea NOT NULL CHECK (octet_length(signature) = 64),
    worm_ref text NOT NULL UNIQUE CHECK (length(worm_ref) BETWEEN 8 AND 300),
    FOREIGN KEY (last_sequence) REFERENCES ascp_events(sequence)
);

CREATE OR REPLACE FUNCTION flowops_validate_ascp_event_append()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    current_sequence bigint;
    current_hash text;
BEGIN
    PERFORM pg_advisory_xact_lock(704832801153);
    SELECT sequence, event_hash INTO current_sequence, current_hash
      FROM ascp_events ORDER BY sequence DESC LIMIT 1;
    IF current_sequence IS NULL THEN
        IF NEW.sequence <> 1 OR NEW.previous_hash <> repeat('0', 64) THEN
            RAISE EXCEPTION 'invalid ASCP event genesis' USING ERRCODE = '23514';
        END IF;
    ELSIF NEW.sequence <> current_sequence + 1 OR NEW.previous_hash <> current_hash THEN
        RAISE EXCEPTION 'invalid ASCP event chain append' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION flowops_validate_ascp_checkpoint()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    canonical_hash text;
BEGIN
    SELECT event_hash INTO canonical_hash FROM ascp_events WHERE sequence = NEW.last_sequence;
    IF canonical_hash IS NULL OR canonical_hash <> NEW.last_event_hash THEN
        RAISE EXCEPTION 'ASCP checkpoint does not bind the canonical event' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS ascp_events_validate_append ON ascp_events;
CREATE TRIGGER ascp_events_validate_append
BEFORE INSERT ON ascp_events
FOR EACH ROW EXECUTE FUNCTION flowops_validate_ascp_event_append();

DROP TRIGGER IF EXISTS ascp_event_checkpoints_validate ON ascp_event_checkpoints;
CREATE TRIGGER ascp_event_checkpoints_validate
BEFORE INSERT ON ascp_event_checkpoints
FOR EACH ROW EXECUTE FUNCTION flowops_validate_ascp_checkpoint();

DROP TRIGGER IF EXISTS ascp_events_append_only ON ascp_events;
CREATE TRIGGER ascp_events_append_only
BEFORE UPDATE OR DELETE ON ascp_events
FOR EACH ROW EXECUTE FUNCTION flowops_reject_immutable_mutation();

DROP TRIGGER IF EXISTS ascp_event_checkpoints_append_only ON ascp_event_checkpoints;
CREATE TRIGGER ascp_event_checkpoints_append_only
BEFORE UPDATE OR DELETE ON ascp_event_checkpoints
FOR EACH ROW EXECUTE FUNCTION flowops_reject_immutable_mutation();
