-- Every temporal family reads back as time.Time, but the precision and the
-- timezone differ, thus the generated read guards differ too.
-- name: ReadTemporalColumns :one
SELECT
    d     AS day,
    d32   AS wide_day,
    dt    AS second,
    dtz   AS second_utc,
    dt64  AS milli,
    dtz64 AS micro_utc,
    ndt64 AS nullable_milli
FROM temporal_probe
WHERE id = chgen.arg('ID');

-- INTERVAL arithmetic changes the result family: a Date plus an hour is a
-- DateTime, and a DateTime64 keeps its scale.
-- name: ReadIntervalResults :one
SELECT
    d + INTERVAL 1 DAY     AS day_plus_day,
    d + INTERVAL 1 HOUR    AS day_plus_hour,
    dt + INTERVAL 1 MONTH  AS second_plus_month,
    dt64 - INTERVAL 5 SECOND AS milli_minus_second,
    toStartOfInterval(dt64, INTERVAL 1 HOUR) AS start_of_hour
FROM temporal_probe
WHERE id = chgen.arg('ID');

-- A temporal PARAMETER carries the write guard, because a Go time.Time can
-- hold a value that the column cannot store.
-- name: CountAfter :one
SELECT count() AS total
FROM temporal_probe
WHERE d > chgen.arg('Day') AND dt64 > chgen.arg('Since');
