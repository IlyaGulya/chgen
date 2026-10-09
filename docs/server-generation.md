# Explicit server generation

`generate-server` is an opt-in alternative to offline generation. It asks an
existing ClickHouse test database for result metadata, then generates the same
typed Go methods with metadata checks before Scan. It never falls back to this
mode after an offline error. Ordinary `chgen` and `chgen check` remain offline.

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
chgen generate-server -f chgen.yaml \
  -server http://localhost:8123 -database test_schema -params examples.json
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

## Trust and limits

- This is **server analysis**, not proof of execution or correct business values.
  Analysis can evaluate constants and access data sources through table functions.
  Use a restricted account, an isolated test database, and network restrictions.
- The endpoint is explicit; redirects are refused. Authentication uses
  `CHGEN_DESCRIBE_USER` and `CHGEN_DESCRIBE_PASSWORD`. Requests use `readonly=1`,
  a server time limit, a client deadline, and a bounded response size.
- The schema paths remain protected inputs, but are neither replayed locally nor
  executed on the server. Prepare the actual test database yourself. This mode
  does not validate migration replay or capture a schema snapshot.
- Only named `:one` and `:many` SELECT queries are accepted. A lexical guard
  rejects multiple statements, mutations, `INTO`, top-level `FORMAT`, malformed
  quoting, and unhandled dollar-quoted syntax. ClickHouse owns grammar validation.
- Parameters and results require an existing safe Go type mapping. Runtime
  regressions cover primitive, nullable, array, map, UUID, decimal and temporal
  binding on both supported drivers. This does not promise every ClickHouse
  type: identifier parameters and types without a supported Go representation
  remain errors. Composition directives and macros other than `chgen.arg` and
  `chgen.external` are not supported by this analyzer.
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
