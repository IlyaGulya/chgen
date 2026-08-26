CREATE TABLE wrapper_probe
(
    id     UInt64,
    s      String,
    ns     Nullable(String),
    lc     LowCardinality(String),
    lcn    LowCardinality(Nullable(String)),
    ni32   Nullable(Int32),
    arr    Array(Nullable(String)),
    attrs  Map(String, Nullable(UInt64))
)
ENGINE = MergeTree ORDER BY id;
