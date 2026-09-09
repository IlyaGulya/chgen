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

A `:one` method still returns an error when the result is empty, but generated
packages export `ErrNoRows` so callers can distinguish absence from a query or
driver failure with `errors.Is(err, querygen.ErrNoRows)`. The rendered error
text remains `<QueryName>: no rows`.

The command after the name selects the generated method result:

| Command | Generated result |
| --- | --- |
| `:many` | A slice of result rows and an error. |
| `:one` | One result row and an error. |
| `:exec` | An error. |

See [Type mappings and generated API](type-mappings.md) for the exact method,
row, parameter, interface, and mock types.

### Empty aggregate input is not an empty result

A global aggregate (without `GROUP BY`) can return one row even when no source
rows match. For a non-Nullable DateTime64 column, `min` and `max` return a
non-Nullable timestamp at Unix epoch on empty input. A genuine epoch timestamp
has exactly the same value and type. `:one` therefore cannot turn this into
`ErrNoRows`, and changing only the Go field to a pointer would not produce nil.

Use explicit nullable aggregates when absence must be represented in the row:

```sql
-- name: FoldPayloadTimeOrderedSourceSpan :one
SELECT minOrNull(occurred_at) AS min_occurred_at,
       maxOrNull(occurred_at) AS max_occurred_at
FROM source_spans
WHERE scope = chgen.arg('Scope');
```

For `occurred_at DateTime64(3, 'UTC')`, both results are
`Nullable(DateTime64(3, 'UTC'))` and generate `*time.Time` fields. Empty input
produces nil; a real epoch timestamp produces a non-nil pointer. No result-type
annotation is needed. The same explicit `-OrNull` choice applies to supported
`sum`, `avg`, `argMin`, and `argMax` forms. See the
[ClickHouse -OrNull contract](https://clickhouse.com/docs/reference/functions/aggregate-functions/combinators#-ornull).

Alternatively, keep `min`/`max` and add `HAVING count() > 0` to suppress the
global aggregate row on empty input. Then `:one` returns `ErrNoRows`. A `:many`
method follows the same SQL: a global aggregate yields one row; suppressing it
with HAVING yields an empty slice.

An ordinary `GROUP BY scope` emits no group for a missing scope, but GROUP BY
alone does not guarantee that every aggregate consumed a value. For example,
`minIfOrNull(value, condition)` can return NULL inside an existing group when
the condition matches no rows. Nullability follows the SQL expression, not
simply the presence or absence of GROUP BY.

Generated `chgenCheckDateTime64ScanRange` checks for driver range overflow; it
is **not an absence check**. It accepts Unix epoch and remains enabled for
aggregates, which can also return timestamps outside the driver's range. This
was named `chgenCheckScannedTime` before v0.1.9. chgen never rewrites `min` into
`minOrNull` or treats epoch as NULL. Updating chgen alone does not change the
empty-input behavior of an existing SQL query.

## Unchecked SETTINGS

`optimize_read_in_order` and `max_threads` are built-in settings. For example,
`SETTINGS optimize_read_in_order = 1, max_threads = 1` works without an opt-in.
See the [measured SETTINGS roster](statement-query-conformance.md#measured-settings)
for built-in literal domains.

For a setting outside that roster, opt in for one named query:

```sql
-- name: ReadFirstOrder :one
-- chgen:unchecked-setting max_rows_to_read
SELECT order_id, total_amount
FROM orders
ORDER BY order_id
LIMIT 1
SETTINGS max_rows_to_read = 100000;
```

This is an explicit escape hatch, **not semantic validation**. The resolver
ignores the named unknown setting when inferring types; the generated SQL
retains it unchanged. The caller must verify server support, valid values,
and that the setting does not invalidate inferred parameter or result types.
Do not use it to enable type-changing behavior such as `join_use_nulls`
without a matching resolver model. Successful generation does not prove that
an unchecked setting is valid or type-neutral.

The annotation goes after `-- name:` and before the SQL body. Repeat it for
each setting. Names are exact lowercase identifiers; wildcards and a global
allow-all switch are not supported. It applies to all SETTINGS clauses within
that query, including nested queries and INSERT SELECT, but not the next
query or another file. Duplicate annotations and names absent from the SQL
are errors. Values must remain unsigned integer (UInt64 range), Boolean, or
string literals; parameters and expressions are not allowed.

Built-in validation always wins: annotating `max_block_size` cannot allow
`max_block_size = 0`. If a future release adds a rule for an opted-in name,
that rule takes effect automatically. Other unknown settings still fail.
The annotation does not bypass SQL parsing, column resolution, unsupported
functions, DDL restrictions, or any non-SETTINGS validation.

## Explicit ClickHouse result contracts

`-- result:` controls Go field names and representation; it does not bypass
ClickHouse type inference. Starting with v0.1.8, an unregistered function can
declare its output contract separately:

```sql
-- name: ReadMonths :many
-- result-chtype: month UInt32
-- result: Month month uint32
SELECT clientMonthFunction(occurred_at) AS month
FROM events;
```

The `-- result:` line is optional. The CH contract supplies the generated Go
type when no Go override is given. The function remains unregistered: chgen
does not claim to have proved its semantics. The SQL is not cast or rewritten.
The generated method checks server column names and the asserted types before
scanning, even for empty results. A mismatch or missing metadata is an error;
rows are closed normally. No extra server query is issued.

Contracts apply to unique explicit output aliases in an ordinary outer SELECT
(:one or :many). They must be in the query header. Duplicate, unused, ambiguous,
and misplaced contracts are errors. A contract cannot contradict an inferred
type, drop Nullable, introduce an unsupported Go mapping, or bypass an invalid
known function call. Type syntax may contain spaces, for example
`Nullable(DateTime64(3, 'UTC'))`.

When inference is unavailable, v0.1.8 accepts direct unregistered function
calls, including nested unregistered calls with independently valid arguments.
It does not use a final output contract to guess the operand types of a known
function or operator. Parsing, column resolution and parameter validation still
run. An input parameter without inferable type still needs `-- param:`; a result
contract does not type inputs. Types unavailable inside CTEs, subqueries or
aliases used by other clauses are not supplied by an outer result contract.
Star expansion, set-operation contracts, unknown parametric functions and
contracts on :exec remain unsupported. Independently typed ORDER BY expressions
are permitted.

Runtime type comparison ignores inter-token whitespace, but preserves wrappers,
parameters, quoted contents and token boundaries. It does not assume type
aliases or timezone spellings are equivalent. Unannotated queries retain their
existing generated behavior.

The numeric date-key functions `toYYYYMM`, `toYYYYMMDD`, and
`toYYYYMMDDhhmmss` need no contracts: their measured one-argument forms return
UInt32, UInt32, and UInt64 respectively, with measured wrapper propagation.
Their optional timezone form remains outside the registry's one-argument
boundary. A bare NULL produces Nullable(Nothing) on the server and has no Go
result representation; it must not be advertised as a typed integer result.

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
