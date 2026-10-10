# Explicit server generation

Use your test ClickHouse to analyze SELECT queries that the offline frontend
does not yet understand. This is a mode of chgen, not a hosted service. Keep the
usual `chgen.yaml`; no support package or hand-written result types are needed.

## Reproducible generation without a server

Capture result contracts once against a prepared test database, then commit
the snapshot with the query sources:

```sh
chgen -server http://localhost:8123 -database test_schema \
  -params examples.json -contracts contracts.json
chgen -database test_schema -params examples.json -contracts contracts.json
chgen check -database test_schema -params examples.json -contracts contracts.json
```

`-server` chooses live analysis explicitly. With it, `-contracts` saves the
result contract; without it, `-contracts` reads that file and makes no network
requests. Omit `-params` for queries without parameters. Commit the contract
alongside SQL and representative, non-secret examples; use the last command in
CI. Paths supplied as CLI flags are relative to the working directory.

Before live analysis, prepare a test database with your migrations and use a
restricted read account. chgen does **not** create or migrate the database.
Set `CHGEN_DESCRIBE_USER` and `CHGEN_DESCRIBE_PASSWORD` if authentication is
required; do not put credentials in the endpoint URL. Use the HTTP port, not
the driver's native port.

Replay makes no network requests, supports all finite composition variants,
and produces the same generated code. Supply the same database and parameter
examples. Hashes bind each report to normalized SQL, external-table structure,
database and example values; a separate digest covers configuration, schema
and query file contents. Changed inputs fail before any generated file is
replaced. Capture and generated outputs use the same staged commit, with the
same documented limitation that replacing multiple files is not atomic.

Snapshots store column metadata, ClickHouse version and hashes, not credentials
or raw parameter values. Treat them as trusted, reviewable build inputs, not
signed proof. They are **result-contract snapshots**, not complete database
schema snapshots: replay cannot discover changes on a live server or certify
SQL value semantics. Refresh with `-server` after database, server or settings
changes. `chgen check -contracts` never writes files. Use `chgen check -server
URL` for a live non-writing check; combining `check -server` with `-contracts`
is refused because checking cannot capture contracts. Add `-json` to either
check for a structured report identifying live or saved analysis. Confirmation
means matching result contracts and files, not proof of execution or values.
`-require-confirmed` remains an offline inference option.

The older `generate-server` and `check-server` commands remain supported.
Their `-snapshot-out` and `-snapshot-in` flags select the same capture and replay
implementation; they are not required for the common workflow.

## Recovering from errors

- A local missing type rule or parser refusal points to `-server`; it never
  connects automatically or treats a coverage gap as proof of invalid SQL.
- Missing parameter examples report the package, query and source line and
  supply a JSON template. Replace the type placeholders with representative
  values; chgen does not invent them.
- Stale contracts require fresh analysis with the same database and examples.
  Until that succeeds, the existing generated files and contracts are preserved.
- Access errors point to authentication and read permissions. Missing schema
  errors point to `-database` and test-database preparation. Connection failures
  identify network, TLS and HTTP-port checks. Server prose is not printed because
  it can contain SQL, values and private paths.

## Live analysis

`chgen -server` is an opt-in alternative to offline generation. It asks an
existing ClickHouse test database for result metadata, then generates the same
typed Go methods with metadata checks before Scan. It never falls back to this
mode after an offline error. Without `-server` or `-contracts`, ordinary `chgen`
and `chgen check` remain offline.

```sql
-- name: Read :many
SELECT number, {Text:String} AS text
FROM numbers({Limit:UInt64})
QUALIFY row_number() OVER () > 1
ORDER BY number;
```

Keep the normal `chgen.yaml` package, output, and query paths. Supply concrete
parameter examples in a separate JSON fixture, not type overrides in config:

```json
{"queries.Read":{"Limit":"3","Text":"example"}}
```

```sh
chgen -server http://localhost:8123 -database test_schema \
  -params examples.json -contracts contracts.json
```

Keys are `package.Query`. A bare query name also works if unambiguous across
packages. Every referenced parameter needs one example; unused examples and
conflicting parameter types are errors. Repeated occurrences share one Go field.
Values are strings in JSON; supply logical strings, without SQL quoting or
manual escaping. Containers use ClickHouse text values, for example `[1,2]`.
Examples are sent to the chosen server, not substituted into generated SQL.
Give parameter-dependent projections simple SQL aliases: expression names can
contain examples, and are refused rather than published in generated code.

The generated `ReadParams` contains `Limit uint64` and `Text string`. Use a
native-protocol `clickhouse-go/v2` connection. Native parameter encoding is
measured with v2.42.0 and v2.47.0; the generated methods are not an HTTP-protocol
transport adapter. HTTP is used only by the generation-time analyzer.

## What this unlocks

SQL grammar and function inference are delegated to ClickHouse, independently
of the offline parser and function registry. Runtime tests cover `QUALIFY`,
recursive CTEs, unaliased aggregate outputs, native parameters, empty results,
and parameter-dependent type drift. Result names come from the server, not a
locally reformatted expression. Go field names normalize punctuation; a name
collision is an error and requires explicit SQL aliases.

Scalar-only native parameters retain native binding. Nullable, array, map,
decimal, UUID, and temporal parameters use driver binding with explicit casts.
Temporal values travel as integer timestamps; nested containers are converted
recursively. These binding transformations never interpolate parameter samples.
Generated code records the server version and analyzed SQL-body SHA-256.

## Existing application APIs

Library callers use `chgen.RunServer(ctx, configPath, options)` to generate and
`chgen.CheckServer(ctx, configPath, options)` to verify without writing. Both
share the CLI analysis, snapshot, safety, and staged-output implementation:

```go
options := chgen.ServerOptions{
    Server: "http://localhost:8123",
    Database: "test_schema",
    User: "schema_reader",
    Password: password,
    ParameterExamples: map[string]map[string]string{
        "queries.Read": {"Limit": "3", "Text": "example"},
    },
    SnapshotOutput: "contracts.json",
}
err := chgen.RunServer(ctx, "chgen.yaml", options)
```

Credentials are explicit API arguments, not read from environment variables.
For replay, omit `Server` and use `SnapshotInput` instead of `SnapshotOutput`.
Checking never captures a snapshot. Cancellation propagates through the returned
error. `LoadConfig` preserves each package's `Analysis` setting.

`-- param-chtype: Keys Array(String)` supplies a type for `chgen.arg('Keys')`
or a positional `?`. Positional declarations follow placeholder order; repeated
named arguments share one Go field. Existing `-- param:` annotations work when
their Go type has an unambiguous ClickHouse mapping, or an explicit/native type
is supplied. `-- result:` can rename Go fields; explicit Go types must match the
server mapping. `-- result-chtype:` is an assertion checked against the server,
not an unchecked override.

Marked `-- chgen:external` table declarations provide typed external-table
bindings through `chgen.external(...)`. Analysis sends column structure and zero
rows; runtime sends the caller's rows. Physical migrations are never executed.

Packages may set `analysis: offline` or `analysis: server`. Explicit offline
packages retain local validation even in `generate-server`; explicit server
packages require the server command. With no setting, the command determines
the analyzer. There is no automatic fallback after a local validation error.

`check-server` accepts the same options and regenerates in memory, compares all
outputs, and fails on drift or missing output without writing files. Use it in
CI against a prepared test database; it is not a substitute for runtime tests.

## Typed finite composition

The [existing composition API](query-composition.md) also works with server
analysis: optional `-- chgen:if Name` blocks, finite `-- chgen:table` choices,
and option-owned scalar or external-table parameters. The generator describes
every combination, requires identical result columns and compatible shared
parameter types, and produces one Go method. No caller-supplied SQL fragment
or arbitrary table name is accepted. Each variant records its analysis digest.

Declare the CH types of `chgen.arg` and positional parameters explicitly.
One example fixture covers the union of parameters; analysis sends only the
parameters and empty external-table structures used by the selected variant.
Positional placeholders are normalized before optional blocks are removed,
so omitting a block never shifts declarations onto different arguments.

Table choices must be distinct, simple names; every choice must be used in
every variant, and each selected table must exist in the prepared database.
Unlike offline mode, physical catalog declarations are not required. External
schemas cannot be selected as physical tables. The same limit of 32 variants,
and the same refusal of nested blocks and `else`, apply to both analyzers.

Composed methods bind the selected variant's arguments with explicit casts
and clear older native parameters from the caller's context. Other context
settings and selected external tables remain intact. `check-server` checks all
variants without writing; any analysis or result-contract failure preserves
existing generated files.

## Trust and limits

- This is **server analysis**, not proof of execution or correct business values.
  Analysis can evaluate constants and access data sources through table functions.
  Use a restricted account, an isolated test database, and network restrictions.
- The endpoint is explicit; redirects are refused. Authentication uses
  `CHGEN_DESCRIBE_USER` and `CHGEN_DESCRIBE_PASSWORD`. Requests use `readonly=1`,
  a server time limit, a client deadline, and a bounded response size.
- The schema paths remain protected inputs, but are neither replayed locally nor
  executed on the server. Prepare the actual test database yourself. This mode
  does not validate migration replay or capture schema DDL.
- Only named `:one` and `:many` SELECT queries are accepted. A lexical guard
  rejects multiple statements, mutations, `INTO`, top-level `FORMAT`, malformed
  quoting, and unhandled dollar-quoted syntax. ClickHouse owns grammar validation.
  Parenthesized SELECTs and set expressions are accepted; enclosing parentheses
  do not disable the statement, mutation, or output-clause checks.
- Parameters and results require an existing safe Go type mapping. Runtime
  regressions cover primitive, nullable, array, map, UUID, decimal and temporal
  binding on both supported drivers. This does not promise every ClickHouse
  type: identifier parameters and types without a supported Go representation
  remain errors. Macros other than `chgen.arg`, `chgen.external`, and declared
  `chgen.table` slots are not supported by this analyzer.
- Some otherwise valid SQL cannot be analyzed with `readonly=1`. On the pinned
  server, `DESCRIBE TABLE (SELECT zero FROM zeros(...))` returns code 164; ordinary
  offline series generation works. The tool does not silently relax readonly.
- Parameter values can change result types. Every generated output column is
  checked against runtime names and types **before** reading the first row,
  including empty results. A changed type is an error, not an implicit conversion.
  Matching metadata still cannot prove intermediate types or predicate semantics.
- Rerun generation after schema, server, settings, or SQL changes. There is no
  cached verification or lockfile pretending to validate a changed database.

All packages are analyzed and generated before any output is committed. The
ordinary input/output collision checks and replacement procedure are shared
with offline generation. Failed analysis leaves generated files untouched.
