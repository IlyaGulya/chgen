-- The -Merge combinator reads a state column back to a scalar.
-- name: ReadMergedStates :many
SELECT
    day                    AS day,
    maxMerge(max_state)    AS max_value,
    sumMerge(sum_state)    AS sum_value,
    uniqMerge(uniq_state)  AS uniq_value
FROM metric_rollup
GROUP BY day;

-- The -If combinator keeps the base result type and takes one more
-- argument, the UInt8 condition.
-- name: ReadConditionalAggregates :one
SELECT
    countIf(value > 10)          AS big_count,
    sumIf(amount, value > 10)    AS big_sum,
    avgIf(amount, kind = 'read') AS read_average
FROM metric_events;
