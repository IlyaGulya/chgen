# chgen

`chgen` generates typed Go query wrappers from annotated ClickHouse SQL.

`chgen` parses DDL into a table, column, and type catalog. It resolves query
expressions against that catalog before it generates Go. A missing table or
column causes a generation error instead of a run-time scan error.

SQL stays the source of truth, the DDL supplies the types, and the generated
API is intentionally narrow.

The SQL front-end is
[`github.com/AfterShip/clickhouse-sql-parser`](https://github.com/AfterShip/clickhouse-sql-parser);
the runtime is
[`github.com/ClickHouse/clickhouse-go/v2`](https://github.com/ClickHouse/clickhouse-go).

## Install

```bash
go install github.com/IlyaGulya/chgen/cmd/chgen@latest
```

The module declares Go 1.26.0 and the Go 1.26.5 toolchain. Code generation does
not need a ClickHouse server. The type rules are verified with ClickHouse
25.8.29.51.

## Verification profiles

The differential type oracle has one current sampling profile:
`current-combined-v1`. It uses the `current-fixture` fixture. Select it with
`CHGEN_ORACLE_PLAN=current-combined-v1` and
`CHGEN_ORACLE_FIXTURE_ID=current-fixture`. The version-boundary CI job uses the
separate `cross-version-common-v1` fixture on both server versions.

The old `CHGEN_ORACLE_GRAMMAR` interface is retired and causes an explicit
refusal. Old grammar reports are archive evidence only. They cannot become
current reports or current baseline cells. See
[the oracle baseline runbook](docs/ci-and-the-oracle-baseline.md) and
[the historical evidence index](docs/historical-oracle-evidence.md).

The [ClickHouse API inventory](docs/clickhouse-api-inventory.md) records the
full function, type family, table function, and setting surface of the pinned
server. It is the source list for measured API coverage.

The [ClickHouse support manifest](docs/clickhouse-support-manifest.md) assigns
one evidence-backed status to every inventory item and produces coverage
counts without reading source-code comments.

The [ClickHouse type specimen catalog](docs/clickhouse-type-specimens.md)
measures a seeded real column for every canonical server type family and feeds
all accepted observed types into the oracle fixture.

The [ClickHouse function probe catalog](docs/clickhouse-function-probes.md)
derives legal calls and reject probes from the function registry and the same
real-column domains. A live gate checks analysis and execution separately.

The [API coverage mutation gate](docs/api-coverage-mutation-gate.md) removes
each coverage signal in turn. Every mutation must make the owning validator
refuse, and attempts never count as accepted cells.

## Usage

`chgen` reads `chgen.yaml` from the working directory and generates every
package the file declares. One run generates all packages.

```bash
chgen                      # read ./chgen.yaml
chgen -f path/to/chgen.yaml
chgen -version
```

| Flag | Meaning |
| --- | --- |
| `-f` | Path to the configuration file. Defaults to `chgen.yaml` in the working directory. |
| `-version` | Print the chgen version and exit. |

### Configuration file

```yaml
version: 1
packages:
  - name: gen                        # Go package name of the output
    output: gen/queries.sql.go       # generated .go file
    queries: queries.sql             # annotated query sources
    schema:                          # DDL sources, applied in order
      - schema.sql
      - migrations                   # directory: ordered *.sql
      - external_tables.sql
```

All relative paths resolve against the directory that holds the configuration
file. Absolute query, schema, and output paths stay absolute. `queries` and
`schema` accept one path or a list of paths; each entry is
a file, a directory, or a glob pattern.

A directory contributes its immediate `*.sql` files, without `*.down.sql`,
sorted byte-wise, so `000001_*.up.sql` migration naming sorts correctly.
Subdirectories are not descended. A glob applies the same filter and sort. An
entry that resolves to nothing is an error; a silent empty input would
generate a wrong catalog.

A `CREATE TABLE` or `ALTER TABLE` whose target is exactly `schema_migrations`
is skipped: that table belongs to `golang-migrate`, not to the domain schema.

Unknown configuration keys are an error, not a warning.

## Examples

[`examples/`](examples/) shows the supported source forms:

| File | Contents |
| --- | --- |
| [`examples/schema.sql`](examples/schema.sql) | The DDL catalog: scalar, `LowCardinality`, `Nullable`, `Array`, `Map` and `AggregateFunction` columns. |
| [`examples/migrations/`](examples/migrations/) | A supported `ALTER TABLE ... ADD COLUMN` delta. |
| [`examples/chgen.yaml`](examples/chgen.yaml) | The configuration file with the DDL and query inputs. |
| [`examples/external_tables.sql`](examples/external_tables.sql) | Reusable request-scoped external-table row schemas. |
| [`examples/queries.sql`](examples/queries.sql) | 17 annotated queries, each one commented with the feature it shows. |
| [`examples/internal/examplequeries/queries.sql.go`](examples/internal/examplequeries/queries.sql.go) | The committed generated output in a private package. |

`go test ./examples` regenerates the output and fails if it differs from the
SQL source.

### Golden corpus

[`testdata/golden/`](testdata/golden/) holds small focused cases
that pin the complete generated file, or the exact refusal text, for each type
family. Where the other tests look for a substring, a golden case makes a
lost code path a visible text difference. It needs no ClickHouse server.
Regenerate with `go test ./internal/engine -run TestGolden -update`, then read the
diff; see [the corpus README](testdata/golden/README.md).

## Source format

A query begins with a `-- name:` annotation that gives the Go name and the
result kind: `:many`, `:one` or `:exec`.

```sql
-- name: ListOrders :many
SELECT
    order_id,
    customer_id,
    total_amount
FROM orders
WHERE country = chgen.arg('Country')
LIMIT chgen.arg('Limit')
```

Result field names and Go types come from the DDL. Plain direct columns do not
need `AS`: `order_id` becomes both the SQL result name and the Go field
`OrderID`. Computed expressions still require an explicit alias.

### Parameters

`chgen.arg('Name')` names a parameter at the source level. The generator lowers
every occurrence to a positional `?` in the runtime SQL. A repeated name
becomes **one** Go field that the wrapper binds repeatedly:

```sql
WHERE (chgen.arg('DiscountCode') = '' OR discount_code = chgen.arg('DiscountCode'))
```

In schema-aware mode a raw positional `?` in the query source is an error.
Write `chgen.arg('Name')` instead, so the parameter name stays in the SQL
source. `?` inside strings, quoted identifiers, and comments is data and is
not affected.

If one named argument is used in incompatible typed contexts, generation fails
rather than silently choosing a type. Numeric coercions and `Nullable(T)`
versus `T` are accepted.

### Annotations

| Annotation | Purpose |
| --- | --- |
| `-- param: GoName [GoType]` | Override the Go type of one argument when the DDL type is not the desired API. |
| `-- result: GoName SQLAlias [GoType]` | Override one result field; the other fields stay inferred. |
| `-- result-capacity: SliceParameter` | On a `:many` query, pre-allocate the result slice from a slice parameter. |

A `*T` override is an optional bound: a nil pointer means "no
limit", a non-nil pointer supplies the value.

```sql
-- name: DrainOrderEvents :many
-- param: Limit *uint64
...
LIMIT ifNull(chgen.arg('Limit'), toUInt64(-1))
```

### The database comes from the connection

Queries name tables without a database qualifier. The database is a property
of the connection (`clickhouse.Options.Auth.Database`), exactly as `sqlc`
takes it from the DSN. Any qualified `db.table` reference is a generation
error, because a name stored in the SQL text can disagree with
the connection.

### Request-scoped external tables

Large row sets must not be expanded into repeated `IN (...)`, `VALUES`, or
ordering-array bindings. Instead, a query declares a row schema and receives
the rows over the native protocol.

Declare a row schema in any `schema` input with a marker comment directly
before its `CREATE TABLE`:

```sql
-- chgen:external
CREATE TABLE ordered_order_keys
(
    ordinal  UInt64,
    order_id String
);
```

A marked declaration is a type definition only: a bare column list, never
executed against ClickHouse. A storage clause (`ENGINE`, `ORDER BY`,
`PARTITION BY`, `TTL`) on a marked table is an error, and so is an
`ALTER TABLE` against it.

```sql
-- The parameter name and wire table name are inferred from the schema name.
INNER JOIN chgen.external(ordered_order_keys) AS requested ON ...

-- Several independent inputs may reuse the same row schema.
INNER JOIN chgen.external('IncludedKeys', ordered_order_keys) AS included ON ...
LEFT JOIN chgen.external('ExcludedKeys', ordered_order_keys) AS excluded ON ...
```

Before execution the wrapper builds `ext.Table` blocks, attaches them with
`clickhouse.WithExternalTable`, and lowers each source function to the exact
table identifier sent on the wire. External inputs are request-scoped: they
need no pooled-connection affinity, no server-side DDL and no cleanup.

External schemas and physical tables are separate catalogs. A direct table
reference never implicitly becomes a Go input. Wire-name collisions with
physical tables, unknown schemas, conflicting parameter reuse and unsupported
column types all fail generation.

### Fixed commands

A schema-aware `:exec` query supports `INSERT ... VALUES` (one row),
`INSERT ... SELECT`, `ALTER TABLE ... UPDATE`, `ALTER TABLE ... DELETE` and
`ALTER TABLE ... DROP PARTITION`. The target column definitions supply the
parameter names and Go types.

```sql
-- name: InsertOrderAudit :exec
INSERT INTO order_audit (order_id, actor, note, written_at)
VALUES (chgen.arg('OrderID'), chgen.arg('Actor'), chgen.arg('Note'), chgen.arg('WrittenAt'))
```

For `Nullable` parameters the generated call unwraps a non-nil pointer and
passes nil for NULL, matching clickhouse-go parameter binding.

A partition expression addresses a partition value rather than a table column,
so `DROP PARTITION` takes its type from a `-- param:` annotation.

## Schema catalog

The configuration file is the explicit, reviewed list of DDL inputs;
migrations are never discovered implicitly. The catalog applies supported
deltas in input order and rejects schema operations it does not model:

- `ALTER TABLE ... ADD COLUMN`, including `AFTER` ordering
- `ALTER TABLE ... MODIFY COLUMN`
- `ALTER TABLE ... DROP COLUMN`

Engine clauses, engine parameters and sort keys are preserved, so a
`ReplacingMergeTree` version column stays visible to the catalog.

## Generated API

For each query the generator emits a `Params` struct (when the query takes
arguments), a `Row` struct (for `:many` and `:one`), the SQL constant and a
method on `*Queries`. It also emits a `Querier` interface and a `MockQuerier`
with one `NameFunc` field per query, so a unit test can inject only the query
behavior it needs without hand-writing a parallel interface.

The type rules cover `LowCardinality`, `Nullable`, `Array`, `Map`, ClickHouse
scalar types, `maxMerge`/`quantileMerge` over `AggregateFunction` state
columns, and a registry of common aggregate, conditional, conversion, array and
map functions.

### A hand-written call must not bind a typed nil pointer

The generated methods are safe. Each one wraps every pointer parameter, thus a
nil pointer reaches the driver as an untyped nil and stores NULL.

A call that you write yourself against the same `Queries` connection does not
get that wrapper. If such a call binds a TYPED nil pointer through
`conn.Exec`, the driver panics. Measured with clickhouse-go v2.47.0 against
ClickHouse 25.8.29.51:

| Bound value | Result of `conn.Exec` |
|---|---|
| `(*uuid.UUID)(nil)` | panic: value method `uuid.UUID.Value` called using nil pointer |
| `(*decimal.Decimal)(nil)` | panic: value method `decimal.Decimal.Value` called using nil pointer |
| `(*time.Time)(nil)` | no panic |
| `(*string)(nil)` | no panic |
| `(*net.IP)(nil)` | no panic; `net.IP` is itself a slice |
| untyped `nil` | no panic; stores NULL |

`uuid.UUID` and `decimal.Decimal` declare `Value()` on the value receiver, so
the driver dereferences the nil pointer to call it. Give an untyped `nil` for
an absent value, or send the value through a generated method. The native
`PrepareBatch`/`Append` path panics for none of these.

### Decimal columns

Every `Decimal` column (`Decimal(P, S)`, `Decimal32/64/128/256`) maps to
`decimal.Decimal` from `github.com/shopspring/decimal`, in every wrapper
shape: `Nullable(Decimal)` becomes `*decimal.Decimal`, `Array(Decimal)`
becomes `[]decimal.Decimal`, and `Map(K, Decimal)` becomes
`map[K]decimal.Decimal`. This is a breaking change from the earlier
`float64` mapping. The change is necessary: clickhouse-go v2.47.0 refuses
to scan a Decimal column into `*float64`, so the old mapping made every
query that returns a Decimal fail at run time, and a float target would
also lose decimal precision silently. The clickhouse-go driver already
depends on the same decimal package, so `go mod tidy` in your module adds
no new dependency tree. `median` and `quantile` over a Decimal argument
keep the input Decimal type, which ClickHouse reports in the canonical
`Decimal(P, S)` spelling.

### IPv4 and IPv6 columns

Every `IPv4` and `IPv6` column maps to `net.IP` from the standard library,
in every wrapper shape: `Nullable(IPv6)` becomes `*net.IP`, `Array(IPv4)`
becomes `[]net.IP`, `Array(Nullable(IPv6))` becomes `[]*net.IP`, and
`Map(K, IPv6)` becomes `map[K]net.IP`. This is a breaking change from the
earlier `string` mapping.

The change is necessary, because the `string` mapping was wrong in two ways
that gave no error:

- Inside a container the driver gives raw binary, not text. `Array(IPv4)`
  scanned into `[]string` gave `"\xc0\xa8\x01\x01"` for `192.168.1.1`.
  Only a bare scalar gave text, so the type name could not show the
  difference, and a re-insert of the binary value failed with ClickHouse
  code 675 or 676.
- An IPv4-mapped IPv6 address lost its family. The `IPv6` value
  `::ffff:1.2.3.4` arrived as `"1.2.3.4"`, which is a different address in
  an `IPv6` column.

`net.IP` holds the full 16 bytes of a mapped address, thus `::ffff:1.2.3.4`
round-trips unchanged. Note that `net.IP.String()` prints such an address as
`1.2.3.4`; the value keeps the family even when the text form does not show
it. `net` is a standard-library package, so this adds no dependency.

A `Map` with an `IPv4` or `IPv6` **key** is refused at generation time.
`net.IP` is a byte slice and cannot be a Go map key, and clickhouse-go
cannot decode such a column either. An explicit error is better than code
that cannot compile.

### UUID columns

Every `UUID` column maps to `uuid.UUID` from `github.com/google/uuid`, in
every wrapper shape: `Nullable(UUID)` becomes `*uuid.UUID`, `Array(UUID)`
becomes `[]uuid.UUID`, `Array(Nullable(UUID))` becomes `[]*uuid.UUID`,
`Map(String, UUID)` becomes `map[string]uuid.UUID`, and `Map(UUID, String)`
becomes `map[uuid.UUID]string`. This is a breaking change from the earlier
`string` mapping.

The change is necessary, because the driver gives back a `uuid.UUID` and
not text. Only a bare scalar and a `Nullable` scalar scanned into a
`string`; every container shape failed the scan, thus the type name could
not show the difference:

```
Array(UUID)           converting uuid.UUID to string is unsupported
Array(Nullable(UUID)) converting *uuid.UUID to *string is unsupported
Map(String, UUID)     converting Map(String, UUID) to *map[string]string
                      is unsupported. try using map[string]uuid.UUID
```

Unlike `net.IP`, a `UUID` **is** usable as a `Map` key. `uuid.UUID` is a
`[16]byte` array, not a byte slice, thus it is a valid Go map key, and the
driver decodes `Map(UUID, String)` into `map[uuid.UUID]string`. This shape
needs no refusal.

The driver already depends on `github.com/google/uuid`, so `go mod tidy` in
your module adds no new dependency tree.

### Enum columns

An `Enum8` or `Enum16` column maps to `string`, in every wrapper shape,
including as a `Map` key. Generation used to refuse an Enum column
altogether.

An Enum value travels as its **name**, not as its number. The driver
refuses the numeric target (`converting Enum8 to *int8 is unsupported`),
thus the name is the only form. A name that is not in the value set cannot
become a silently wrong value: the driver rejects the row when it is
appended (`unknown element "nope"`).

chgen also keeps the value set of the column type. `Enum8('a' = 1)` used to
render as the bare word `Enum8`, which a consumer cannot map. The type now
round-trips in the same canonical form that ClickHouse reports, and a
declaration that leaves the numbers out is numbered the same way the server
numbers it (`Enum8('a', 'b')` becomes `Enum8('a' = 1, 'b' = 2)`).

### Temporal columns

Every `time.Time` that reaches a ClickHouse temporal column passes a range
guard before it leaves the process, and every scanned `DateTime64` passes a
wrap check. **This is a breaking change**: a value that the column or the
driver cannot represent now returns an error where earlier versions returned
`nil` and stored a different instant.

The change is necessary. Measured on ClickHouse 25.8.29.51 with clickhouse-go
v2.47.0, a write of `2299-12-31` into a `DateTime64(3)` column stored
`2106-02-07 06:28:15` through `conn.Exec` and `1900-01-01 00:00:00.291`
through the native batch path, and a stored `2299-12-31` read back as
`1715-06-12`. All of these returned `err = nil`. The rule for this project is:
never silently wrong.

Two things changed together, because one alone is not enough:

- A fixed `INSERT ... VALUES` whose tuple is bare placeholders now uses the
  native `PrepareBatch`/`Append`/`Send` path. The client-side text
  interpolation of `conn.Exec` drops the sub-second fraction of a `time.Time`
  (`03:04:05.123` arrives as `03:04:05.000`) and renders a `Map` temporal with
  the Go default time format, which the server rejects with code 62. A tuple
  that wraps a placeholder in an expression keeps `conn.Exec`.
- Range guards apply to **both** paths. The batch path fixes the fraction but
  still corrupts a far date, only differently, so the batch path is not safe by
  itself.

The limits are measured against the server through its HTTP interface, which
does not involve the Go driver. They live in `temporal.go` as named
constants and travel into every generated file:

| Family | Guarded range (UTC) |
|---|---|
| `Date` | 1970-01-01 to 2149-06-06 23:59:59 |
| `Date32` | 1900-01-01 to 2299-12-31 23:59:59 |
| `DateTime` | 1970-01-01 to 2106-02-07 06:28:15 |
| `DateTime64(0..8)` | 1900-01-01 to 2262-04-11 23:47:16 |
| `DateTime64(9)` | 1900-01-01 to 2262-04-11 23:47:16 |

`DateTime64` with precision 0 through 8 is guarded more narrowly than the
column itself allows. The column holds up to 2299-12-31, but clickhouse-go
carries every `DateTime64` through int64 nanoseconds whatever the declared
precision, so a guard at the column limit would pass exactly the values that
corrupt. If you must store a date after 2262-04-11 in such a column, write it
with SQL text rather than with a bound parameter.

The guards walk containers: `Array` elements, `Map` keys and `Map` values, and
a `Nullable` is checked only when it is not nil. On the read side only
`DateTime64` is checked, because `Date`, `Date32` and `DateTime` cannot reach
the nanosecond limit.

`chgen.arg` is recognized by a small lexical scanner, not a regular expression,
so occurrences inside SQL string literals, quoted identifiers and line or block
comments are left untouched.

## Deliberate limits

This is not a complete semantic ClickHouse type checker. Function return types
resolve through the rule registry, and an unknown function fails generation
instead of silently producing `any`.

Supported: non-recursive relation CTEs, scalar `WITH` aliases, scalar
subqueries, `EXISTS`, uncorrelated `IN` subqueries, aliased derived tables and
ordinary joins. A scalar subquery can use an outer row. An `EXISTS` subquery can
also use an outer row. A correlated `IN` subquery and a lateral derived table
cause an explicit error. A scalar subquery with a correlated `LIMIT` also
causes an explicit error because ClickHouse cannot decorrelate that form.

Not yet supported: tuple-valued result columns and functions without a type
rule. Those remain explicit `AS`/annotation territory until their resolver
rules exist. Aggregate functions whose result is
determined by their first argument (including `argMax(..., tuple(...))`) do not
type-check the ordering key.

Fixed commands stop at one-row `VALUES` and the simple `ALTER` forms listed
above. Multi-row batch loaders and dynamic aggregate-state fold builders keep
handwritten builders, because their performance and state semantics are not
those of a typed fixed query. A one-row fixed `INSERT ... VALUES` does use the
native batch protocol internally, for the correctness reason given under
"Temporal columns", but it still sends exactly one row per call.

## Recommended workflow

Commit the generated file and check it for drift, exactly as with `sqlc`
output. A migration change that affects a generated query set then fails the
check before commit or CI rather than at runtime.

`chgen` rejects raw positional `?` placeholders in schema-aware query sources,
so parameter names stay in the SQL.

## Verification

Run the complete offline check before you send a change:

```sh
./scripts/verify.sh
```

The command checks every supported build tag and does not need a ClickHouse
server. It also checks the public package layout, generated files, examples,
and an external consumer build.

## License

MIT. See [LICENSE](LICENSE). See [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)
for ClickHouse measurement and license attribution.
