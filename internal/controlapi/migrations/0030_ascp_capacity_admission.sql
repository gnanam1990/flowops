CREATE TABLE IF NOT EXISTS ascp_capacity_counters (
    scope text PRIMARY KEY CHECK (scope='GLOBAL'),
    max_active_operations integer NOT NULL CHECK (max_active_operations BETWEEN 1 AND 100000),
    active_operations integer NOT NULL CHECK (active_operations BETWEEN 0 AND max_active_operations),
    updated_at timestamptz NOT NULL
);

CREATE TABLE IF NOT EXISTS ascp_capacity_admissions (
    operation_id text PRIMARY KEY REFERENCES ascp_intents(operation_id),
    reservation_id text NOT NULL UNIQUE REFERENCES ascp_budget_reservations(reservation_id),
    scope text NOT NULL DEFAULT 'GLOBAL' REFERENCES ascp_capacity_counters(scope),
    state text NOT NULL CHECK (state IN ('ACTIVE','RELEASED')),
    acquired_at timestamptz NOT NULL,
    released_at timestamptz,
    release_reservation_state text,
    CHECK (
      (state='ACTIVE' AND released_at IS NULL AND release_reservation_state IS NULL) OR
      (state='RELEASED' AND released_at IS NOT NULL AND release_reservation_state IN (
        'CONSUMED_ON_RELEASE','RESTORED_ON_REFUND','RELEASED','RELEASED_AFTER_EXPIRY_PROOF'
      ))
    )
);

DO $$
DECLARE active_count integer;
BEGIN
    SELECT count(*) INTO active_count
    FROM ascp_budget_reservations
    WHERE state IN ('RESERVED','AUTHORIZATION_LIVE','COMMITTED_SAFE','COMMITTED_FINALIZED','REORGED_BACK');
    IF active_count > 1000 THEN
        RAISE EXCEPTION 'active ASCP operations exceed the initial capacity limit; drain before migration';
    END IF;
    INSERT INTO ascp_capacity_counters(scope,max_active_operations,active_operations,updated_at)
    VALUES ('GLOBAL',1000,active_count,now())
    ON CONFLICT (scope) DO NOTHING;
END;
$$;

INSERT INTO ascp_capacity_admissions(operation_id,reservation_id,state,acquired_at)
SELECT operation_id,reservation_id,'ACTIVE',created_at
FROM ascp_budget_reservations
WHERE state IN ('RESERVED','AUTHORIZATION_LIVE','COMMITTED_SAFE','COMMITTED_FINALIZED','REORGED_BACK')
ON CONFLICT (operation_id) DO NOTHING;

CREATE OR REPLACE FUNCTION ascp_acquire_capacity(
    requested_operation_id text,
    requested_reservation_id text,
    expected_max_active integer,
    acquired_at_value timestamptz
) RETURNS text
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $$
DECLARE configured_max integer;
DECLARE updated_count integer;
BEGIN
    IF requested_operation_id IS NULL OR requested_reservation_id IS NULL OR
       expected_max_active < 1 OR expected_max_active > 100000 OR acquired_at_value IS NULL THEN
        RETURN 'INVALID';
    END IF;
    SELECT max_active_operations INTO configured_max
    FROM public.ascp_capacity_counters WHERE scope='GLOBAL';
    IF configured_max IS NULL OR configured_max <> expected_max_active THEN
        RETURN 'LIMIT_MISMATCH';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM public.ascp_budget_reservations r
        WHERE r.operation_id=requested_operation_id AND r.reservation_id=requested_reservation_id
          AND r.state='RESERVED'
    ) THEN
        RETURN 'RESERVATION_MISMATCH';
    END IF;
    UPDATE public.ascp_capacity_counters
    SET active_operations=active_operations+1,updated_at=acquired_at_value
    WHERE scope='GLOBAL' AND max_active_operations=expected_max_active
      AND active_operations<max_active_operations
    RETURNING active_operations INTO updated_count;
    IF updated_count IS NULL THEN
        RETURN 'EXHAUSTED';
    END IF;
    INSERT INTO public.ascp_capacity_admissions(operation_id,reservation_id,state,acquired_at)
    VALUES (requested_operation_id,requested_reservation_id,'ACTIVE',acquired_at_value);
    RETURN 'ACQUIRED';
EXCEPTION
    WHEN unique_violation THEN RETURN 'CONFLICT';
END;
$$;

CREATE OR REPLACE FUNCTION flowops_release_capacity_on_reservation_terminal()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $$
DECLARE released_rows integer;
BEGIN
    IF OLD.state IN ('RESERVED','AUTHORIZATION_LIVE','COMMITTED_SAFE','COMMITTED_FINALIZED','REORGED_BACK')
       AND NEW.state IN ('CONSUMED_ON_RELEASE','RESTORED_ON_REFUND','RELEASED','RELEASED_AFTER_EXPIRY_PROOF') THEN
        UPDATE public.ascp_capacity_admissions
        SET state='RELEASED',released_at=now(),release_reservation_state=NEW.state
        WHERE reservation_id=NEW.reservation_id AND state='ACTIVE';
        GET DIAGNOSTICS released_rows = ROW_COUNT;
        IF released_rows <> 1 THEN
            RAISE EXCEPTION 'terminal reservation lacks exactly one active capacity admission' USING ERRCODE='23514';
        END IF;
        UPDATE public.ascp_capacity_counters
        SET active_operations=active_operations-1,updated_at=now()
        WHERE scope='GLOBAL' AND active_operations>0;
        IF NOT FOUND THEN
            RAISE EXCEPTION 'capacity counter underflow' USING ERRCODE='23514';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS ascp_budget_reservations_release_capacity ON ascp_budget_reservations;
CREATE TRIGGER ascp_budget_reservations_release_capacity
AFTER UPDATE OF state ON ascp_budget_reservations
FOR EACH ROW EXECUTE FUNCTION flowops_release_capacity_on_reservation_terminal();

CREATE OR REPLACE FUNCTION flowops_guard_capacity_admission()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP='DELETE' OR OLD.operation_id<>NEW.operation_id OR OLD.reservation_id<>NEW.reservation_id OR
       OLD.scope<>NEW.scope OR OLD.acquired_at<>NEW.acquired_at OR OLD.state<>'ACTIVE' OR NEW.state<>'RELEASED' THEN
        RAISE EXCEPTION 'invalid capacity admission mutation' USING ERRCODE='55000';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS ascp_capacity_admissions_guard ON ascp_capacity_admissions;
CREATE TRIGGER ascp_capacity_admissions_guard BEFORE UPDATE OR DELETE ON ascp_capacity_admissions
FOR EACH ROW EXECUTE FUNCTION flowops_guard_capacity_admission();
DROP TRIGGER IF EXISTS ascp_capacity_admissions_no_truncate ON ascp_capacity_admissions;
CREATE TRIGGER ascp_capacity_admissions_no_truncate BEFORE TRUNCATE ON ascp_capacity_admissions
FOR EACH STATEMENT EXECUTE FUNCTION flowops_reject_immutable_mutation();

REVOKE ALL ON FUNCTION ascp_acquire_capacity(text,text,integer,timestamptz) FROM PUBLIC;
REVOKE ALL ON FUNCTION flowops_release_capacity_on_reservation_terminal() FROM PUBLIC;
