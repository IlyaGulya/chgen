-- Example chgen query source.
--
-- Every feature of the generator is exercised below. Run
--   go run ./cmd/chgen -f examples/chgen.yaml
-- from the repository root to regenerate
-- examples/internal/examplequeries/queries.sql.go.
-- The external row schemas come from external_tables.sql, which is an
-- ordinary schema input in examples/chgen.yaml.


-- 1. Type inference from DDL.
--
-- Plain direct columns need no AS: order_id becomes both the SQL result
-- name and the Go field OrderID. Result Go types come from the DDL, so
-- LowCardinality(String) is a string, Nullable(String) is a *string,
-- Array(String) is []string and Map(String, String) is map[string]string.
--
-- LIMIT chgen.arg('Limit') names the parameter at the source level; the
-- generator lowers it to a positional ? in the runtime SQL.
-- name: ListOrders :many
SELECT
    order_id,
    customer_id,
    country,
    status,
    item_count,
    total_amount,
    discount_code,
    tag_list,
    attributes,
    placed_at
FROM orders
WHERE country = chgen.arg('Country')
ORDER BY placed_at DESC
LIMIT chgen.arg('Limit')


-- 2. A :one query and a computed expression.
--
-- A computed expression still requires an explicit AS alias; only plain
-- direct columns are named implicitly.
-- name: CountOrdersForCustomer :one
SELECT
    count() AS order_count,
    sum(total_amount) AS total_spend
FROM orders
WHERE customer_id = chgen.arg('CustomerID')


-- 3. A repeated named argument.
--
-- The same name used more than once becomes ONE Go field. The generated
-- wrapper binds the value repeatedly, so the sentinel predicate below takes a
-- single DiscountCode argument.
-- name: ListOrdersByOptionalDiscount :many
SELECT
    order_id,
    total_amount
FROM orders
WHERE (chgen.arg('DiscountCode') = '' OR discount_code = chgen.arg('DiscountCode'))
ORDER BY order_id


-- 4. A -- param: type override.
--
-- The DDL type is not always the desired API. Here a nil pointer means
-- "no limit" and a non-nil pointer supplies the bound, so the Go type is
-- overridden to *uint64 while every other parameter stays inferred.
-- name: DrainOrderEvents :many
-- param: Limit *uint64
SELECT
    order_id,
    event_type,
    occurred_at
FROM order_events
WHERE occurred_at > chgen.arg('Since')
ORDER BY occurred_at
LIMIT ifNull(chgen.arg('Limit'), toUInt64(-1))


-- 5. Aggregate-state columns.
--
-- maxMerge and quantileMerge over AggregateFunction state columns
-- resolve to the underlying value type.
-- name: ListCustomerActivity :many
SELECT
    customer_id,
    maxMerge(last_seen_at_state) AS last_seen_at,
    quantileMerge(0.95)(spend_state) AS p95_spend
FROM customer_activity
WHERE country = chgen.arg('Country')
GROUP BY customer_id
ORDER BY customer_id


-- 6. Request-scoped external tables, name inferred from the schema.
--
-- chgen.external(schema) generates a []OrderedOrderKeysRow field named from
-- the schema. Before execution the wrapper builds ext.Table blocks, attaches
-- them with clickhouse.WithExternalTable, and lowers the source-only table
-- function to the exact table identifier sent on the wire. External inputs are
-- request-scoped: no server-side DDL, no pooled-connection affinity, no
-- cleanup. This is how a large key set is passed without a repeated IN (...).
-- name: ListOrdersForKeys :many
SELECT
    o.order_id,
    o.total_amount
FROM orders AS o
INNER JOIN chgen.external(ordered_order_keys) AS requested
    ON requested.order_id = o.order_id
ORDER BY requested.ordinal


-- 7. Two independent external inputs that reuse one row schema.
--
-- The two-argument form names the Go field explicitly while sharing the same
-- row type, so an include set and an exclude set can be sent in one request.
-- name: ListOrdersIncludedExcluded :many
SELECT
    o.order_id,
    o.country
FROM orders AS o
INNER JOIN chgen.external('IncludedKeys', ordered_order_keys) AS included
    ON included.order_id = o.order_id
LEFT JOIN chgen.external('ExcludedKeys', ordered_order_keys) AS excluded
    ON excluded.order_id = o.order_id
WHERE excluded.order_id = ''
ORDER BY included.ordinal


-- 8. A result-capacity hint.
--
-- On a :many query the generated wrapper pre-allocates the result slice from
-- the named slice parameter, which avoids regrowing it while scanning.
-- name: ListOrdersForKeysWithHint :many
-- result-capacity: RequestedKeys
SELECT
    o.order_id,
    o.status
FROM orders AS o
INNER JOIN chgen.external('RequestedKeys', ordered_order_keys) AS requested
    ON requested.order_id = o.order_id
ORDER BY requested.ordinal


-- 9. A relation CTE and an aliased derived table.
--
-- Non-recursive relation CTEs, scalar WITH aliases, scalar subqueries and
-- aliased derived tables all resolve against the catalog.
-- name: ListTopCountries :many
WITH recent AS (
    SELECT
        country,
        total_amount
    FROM orders
    WHERE placed_at >= chgen.arg('Since')
)
SELECT
    recent.country AS country,
    sum(recent.total_amount) AS revenue,
    count() AS order_count
FROM recent
GROUP BY recent.country
ORDER BY revenue DESC
LIMIT chgen.arg('Limit')


-- 10. A scalar WITH alias and a scalar subquery.
-- name: ListOrdersAboveAverage :many
WITH (
    SELECT avg(total_amount) AS value FROM orders
) AS avg_amount
SELECT
    order_id,
    total_amount,
    (SELECT count() FROM order_events) AS event_total
FROM orders
WHERE total_amount > avg_amount
ORDER BY total_amount DESC
LIMIT chgen.arg('Limit')


-- 11. Fixed command: single-row INSERT ... VALUES.
--
-- On a schema-aware :exec query the target column definitions supply the
-- parameter names and Go types. The Nullable(String) column becomes a *string;
-- generated calls unwrap a non-nil pointer and pass nil for NULL, matching
-- clickhouse-go parameter binding.
-- name: InsertOrderAudit :exec
INSERT INTO order_audit (order_id, actor, note, written_at)
VALUES (chgen.arg('OrderID'), chgen.arg('Actor'), chgen.arg('Note'), chgen.arg('WrittenAt'))


-- 12. Fixed command: ALTER TABLE ... UPDATE.
-- name: UpdateOrderStatus :exec
ALTER TABLE orders
UPDATE status = chgen.arg('Status')
WHERE order_id = chgen.arg('OrderID')


-- 13. Fixed command: ALTER TABLE ... DELETE.
-- name: DeleteOrderEventsBefore :exec
ALTER TABLE order_events
DELETE WHERE occurred_at < chgen.arg('Before')


-- 14. Fixed command: INSERT ... SELECT rollup refresh.
-- name: RefreshCountryRollup :exec
-- param: WindowStart time.Time
INSERT INTO country_rollup (country, order_count, revenue, window_start)
SELECT
    country,
    count() AS order_count,
    sum(total_amount) AS revenue,
    max(placed_at) AS window_start
FROM orders
WHERE placed_at >= chgen.arg('WindowStart')
GROUP BY country


-- 15. Fixed command: ALTER TABLE ... DROP PARTITION.
--
-- A partition expression addresses a partition VALUE, not a table column, so
-- the placeholder type comes from the -- param: annotation.
-- name: DropCountryRollupPartition :exec
-- param: Partition string
ALTER TABLE country_rollup
DROP PARTITION chgen.arg('Partition')


-- 16. Legacy -- result: annotation as a partial override.
--
-- Result rules cover the common ClickHouse function registry. When an
-- expression has no rule yet, or the inferred type is not the desired API,
-- one field can be overridden while every other field stays inferred. Here
-- the JSON payload is exposed as json.RawMessage instead of string.
-- name: ListOrderEventPayloads :many
-- result: Payload payload json.RawMessage
SELECT
    order_id,
    payload,
    occurred_at
FROM order_events
WHERE order_id = chgen.arg('OrderID')
ORDER BY occurred_at


-- 17. The chgen.arg scanner is lexical, not a regular expression.
--
-- Occurrences inside string literals, quoted identifiers and comments are left
-- untouched. The literal below stays literal text; only the real argument
-- becomes a bound parameter.
-- name: ListOrdersWithLiteralMarker :many
SELECT
    order_id,
    'chgen.arg(NotAParameter)' AS marker, -- chgen.arg('AlsoNotAParameter')
    status
FROM orders
WHERE customer_id = chgen.arg('CustomerID')
ORDER BY order_id
