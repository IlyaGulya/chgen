-- A whole Tuple column has no Go mapping. Read the elements one by one,
-- as the containers_array_map_tuple case does.
-- name: ReadWholeTuple :one
SELECT pair AS pair_value
FROM container_probe
WHERE id = chgen.arg('ID');
