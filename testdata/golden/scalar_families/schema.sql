CREATE TABLE scalar_probe
(
    id     UUID,
    price  Decimal(18, 4),
    small  Decimal32(2),
    kind   Enum8('a' = 1, 'b' = 2),
    kind16 Enum16('x' = 1000, 'y' = 2000),
    v4     IPv4,
    v6     IPv6,
    nid    Nullable(UUID),
    nprice Nullable(Decimal(18, 4))
)
ENGINE = MergeTree ORDER BY id;
