-- name: Read :many
SELECT id, label, toYYYYMM(at) AS month
FROM events
WHERE id IN (SELECT id FROM chgen.external('Keys', requested))
ORDER BY at, id
SETTINGS optimize_read_in_order=1, max_threads=1;

-- name: Span :one
SELECT minOrNull(at) AS earliest, maxOrNull(at) AS latest
FROM events
WHERE id = chgen.arg('ID');
