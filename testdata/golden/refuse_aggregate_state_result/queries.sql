-- A -State combinator gives an AggregateFunction result. That type has no
-- Go mapping either, thus SELECTing a state is refused for the same reason
-- as reading a state column. A rollup that builds states writes them with
-- INSERT ... SELECT, where no value crosses into Go.
-- name: BuildStates :many
SELECT
    day             AS day,
    maxState(value) AS max_state
FROM metric_events
GROUP BY day;
