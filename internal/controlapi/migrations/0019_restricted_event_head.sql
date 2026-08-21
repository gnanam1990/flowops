DO $migration$
DECLARE
    target_schema text := current_schema();
BEGIN
    EXECUTE format($definition$
        CREATE OR REPLACE FUNCTION %1$I.ascp_current_event_head()
        RETURNS TABLE(sequence bigint, event_hash text)
        LANGUAGE sql
        STABLE
        SECURITY DEFINER
        SET search_path = pg_catalog
        AS $body$
            SELECT COALESCE(head.sequence, 0)::bigint,
                   COALESCE(head.event_hash, repeat('0', 64))::text
            FROM (SELECT 1) AS singleton
            LEFT JOIN LATERAL (
                SELECT event.sequence, event.event_hash
                FROM %1$I.ascp_events AS event
                ORDER BY event.sequence DESC
                LIMIT 1
            ) AS head ON true
        $body$
    $definition$, target_schema);
    EXECUTE format(
        'REVOKE ALL PRIVILEGES ON FUNCTION %I.ascp_current_event_head() FROM PUBLIC',
        target_schema
    );
END;
$migration$;
