-- sum() over a String is ILLEGAL_TYPE_OF_ARGUMENT on the server. A rule
-- that gave this call a type would generate Go that compiles and then
-- fails at run time, thus the generator refuses it.
-- name: SumLabels :one
SELECT sum(label) AS total
FROM domain_probe;
