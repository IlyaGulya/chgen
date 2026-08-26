-- The Nullable wrapper must become a Go pointer, and LowCardinality must
-- disappear from the Go type without removing Nullable under it.
-- name: ReadWrappers :one
SELECT
    s     AS plain,
    ns    AS nullable,
    lc    AS low_cardinality,
    lcn   AS low_cardinality_nullable,
    ni32  AS nullable_int,
    arr   AS nullable_array_element,
    attrs AS nullable_map_value
FROM wrapper_probe
WHERE id = chgen.arg('ID');

-- concat over a LowCardinality column keeps the wrapper on the server but
-- the Go type is still a plain string.
-- name: ReadWrapperExpressions :many
SELECT
    upper(lc)      AS upper_low_cardinality,
    upper(lcn)     AS upper_low_cardinality_nullable,
    ifNull(ns, s)  AS if_null
FROM wrapper_probe
WHERE id = chgen.arg('ID');
