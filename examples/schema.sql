-- Example ClickHouse DDL catalog.
--
-- chgen parses this DDL first into a table/column/type catalog, then resolves
-- every query expression against it. A missing table or column therefore fails
-- at generation time instead of becoming a runtime error.
--
-- The domain is a generic "shop" so the example stays independent of any
-- particular product.

CREATE TABLE orders
(
    order_id      String,
    customer_id   String,
    country       LowCardinality(String),
    status        LowCardinality(String),
    item_count    UInt32,
    total_amount  Float64,
    discount_code Nullable(String),
    tag_list      Array(String),
    attributes    Map(String, String),
    placed_at     DateTime64(3),
    placed_date   Date
)
ENGINE = ReplacingMergeTree(placed_at)
PARTITION BY toYYYYMM(placed_date)
ORDER BY (country, customer_id, order_id);

CREATE TABLE order_events
(
    order_id     String,
    event_type   LowCardinality(String),
    payload      String,
    occurred_at  DateTime64(3)
)
ENGINE = MergeTree
ORDER BY (order_id, occurred_at);

-- An AggregateFunction table. chgen understands `maxMerge` and
-- `quantileMerge` over these state columns.
CREATE TABLE customer_activity
(
    customer_id        String,
    country            LowCardinality(String),
    last_seen_at_state AggregateFunction(max, DateTime64(3)),
    spend_state        AggregateFunction(quantile(0.95), Float64)
)
ENGINE = AggregatingMergeTree
ORDER BY (country, customer_id);

-- Target of the fixed-command (:exec) examples.
CREATE TABLE order_audit
(
    order_id   String,
    actor      String,
    note       Nullable(String),
    written_at DateTime64(3)
)
ENGINE = MergeTree
ORDER BY (order_id, written_at);

-- Target of the rollup INSERT ... SELECT and DROP PARTITION examples.
CREATE TABLE country_rollup
(
    country      LowCardinality(String),
    order_count  UInt64,
    revenue      Float64,
    window_start DateTime64(3)
)
ENGINE = ReplacingMergeTree(window_start)
PARTITION BY country
ORDER BY (country, window_start);
