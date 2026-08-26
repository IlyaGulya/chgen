-- An AggregateFunction column has no Go representation: clickhouse-go
-- cannot decode the binary state. Reading it without a -Merge combinator
-- must be refused at generation time, not at run time.
-- name: ReadRawState :one
SELECT max_state AS raw_state
FROM metric_rollup
WHERE day = chgen.arg('Day');
