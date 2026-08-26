-- Supported `ALTER TABLE ... ADD COLUMN` delta.
--
-- The catalog applies these in manifest order, so generated types match the
-- schema that is actually applied to ClickHouse. Unmodelled schema operations
-- are rejected rather than silently ignored.
ALTER TABLE orders ADD COLUMN channel LowCardinality(String) AFTER country;
