-- Reusable request-scoped external-table row schemas.
--
-- Each declaration is marked with -- chgen:external, so it enters the
-- external catalog and never the physical one.
--
-- These are type definitions only. chgen never executes them against
-- ClickHouse; they describe the shape of row sets that the caller sends with
-- the query over the native protocol, so a large key set never has to be
-- expanded into a repeated `IN (...)` binding.

-- chgen:external
CREATE TABLE ordered_order_keys
(
    ordinal  UInt64,
    order_id String
);

-- chgen:external
CREATE TABLE country_filter
(
    country String
);
