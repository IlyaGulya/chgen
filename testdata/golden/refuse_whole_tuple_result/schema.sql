CREATE TABLE container_probe
(
    id      UInt64,
    tags    Array(String),
    nested  Array(Array(UInt32)),
    attrs   Map(String, UInt64),
    pair    Tuple(Int32, String),
    named   Tuple(a Nullable(Int32), b LowCardinality(String))
)
ENGINE = MergeTree ORDER BY id;
