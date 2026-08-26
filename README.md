# chgen

Typed ClickHouse queries for Go, generated from the SQL and DDL you already
own.

SQL defines the queries. `chgen` reads your schema, checks each query against
that schema, and generates Go methods, parameter types, result types, an
interface, and a test double. It does this offline, without a live ClickHouse
server.

If `chgen` cannot prove a type, it stops with an error. It has no `any` or
guessed-type fallback.

## Why use chgen

- Find unknown tables, columns, parameters, and unsafe types during generation.
- Remove hand-written query bindings, result structs, and `Scan` calls.
- Keep parameter names in SQL with `chgen.arg('Name')`.
- Get a `Querier` interface and `MockQuerier` for unit tests.
- Use ClickHouse types such as `LowCardinality`, `Nullable`, arrays, maps,
  merged aggregate-state results, UUIDs, IP addresses, decimals, and temporal
  values.
- Generate the same result on a developer computer and in CI. A server is not
  necessary during generation.

## From SQL to a Go method

Start with the ClickHouse schema:

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

Write a query with a name, a result kind, and named parameters:

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

Connect these files to one generated package:

`chgen.yaml`:

```yaml
version: 1
packages:
  - name: gen
    output: internal/gen/queries.sql.go
    schema: schema.sql
    queries: queries.sql
```

The command requires Go 1.24 or later.

Pin the generator in your Go module and run it:

```sh
go get -tool github.com/IlyaGulya/chgen/cmd/chgen@latest
go tool chgen
```

`chgen` resolves the parameter and result types from the DDL. It generates an
API with this shape:

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

Call the generated method with an existing native `driver.Conn` from
`clickhouse-go`:

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

The same files are in the runnable generator example in
[`examples/quickstart/`](examples/quickstart/).

## Quick start

The example above shows the minimum source files. Use `-f` for another
configuration path. Use `-version` to print the command version:

```sh
go tool chgen -f path/to/chgen.yaml
go tool chgen -version
```

You can also install the command outside a module:

```sh
go install github.com/IlyaGulya/chgen/cmd/chgen@latest
chgen
```

Commit the generated file. Check it for drift in CI:

```sh
go tool chgen
git diff --exit-code
```

The drift command compares generated output with the committed file. Tests in
[`examples/`](examples/) also exercise the larger feature showcase; they are
not a replacement for the drift command in a consumer project.

## Go and driver compatibility

The `chgen` command and root module require Go 1.24 or later. The `go get
-tool` command and the `go tool` workflow also require a Go 1.24 or later
toolchain.

Adding `chgen` as a tool does not add `clickhouse-go` to the application module
graph through `chgen`. The generated package still uses the driver version
that the application selects.

CI compiles generated code at the minimum Go version for each supported
driver:

| Minimum Go version | `clickhouse-go` | Evidence |
| --- | --- | --- |
| Go 1.24 | v2.42.0 | Generated code compiles. |
| Go 1.25 | v2.47.0 | Generated code compiles. |

Each cell proves the driver at its minimum Go version. A later Go version with
the same supported driver is also included. The generated source uses Go 1.18
language syntax. This syntax does not make other driver lines supported. A
driver line that is not in the table is not supported.

The execution oracle measures runtime behavior only with `clickhouse-go`
v2.47.0. The v2.42.0 check is a compile-only check. It does not prove the same
runtime behavior or safety limits.

A project with a Go 1.18 through Go 1.23 directive can keep that directive.
Install and run `chgen` outside the application module graph:

```sh
GOBIN="$PWD/.bin" go install github.com/IlyaGulya/chgen/cmd/chgen@latest
./.bin/chgen
```

This path runs the command without a change to the application module graph.
It does not give supported generated code to a project that stays on Go 1.18
through Go 1.23. The application must move to a Go and driver pair in the
table before its generated package is supported.

This install needs a Go command that can build with Go 1.24. A Go 1.21 or later
command can switch toolchains when `GOTOOLCHAIN` permits it. An older command
needs a separately installed Go 1.24 or later toolchain, or a binary that was
built with that toolchain. Installation and toolchain switching can need
network access. The project does not publish prebuilt binaries at this time.

## How it works

One run has five main steps:

1. Read and validate `chgen.yaml`.
2. Expand the declared schema and query inputs in a stable order.
3. Build a catalog from `CREATE TABLE` statements and supported `ALTER TABLE`
   statements.
4. Parse each annotated query and resolve its parameters and result types.
5. Generate all configured packages, then replace their output files.

Relative paths start at the directory that contains `chgen.yaml`. A schema or
query input can be a file, a directory, or a glob. Input order is significant
because migrations change the catalog in that order.

`chgen` validates and generates every package first. It then writes and syncs
all complete temporary files before it replaces any output. If a later
`os.Rename` fails during the replacement phase, earlier outputs can already be
replaced.

See [Configuration](docs/configuration.md) for the full path, glob, collision,
file name, and write rules. Programs can also import the public `chgen` package
and call `LoadConfig`, the parse functions, `Generate`, or `Run` directly.

### Why not sqlc

`sqlc` is a good choice for the database engines that it supports. `chgen`
has a separate ClickHouse type resolver, ClickHouse DDL catalog, native-driver
API, external-table support, and measured driver guards. Choose the tool that
matches the database and query forms in your project.

## Query annotations

Each query starts with `-- name: GoName :one`, `:many`, or `:exec`.

Use `chgen.arg('GoName')` for a parameter. The generator changes it to a
positional `?` in the runtime SQL. Repeated uses of one name create one Go
field and bind that field at each position.

Use an annotation when the inferred type or field name is not the API that you
want:

| Annotation | Purpose |
| --- | --- |
| `-- param: GoName [GoType]` | Override one parameter type. |
| `-- result: GoName SQLAlias [GoType]` | Override one result field. |
| `-- result-capacity: SliceParameter` | Pre-allocate a `:many` result slice. |

A computed result expression needs an SQL alias. A direct column does not.
Raw positional parameters are not allowed in schema-aware query sources.
See [Query source format](docs/query-source.md) for all annotations and command
forms.

## Key features

### Generated application API

For each query, `chgen` generates its applicable structs, SQL constant, and
method on `*Queries`. It also generates `Querier` for services, `MockQuerier`
for tests, and `New` for a `clickhouse-go/v2` native connection.

### Schema changes in input order

The catalog supports `ALTER TABLE ... ADD COLUMN`, `MODIFY COLUMN`, and `DROP
COLUMN`. It applies these changes in the order in the configuration. It skips
the `schema_migrations` table used by `golang-migrate`.

### Request-scoped external tables

Mark a schema with `-- chgen:external`, then use it through
`chgen.external(schema_name)` in a query. The generated method sends the rows
through the native protocol for that request. It does not create a server
table and it does not need connection affinity or cleanup.

See [Query source format](docs/query-source.md) for the schema marker and both
external-table call forms.

### Explicit safety checks

The generated methods handle nullable bindings and guard temporal values that
the driver can otherwise change without an error. Type mappings are based on
the behavior of ClickHouse and `clickhouse-go`, not only on type names.

See [Type mappings](docs/type-mappings.md) for exact Go types, container rules,
driver behavior, and temporal limits.

## Support and limits

`chgen` is not a complete ClickHouse semantic type checker. It supports a
measured set of ClickHouse types, functions, query forms, and fixed commands.
An unknown function or an unsupported form causes a generation error.

The main supported query forms include joins, non-recursive relation CTEs,
scalar `WITH` aliases, scalar subqueries, `EXISTS`, uncorrelated `IN`
subqueries, and aliased derived tables. A scalar or `EXISTS` subquery can refer
to an outer row.

Important current limits include:

- whole `Tuple` result columns are refused; select supported tuple elements;
- functions without a type rule are refused;
- correlated `IN` subqueries and lateral derived tables are refused;
- fixed `INSERT ... VALUES` supports one row per method call;
- fixed `ALTER` commands cover the update, delete, and partition forms in
  [Query source format](docs/query-source.md);
- qualified `database.table` names are refused because the connection selects
  the database.

Use a hand-written query or builder when a refused form is the correct tool for
the application. Do not widen a type rule only to make generation pass.

For the evidence-backed coverage status, see the
[ClickHouse support manifest](docs/clickhouse-support-manifest.md). For parser
and resolver limits, see [SQL front-end gaps](docs/front-end-gaps.md). For
external tables and fixed commands, see
[Query source format](docs/query-source.md).

## Documentation

User documentation:

- [Configuration](docs/configuration.md)
- [Query source format](docs/query-source.md)
- [Type mappings and driver behavior](docs/type-mappings.md)
- [ClickHouse support manifest](docs/clickhouse-support-manifest.md)
- [SQL front-end gaps](docs/front-end-gaps.md)

Maintainer documentation:

- [ClickHouse API inventory](docs/clickhouse-api-inventory.md)
- [Design](docs/design.md)
- [CI and oracle baseline runbook](docs/ci-and-the-oracle-baseline.md)

The SQL front-end uses
[`github.com/AfterShip/clickhouse-sql-parser`](https://github.com/AfterShip/clickhouse-sql-parser).
The generated runtime API uses
[`github.com/ClickHouse/clickhouse-go/v2`](https://github.com/ClickHouse/clickhouse-go).

## Contributing

Run the complete offline verification before you send a change:

```sh
./scripts/verify.sh
```

This command checks the supported build tags, package layout, generated files,
examples, static analysis, and an external consumer build. It does not need a
ClickHouse server.

Changes to type rules need live ClickHouse evidence from both the type oracle
and the execution oracle. See the
[oracle baseline runbook](docs/ci-and-the-oracle-baseline.md) before you change
or accept a baseline.

Focused generated-output and refusal cases are in the
[golden corpus](testdata/golden/README.md).

## License

MIT. See [LICENSE](LICENSE). See
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) for ClickHouse measurement and
license attribution.
