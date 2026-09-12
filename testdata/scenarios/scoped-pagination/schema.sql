CREATE TABLE events (id UInt64) ENGINE=Memory;
CREATE TABLE archived_events (id UInt64) ENGINE=Memory;
-- chgen:external
CREATE TABLE requested_ids (id UInt64);
