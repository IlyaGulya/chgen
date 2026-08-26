CREATE TABLE temporal_probe
(
    id    UInt64,
    d     Date,
    d32   Date32,
    dt    DateTime,
    dtz   DateTime('UTC'),
    dt64  DateTime64(3),
    dtz64 DateTime64(6, 'UTC'),
    ndt64 Nullable(DateTime64(3))
)
ENGINE = MergeTree ORDER BY id;
