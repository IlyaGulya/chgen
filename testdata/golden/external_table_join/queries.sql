-- A request-scoped external table becomes a Go slice parameter and a
-- clickhouse.WithExternalTable option.
-- name: ReadIncludedEvents :many
SELECT events.id AS id, events.payload AS payload
FROM events
INNER JOIN chgen.external('IncludedKeys', ordered_string_keys) AS included
    ON included.id = events.id
ORDER BY included.ordinal;
