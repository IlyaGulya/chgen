CREATE TABLE metric_events
(
    day    Date,
    kind   LowCardinality(String),
    value  UInt32,
    amount Float64
)
ENGINE = MergeTree ORDER BY (day, kind);

CREATE TABLE metric_rollup
(
    day        Date,
    max_state  AggregateFunction(max, UInt32),
    sum_state  AggregateFunction(sum, Float64),
    uniq_state AggregateFunction(uniq, String)
)
ENGINE = AggregatingMergeTree ORDER BY day;
