-- name: ReadScalarFamilies :one
SELECT
    id     AS uuid_value,
    price  AS decimal_value,
    small  AS small_decimal,
    kind   AS enum8_value,
    kind16 AS enum16_value,
    v4     AS ipv4_value,
    v6     AS ipv6_value,
    nid    AS nullable_uuid,
    nprice AS nullable_decimal
FROM scalar_probe
WHERE id = chgen.arg('ID');
