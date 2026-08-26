-- Anonymized fact-table DDL used by the manifest test.
CREATE TABLE fact_events_v1
(
    event_date   Date,
    tenant_id    LowCardinality(String),
    event_id     String,
    projected_at DateTime64(3)
)
ENGINE = ReplacingMergeTree(projected_at)
PARTITION BY toYYYYMM(event_date)
ORDER BY (tenant_id, event_id);

CREATE TABLE fact_sessions_v1
(
    session_date Date,
    tenant_id    LowCardinality(String),
    event_id     String,
    projected_at DateTime64(3)
)
ENGINE = ReplacingMergeTree(projected_at)
PARTITION BY toYYYYMM(session_date)
ORDER BY (tenant_id, event_id);
