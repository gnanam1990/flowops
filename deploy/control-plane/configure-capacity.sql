\set ON_ERROR_STOP on

\if :{?max_active_operations}
\else
  \echo 'max_active_operations is required'
  \quit 3
\endif

SELECT :'max_active_operations' ~ '^[1-9][0-9]{0,5}$'
       AND length(:'max_active_operations') <= 6
       AND (:'max_active_operations')::numeric <= 100000 AS valid_capacity \gset
\if :valid_capacity
\else
  \echo 'max_active_operations must be a canonical integer from 1 through 100000'
  \quit 3
\endif

BEGIN;
SELECT active_operations <= (:'max_active_operations')::integer AS capacity_can_change
FROM ascp_capacity_counters
WHERE scope='GLOBAL'
FOR UPDATE \gset
\if :{?capacity_can_change}
\else
  \echo 'GLOBAL capacity row is missing; apply migration 0030 first'
  \quit 3
\endif
\if :capacity_can_change
\else
  \echo 'new maximum is below the current active-operation count; drain first'
  \quit 3
\endif
UPDATE ascp_capacity_counters
SET max_active_operations=(:'max_active_operations')::integer,updated_at=now()
WHERE scope='GLOBAL';
COMMIT;
