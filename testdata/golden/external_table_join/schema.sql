CREATE TABLE events
(
    id      String,
    payload String
)
ENGINE = MergeTree ORDER BY id;
