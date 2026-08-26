CREATE TABLE domain_probe
(
    id    UInt64,
    label String
)
ENGINE = MergeTree ORDER BY id;
