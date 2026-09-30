-- name: DeleteStaleRunJobMetricFacts :exec
-- param: RunKeys []string
-- param: RefoldStartedAtUnixMilli int64
DELETE FROM fact_job_metrics_v3
WHERE has(chgen.arg('RunKeys'), concat(repository_owner, '/', repository, '#', toString(github_workflow_run_id), '.', toString(github_workflow_run_attempt)))
  AND toUnixTimestamp64Milli(projected_at) < chgen.arg('RefoldStartedAtUnixMilli')
SETTINGS lightweight_deletes_sync = 2;

-- name: DeleteRunAcrossCluster :exec
DELETE FROM fact_job_metrics_v3 ON CLUSTER 'analytics'
WHERE github_workflow_run_id = ?
SETTINGS mutations_sync = 2;
