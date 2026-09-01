# chgen

Generate type-safe ClickHouse queries for Go from SQL and DDL.

## Quick start

The command requires Go 1.24 or later. Add it to your module, create the
initial files, and generate the Go package:

```sh
go get -tool github.com/IlyaGulya/chgen/cmd/chgen@latest
go tool chgen init
go tool chgen
```

The `init` command creates `chgen.yaml`, `schema.sql`, and `queries.sql` in the
current directory. It refuses to replace any of these files.

## From SQL to Go

Start with a ClickHouse schema:

`schema.sql`:

```sql
CREATE TABLE orders
(
    order_id     String,
    customer_id  String,
    total_amount Decimal(18, 2),
    placed_at    DateTime64(3)
)
ENGINE = MergeTree
ORDER BY (customer_id, order_id);
```

Add a named query with named parameters:

`queries.sql`:

```sql
-- name: ListOrders :many
SELECT
    order_id,
    total_amount,
    placed_at
FROM orders
WHERE customer_id = chgen.arg('CustomerID')
ORDER BY placed_at DESC
LIMIT chgen.arg('Limit')
```

Configure the generated package:

`chgen.yaml`:

```yaml
version: 1
packages:
  - name: gen
    output: internal/gen/queries.sql.go
    schema: schema.sql
    queries: queries.sql
```

Run the generator after you change the schema or a query:

```sh
go tool chgen
```

The generated API has this shape:

```go
type ListOrdersParams struct {
    CustomerID string
    Limit      uint64
}

type ListOrdersRow struct {
    OrderID     string
    TotalAmount decimal.Decimal
    PlacedAt    time.Time
}

type Querier interface {
    ListOrders(context.Context, ListOrdersParams) ([]ListOrdersRow, error)
}

type MockQuerier struct {
    ListOrdersFunc func(context.Context, ListOrdersParams) ([]ListOrdersRow, error)
}
```

Use an existing native `driver.Conn` from `clickhouse-go`:

```go
queries := gen.New(conn)

orders, err := queries.ListOrders(ctx, gen.ListOrdersParams{
    CustomerID: customerID,
    Limit:      100,
})
if err != nil {
    return err
}
```

The generated method contains the SQL text, argument order, value guards, and
row scan. Your application supplies the connection and database selection.

See the runnable example in
[`examples/quickstart/`](examples/quickstart/).

## Command options

Use `-f` for another configuration path. Use `-version` to print the command
version:

```sh
go tool chgen -f path/to/chgen.yaml
go tool chgen -version
```

You can also install the command outside a module:

```sh
go install github.com/IlyaGulya/chgen/cmd/chgen@latest
chgen
```

Commit the generated file. Check it for changes after generation:

```sh
go tool chgen
git diff --exit-code
```

Programs can also import `github.com/IlyaGulya/chgen` and call `LoadConfig`,
the parse functions, `Generate`, or `Run`.

## Compatibility

The `chgen` command requires Go 1.24 or later. Generated packages support
these driver combinations:

| Minimum Go version | `clickhouse-go` |
| --- | --- |
| Go 1.24 | v2.42.0 |
| Go 1.25 | v2.47.0 |

## Input processing

One run:

1. Reads and validates `chgen.yaml`.
2. Reads schema and query inputs in configuration order.
3. Applies supported `CREATE TABLE`, `DROP TABLE`, and `ALTER TABLE` statements.
4. Resolves query parameters and result types.
5. Generates all configured packages.

Relative paths start at the directory that contains `chgen.yaml`. A schema or
query input can be a file, a directory, or a glob. Input order is significant
because migrations change the schema in that order.

See [Configuration](docs/configuration.md) for path, glob, collision, file
name, and write rules.

## Query annotations

Each query starts with `-- name: GoName :one`, `:many`, or `:exec`.

Use `chgen.arg('GoName')` for a parameter. The generator changes it to a
positional `?` in the query. Repeated uses of one name create one Go field and
bind that field at each position.

Use an annotation when you want another type or field name:

| Annotation | Purpose |
| --- | --- |
| `-- param: GoName [GoType]` | Set one parameter type. |
| `-- result: GoName SQLAlias [GoType]` | Set one result field. |
| `-- result-capacity: SliceParameter` | Pre-allocate a `:many` result slice. |

A computed result expression needs an SQL alias. A direct column does not.
Raw positional parameters are not allowed in schema-aware query sources.
See [Query source format](docs/query-source.md) for all annotations and command
forms.

## Features

### Generated application API

For each query, `chgen` generates the applicable structs, SQL constant, and
method on `*Queries`. It also generates `Querier` for services, `MockQuerier`
for tests, and `New` for a native `clickhouse-go/v2` connection.

### Schema changes in input order

The schema reader supports `ALTER TABLE ... ADD COLUMN`, `MODIFY COLUMN`, and
`DROP COLUMN`. It applies these changes in configuration order. It skips the
`schema_migrations` table that `golang-migrate` uses.

### Request-scoped external tables

Mark a schema with `-- chgen:external`. Use it with
`chgen.external(schema_name)` in a query. The generated method sends the rows
for that request. The rows exist only for the request, and no table cleanup is
required.

See [Query source format](docs/query-source.md) for the schema marker and both
external-table call forms.

### Value guards

Generated methods validate nullable and temporal values.

See [Type mappings](docs/type-mappings.md) for Go types, container rules,
driver behavior, and temporal limits.

## Support and limits

`chgen` supports a defined set of ClickHouse types, functions, query forms,
and fixed commands. An unknown function or an unsupported form causes a
generation error.

Supported query forms include joins, non-recursive relation CTEs, scalar
`WITH` aliases, scalar subqueries, `EXISTS`, uncorrelated `IN` subqueries, and
aliased derived tables. A scalar or `EXISTS` subquery can refer to an outer
row.

Current limits include:

- Whole `Tuple` result columns are not supported. Select supported tuple
  elements instead.
- Functions without a type rule are not supported.
- Correlated `IN` subqueries and lateral derived tables are not supported.
- Fixed `INSERT ... VALUES` supports one row per method call.
- Fixed `ALTER` commands cover the update, delete, and partition forms in
  [Query source format](docs/query-source.md).
- Qualified `database.table` names are not supported because the connection
  selects the database.

Use a hand-written query or builder when `chgen` does not support the required
form.

## Documentation

- [Configuration](docs/configuration.md)
- [Query source format](docs/query-source.md)
- [Type mappings and driver behavior](docs/type-mappings.md)
- [ClickHouse support](docs/clickhouse-support-manifest.md)
- [SQL support limits](docs/front-end-gaps.md)

## Development

Run the project checks before you send a change:

```sh
./scripts/verify.sh
```

## License

MIT. See [LICENSE](LICENSE). See
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) for third-party license
information.
