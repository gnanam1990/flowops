CREATE INDEX ascp_keeper_jobs_runtime_claim_idx
ON ascp_keeper_jobs (keeper_id, gas_payer, chain_id, eligible_after, created_at, job_id)
WHERE state IN ('QUEUED','PREPARED','BROADCASTING','TIMED_OUT','REORGED');

CREATE INDEX ascp_keeper_jobs_runtime_observation_idx
ON ascp_keeper_jobs (keeper_id, gas_payer, chain_id, updated_at, job_id)
WHERE state IN ('AMBIGUOUS','SUBMITTED','CONFIRMED');
