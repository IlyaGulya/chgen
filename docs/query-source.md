# Query source reference

`chgen` reads annotated ClickHouse SQL files. It checks each query against the
schema catalog before it generates Go code.

## Query declaration

Each query starts with one name annotation:

```sql
-- name: ListOrders :many
SELECT order_id, total_amount
FROM orders;
```

The query name must be a Go identifier other than `_`. Query names must be
unique across all query files in one generated package.

The command after the name selects the generated method result:

| Command | Generated result |
| --- | --- |
| `:many` | A slice of result rows and an error. |
| `:one` | One result row and an error. |
| `:exec` | An error. |

See [Type mappings and generated API](type-mappings.md) for the exact method,
row, parameter, interface, and mock types.

## Named parameters

Use `chgen.arg('GoName')` to declare a query parameter:

```sql
-- name: ListOrders :many
SELECT order_id, total_amount
FROM orders
WHERE customer_id = chgen.arg('CustomerID')
LIMIT chgen.arg('Limit');
```

The generator changes each source marker to a positional `?` in the run-time
SQL. The name remains in the source and becomes a field in the generated
parameter struct.

Repeated uses of one name create one Go field. The method binds the field at
each source position:

```sql
WHERE chgen.arg('DiscountCode') = ''
   OR discount_code = chgen.arg('DiscountCode')
```

One name must have compatible types at all positions. Numeric coercions and
`Nullable(T)` versus `T` are compatible. Other incompatible uses stop
generation.

A raw positional `?` in a query is an error. Use `chgen.arg('GoName')`
instead. The source scanner does not treat `?` or `chgen.arg` text inside
these regions as a parameter:

- string literals
- quoted identifiers
- line comments
- block comments

## Result names

A direct column supplies its result name without an alias:

```sql
SELECT order_id FROM orders;
```

This column becomes the Go field `OrderID`. A computed expression must have an
explicit SQL alias:

```sql
SELECT sum(total_amount) AS total_amount FROM orders;
```

Generation stops when two SQL result names produce the same Go field name.

## Override annotations

Annotations can set a parameter type, a result field, or the capacity hint for
a `:many` result:

- `-- param: GoName [GoType]` declares one parameter and can set its Go type.
- `-- result: GoName SQLAlias [GoType]` declares one result field and can set
  its type.
- `-- result-capacity: SliceParameter` pre-allocates a `:many` result from a
  slice parameter.

The brackets in the syntax descriptions show that `GoType` is optional. Do
not write the brackets in SQL. A result annotation changes one field. Other
result fields stay inferred.

An explicit Go type must be in the supported override whitelist. It
intentionally replaces the inferred Go type and does not prove that the new
type can transport or scan the inferred ClickHouse shape. The user is
responsible for this choice. See
[Override type boundary](type-mappings.md#override-type-boundary) for the
complete list.

A supported pointer parameter can define an optional bound when the SQL
expression accepts NULL. A nil pointer binds SQL NULL. A non-nil pointer binds
the value:

```sql
-- name: DrainOrderEvents :many
-- param: Limit *uint64
SELECT event_id
FROM order_events
LIMIT ifNull(chgen.arg('Limit'), toUInt64(-1));
```

The `-- result-capacity:` annotation is valid only for a `:many` query and
must name a slice parameter.

## Database selection

The ClickHouse database comes from the connection, for example from
`clickhouse.Options.Auth.Database`. Queries must use unqualified table names:

```sql
SELECT order_id FROM orders;
```

A qualified name such as `metrics.orders` is an error. A database name in SQL
can disagree with the database selected by the connection.

## Request-scoped external tables

Use an external table when a request supplies a large row set. The generated
method sends these rows through the ClickHouse native protocol. It does not
create a server table, use one pooled connection for later cleanup, or run
server-side DDL.

### Declare a row schema

Put the marker `-- chgen:external` before a `CREATE TABLE` in any configured
schema input:

```sql
-- chgen:external
CREATE TABLE ordered_order_keys
(
    ordinal  UInt64,
    order_id String
);
```

Only blank lines and other comment lines can occur between the marker and the
`CREATE TABLE`. A marker that has no following `CREATE TABLE` is an error.

The marked declaration is a row type, not executable DDL. It must contain a
bare column list. It must not contain these storage clauses:

- `ENGINE`
- `ORDER BY`
- `PARTITION BY`
- `PRIMARY KEY`
- `TTL`

An `ALTER TABLE` cannot target an external schema. Edit the marked declaration
instead.

### Use the row schema

The short form derives the Go parameter name from the schema name:

```sql
-- name: ListOrdersForKeys :many
SELECT o.order_id
FROM orders AS o
INNER JOIN chgen.external(ordered_order_keys) AS requested
    ON requested.order_id = o.order_id
ORDER BY requested.ordinal;
```

This declaration generates a row type and a slice parameter with this shape:

```go
type OrderedOrderKeysRow struct {
    Ordinal uint64
    OrderID string
}

type ListOrdersForKeysParams struct {
    OrderedOrderKeys []OrderedOrderKeysRow
}
```

Use the two-argument form when one query needs more than one input with the
same row schema:

```sql
INNER JOIN chgen.external('IncludedKeys', ordered_order_keys) AS included
    ON included.order_id = o.order_id
LEFT JOIN chgen.external('ExcludedKeys', ordered_order_keys) AS excluded
    ON excluded.order_id = o.order_id
```

The first argument must be an exported Go identifier. It sets the generated
slice field name. Each external input gets a separate request-scoped wire
table.

### Catalog and collision rules

External schemas and physical tables are separate catalogs:

- `FROM orders` reads a physical server table.
- `chgen.external(order_keys)` uses a marked external row schema.
- A direct table reference never becomes an external Go input.
- An unmarked physical table cannot be used through `chgen.external`.

Generation stops for an unknown external schema, an external and physical
schema with the same name, a wire-table name collision, incompatible reuse of
one parameter name, duplicate generated column names, or an unsupported
column type.

## Fixed `:exec` commands

A schema-aware `:exec` query supports these fixed command forms:

- `INSERT ... VALUES` with exactly one row
- `INSERT ... SELECT`
- `ALTER TABLE ... UPDATE`
- `ALTER TABLE ... DELETE`
- `ALTER TABLE ... DROP PARTITION`

The target table supplies column names and types where the command has a
column context:

```sql
-- name: InsertOrderAudit :exec
INSERT INTO order_audit (order_id, actor, note, written_at)
VALUES (
    chgen.arg('OrderID'),
    chgen.arg('Actor'),
    chgen.arg('Note'),
    chgen.arg('WrittenAt')
);
```

For the `conn.Exec` path, the generated method converts a nil pointer to an
untyped nil and dereferences a non-nil pointer. A native one-row batch passes
the pointer directly to `batch.Append`; the driver handles both nil and
non-nil pointers safely. See
[Nullable parameters and typed nil values](type-mappings.md#nullable-parameters-and-typed-nil-values)
for the driver boundary.

A partition expression describes a partition value, not a table column. A
`DROP PARTITION` parameter therefore needs an explicit type annotation:

```sql
-- name: DropRollupPartition :exec
-- param: Partition uint32
ALTER TABLE rollups
DROP PARTITION chgen.arg('Partition');
```

This annotation supplies a Go type but no inferred ClickHouse type. A
`time.Time` parameter in this position does not get a generated temporal range
guard.

A one-row `INSERT ... VALUES` whose tuple contains only bare parameters uses
the native batch protocol. It still sends exactly one row per method call.
An expression around a parameter uses `conn.Exec`. When a parameter has an
inferred temporal `CHType`, the generated method applies its range guard before
either execution path.

Use a hand-written batch loader for a multi-row insert. Use a hand-written
builder for a dynamic aggregate-state fold. These operations have different
performance or state requirements from a fixed typed query.

## Deliberate query limits

`chgen` is not a complete ClickHouse semantic type checker. An unknown
function or an expression without a type rule stops generation. It does not
produce `any` as a fallback.

The supported relation forms include:

- non-recursive relation CTEs
- scalar `WITH` aliases
- scalar subqueries
- `EXISTS` subqueries
- uncorrelated `IN` subqueries
- aliased derived tables
- ordinary joins

A scalar subquery can refer to an outer row. An `EXISTS` subquery can also
refer to an outer row.

These relation forms are refused:

- a correlated `IN` subquery
- a lateral or correlated derived table
- a scalar subquery with a correlated `LIMIT`

Whole Tuple result columns are always refused. A result annotation does not
bypass this refusal. Select supported Tuple elements as separate scalar result
columns instead. Functions without a type rule stay refused until the resolver
has a measured rule.

For an aggregate whose result type is determined by its first argument,
`chgen` does not type-check the ordering argument. This limit includes forms
such as `argMax(value, tuple(...))`.

Raw `AggregateFunction` state columns cannot cross the native protocol. Merge
or finalize the state before it becomes a Go result. See
[Explicit type refusals](type-mappings.md#explicit-type-refusals) for all
type-family limits.

The [ClickHouse support manifest](clickhouse-support-manifest.md) gives the
evidence-backed status of the measured API surface. The
[SQL front-end gaps](front-end-gaps.md) page gives additional parser and
resolver boundaries.

## Related reference

- [Configuration reference](configuration.md)
- [Type mappings and generated API](type-mappings.md)
- [ClickHouse support manifest](clickhouse-support-manifest.md)
- [Runnable examples](../examples/)
