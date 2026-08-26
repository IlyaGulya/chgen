-- The sized constructors take their result type from their own name, and
-- the *OrNull variant adds Nullable. The wide integer constructors are in
-- refuse_wide_integer_result, because Int128 has no Go mapping.
-- name: ReadConstructors :one
SELECT
    toFixedString(s, 8)    AS fixed_string,
    toDecimal64(i32, 4)    AS decimal_from_int,
    toDecimal32(u32, 2)    AS small_decimal,
    toInt32OrNull(s)       AS int_or_null,
    toInt32OrZero(s)       AS int_or_zero,
    toInt64(lc)            AS from_low_cardinality,
    toUInt64OrNull(ns)     AS from_nullable
FROM constructor_probe
WHERE id = chgen.arg('ID');
