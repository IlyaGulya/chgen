CREATE TABLE events (id UInt32, at DateTime64(3, 'UTC')) ENGINE=MergeTree ORDER BY id;
CREATE TABLE staged (id UInt64, at DateTime64(3, 'UTC')) ENGINE=MergeTree ORDER BY (at, id);
EXCHANGE TABLES events AND staged;
RENAME TABLE staged TO archived;
DROP TABLE archived;
ALTER TABLE events ADD COLUMN label String;
ALTER TABLE events ADD INDEX by_label label TYPE bloom_filter GRANULARITY 1;
ALTER TABLE events MATERIALIZE INDEX by_label;
ALTER TABLE events ADD PROJECTION by_id (SELECT id ORDER BY id);
ALTER TABLE events MATERIALIZE PROJECTION by_id;
-- chgen:external
CREATE TABLE requested (id UInt64);
