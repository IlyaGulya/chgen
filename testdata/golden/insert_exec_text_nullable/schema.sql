CREATE TABLE audit_log
(
    id        UInt64,
    actor     String,
    note      Nullable(String),
    written_at DateTime64(3)
)
ENGINE = MergeTree ORDER BY id;
