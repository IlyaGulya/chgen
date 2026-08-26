CREATE TABLE constructor_probe
(
    id   UInt64,
    i32  Int32,
    u32  UInt32,
    s    String,
    ns   Nullable(String),
    lc   LowCardinality(String)
)
ENGINE = MergeTree ORDER BY id;
