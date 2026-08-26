-- A rollup refresh. The -State values never cross into Go, thus the
-- AggregateFunction type needs no Go mapping here.
-- name: RefreshRollup :exec
INSERT INTO metric_rollup (day, max_state, sum_state, uniq_state)
SELECT
    day,
    maxState(value),
    sumState(amount),
    uniqState(kind)
FROM metric_events
WHERE day >= chgen.arg('Since')
GROUP BY day;
