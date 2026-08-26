-- toInt128 gives Int128. The driver has unsafe write and container paths for
-- this type, thus the generator refuses the result.
-- name: ReadWideInteger :one
SELECT toInt128(i32) AS wide_int
FROM constructor_probe
WHERE id = chgen.arg('ID');
