CREATE TABLE fact_job_metrics_v3
(
    repository_owner String,
    repository String,
    github_workflow_run_id UInt64,
    github_workflow_run_attempt UInt32,
    projected_at DateTime64(3, 'UTC')
) ENGINE = ReplacingMergeTree
ORDER BY (repository_owner, repository, github_workflow_run_id);
