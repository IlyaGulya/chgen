CREATE TABLE fact_ci_spans_v3
(
    fact_id String,
    repository_key String,
    span_type String,
    span_id String,
    event_date Date,
    projected_at DateTime64(3, 'UTC'),
    label String DEFAULT 'unknown',
    normalized_id String MATERIALIZED lower(fact_id),
    display_id String ALIAS fact_id
)
ENGINE = ReplacingMergeTree(projected_at)
PARTITION BY toYYYYMM(event_date)
ORDER BY (repository_key, span_type, fact_id);

CREATE TABLE fact_ci_spans_v3_sort_key AS fact_ci_spans_v3
ENGINE = ReplacingMergeTree(projected_at)
PARTITION BY toYYYYMM(event_date)
ORDER BY (repository_key, span_type, span_id, fact_id);

CREATE MATERIALIZED VIEW copy_facts TO fact_ci_spans_v3_sort_key
AS SELECT * FROM fact_ci_spans_v3;
INSERT INTO fact_ci_spans_v3_sort_key SELECT * FROM fact_ci_spans_v3;
EXCHANGE TABLES fact_ci_spans_v3 AND fact_ci_spans_v3_sort_key;
RENAME TABLE fact_ci_spans_v3_sort_key TO fact_ci_spans_v3_old;
