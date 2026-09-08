# Design: configuration file, external-table marker, database binding

This document is the final interface design for `chgen`. It satisfies the
requirements in `design-requirements.md` (R1–R4) and the amendment to R3 that
questions the `__DATABASE__` marker itself. Each section states the decision
first, then the reason.

Summary of decisions:

- One configuration file, `chgen.yaml`, replaces all input flags and the
  manifest file. The usual invocation is `chgen` with no arguments.
- The external-table marker is the comment `-- chgen:external` before the
  `CREATE TABLE`. The `-- chgen:external-schema` header directive is removed.
  There is no dedicated config key for external inputs.
- The `__DATABASE__` marker is removed. The database comes from the
  connection, as in `sqlc`. A qualified table reference in a query is a
  generation error.
- The command-line flags for inputs are removed. Only `-f` (config path) and
  `-version` remain.

## 1. Configuration file

### 1.1 Discovery and invocation

`chgen` reads `chgen.yaml` from the current working directory. The flag
`-f <path>` selects a different file. If the file is not found, generation
fails; there is no flag fallback and no implicit search up the directory tree.
An explicit path keeps the build reproducible from any CI runner.

All relative paths in the file resolve against the directory that holds the
configuration file, not against the working directory. Absolute query, schema,
and output paths stay absolute. This is the `sqlc` rule and it removes the
flag-versus-manifest resolution difference that R1 describes.

One run generates every package in the file. There is no per-package
selection flag; generation is fast enough that partial runs are not worth a
second invocation mode.

### 1.2 Keys

```yaml
version: 1
packages:
  - name: <string>       # required: Go package name of the output
    output: <path>       # required: generated .go file
    queries: <inputs>    # required: annotated query sources
    schema: <inputs>     # required: DDL sources, applied in order
```

- `version` (integer, required). Must be `1`. An unknown version fails with
  the message in section 5. This is the one forward-compatibility hook.
- `packages` (list, required, at least one entry). Each entry is one
  generation unit. Two entries may share schema inputs; each entry builds its
  own catalog.
- `name` (string, required). The Go package name. No default. The old flag
  default `querygen` is not kept: a silent default package name in a
  multi-package file would be a trap.
- `output` (path, required). One file per package. Two packages must not
  declare the same output; that is an error. One directory must not contain
  two generated outputs because both files declare `Queries`, `Querier`,
  `MockQuerier`, and `New`.
- `queries` (inputs, required). See 1.3. Every matched file is an annotated
  query file. Queries from several files join into one package; duplicate
  query names are an error (section 6).
- `schema` (inputs, required). See 1.3. Entries are applied in list order.
  Inside one entry that is a directory or a glob, files apply in byte-wise
  file-name order. The ordered `ALTER TABLE ADD/MODIFY/DROP COLUMN` delta
  machinery is unchanged. Unmodelled DDL operations are still rejected.

There is no `external_schema` key. Section 2.4 gives the reason.

Annotation-driven type overrides (`-- param:`, `-- result:`,
`-- result-capacity:`) stay in the SQL, per R1. The configuration file never
carries type information.

Unknown keys anywhere in the file are an error, not a warning. A misspelled
key must not silently change what the run reads.

The YAML stream must contain exactly one document. Duplicate keys, aliases,
merge keys, and values with the wrong YAML type are errors.

### 1.3 The `<inputs>` value

`queries` and `schema` accept one path or a list of paths. Each entry is one
of:

- a file path — used as-is;
- a directory path — expanded per section 4;
- a glob pattern (`*`, `?`, or a character class such as `[0-9]`, with Go
  `path/filepath.Glob` semantics) — matches files only, sorted byte-wise.
  Adjacent stars have the same non-recursive meaning as one star. Braces are
  normal characters.

An entry that names a missing file, an empty directory (after filtering), or
a glob with zero matches is an error. A silent empty input would generate a
wrong catalog; fail-loud is the project rule.

Input symbolic links are allowed when their resolved targets are regular
files or directories. The resolved path identifies duplicate inputs and
collisions. An output symbolic link is an error. A link in an output parent
path is allowed, but the generator checks the resolved parent again directly
before it creates the temporary output. Output collision keys fold path case
and Unicode normalization. This conservative check keeps one config portable
between file systems with different path identity rules.

An output name must end in `.go`. Go ignores names that start with `.` or `_`,
and it selects names with known GOOS or GOARCH suffixes for only some targets.
These names and `_test.go` names are errors.

Planning expands all inputs and generates all packages in memory. A planning
or generation error changes no output. During commit, chgen writes, closes,
and syncs a complete temporary file in the target directory before it calls
`os.Rename`. Replacement behavior follows the host implementation of
`os.Rename`. A new output uses mode `0666` after the caller's umask. A replaced
output keeps its previous permission mode. A cross-directory commit is not one
atomic operation. Another process can also change a path after the final
identity check because Go has no portable path lock for this operation.

### 1.4 Example: two generated packages

Two packages from overlapping schema inputs. The second adds an overlay file
with a column stub for a VIEW (the parser does not give view columns), and
both use a shared external-table file (section 2).

```yaml
version: 1
packages:
  - name: readgen
    output: internal/clickhouse/readgen/queries.sql.go
    queries: internal/clickhouse/readgen/queries.sql
    schema:
      - migrations/clickhouse            # directory: ordered *.up.sql
      - internal/clickhouse/readgen/external_tables.sql

  - name: writegen
    output: internal/clickhouse/writegen/queries.sql.go
    queries: internal/clickhouse/writegen/queries.sql
    schema:
      - migrations/clickhouse
      - internal/clickhouse/writegen/schema.sql   # VIEW column stub
```

## 2. External-table marker

### 2.1 Decision

An external row schema is declared with a marker comment on its own line
directly before the `CREATE TABLE`:

```sql
-- chgen:external
CREATE TABLE ordered_order_keys
(
    ordinal  UInt64,
    order_id String
);
```

"Directly before" means: between the marker line and the `CREATE` keyword
there are only blank lines and other comment lines. A human-readable comment
block between the marker and the statement is therefore allowed.

### 2.2 Why this candidate

- It is the same annotation language as `-- name:` and `-- param:`. A reader
  who knows one chgen marker knows them all.
- `CREATE TEMPORARY TABLE` was rejected. Experiment: the parser accepts it
  and sets `HasTemporary`, so it is implementable. But `TEMPORARY` has a real
  ClickHouse meaning (a session-scoped server table), and a chgen row schema
  is not that. A declaration that looks executable but means something else
  to the tool is the kind of ambiguity R2 exists to remove. The form with an
  `ENGINE` clause is also arguable, as the requirements note.
- `ENGINE = Memory` was rejected by the requirements: it is a valid engine
  for a real table.

Implementation note, verified by experiment: the parser strips comments from
the AST, but every `CreateTable` node carries `CreatePos`, the byte offset of
the `CREATE` keyword. The schema loader scans the source text between the end
of the previous statement and `CreatePos` for a `-- chgen:external` line.

Because `CreatePos` is a BYTE offset, the TTL-rollup preprocessor must not
change the length of the text. Trimming a line tail would move every later
offset and attach a marker to the wrong table. The preprocessor therefore
replaces the trimmed tail with spaces of the same length. This keeps the
byte offsets, the line boundaries, and the offset-to-line mapping valid.

### 2.3 Rules and violations

- The marker is mandatory for external schemas. `chgen.external(x)` where `x`
  resolves to an unmarked (physical) table is an error (message in section
  5). An unmarked table never becomes an external input.
- The loader builds two catalogs from one input stream: marked tables go to
  the external catalog, unmarked tables and `ALTER TABLE` deltas go to the
  physical catalog. The catalogs stay separate; `FROM x` never resolves to an
  external schema and `chgen.external(x)` never resolves to a physical table.
- A marker line that is not followed by a `CREATE TABLE` statement (per the
  "directly before" rule) is an error. A dangling marker must not be ignored.
- A marked table must be a bare column list: an `ENGINE`, `ORDER BY`,
  `PARTITION BY`, `TTL` or `PRIMARY KEY` clause on a marked table is an
  error. A row schema is a shape, not a storage declaration.
- `ALTER TABLE` against a marked table is an error. External schemas have no
  migration history; edit the declaration.
- A name collision between an external schema and a physical table, in any
  input of the same package, is an error at catalog build time. Today this
  check runs per query (wire-name collision); it moves earlier and also
  covers the schema name itself. The per-query wire-name collision check for
  renamed parameters stays.
- External schemas may live in any `schema` input, including a file that also
  holds physical tables. Keeping them in one shared file remains a project
  choice.

### 2.4 No dedicated config key

The `-- chgen:external-schema` header directive is removed, and no
`external_schema` config key replaces it. With the marker in the DDL, an
external file is an ordinary schema input; a separate key would restore the
"membership by file" rule that R2 removes, and would be a second way to do
one thing. The one concept now occupies two places instead of three: the
marked declaration and the `chgen.external(...)` call.

## 3. Database binding: remove `__DATABASE__`

### 3.1 Decision

The `__DATABASE__` marker is removed. Queries name tables without a database
qualifier. The database comes from the connection
(`clickhouse.Options.Auth.Database`), exactly as `sqlc` takes it from the
DSN. The generated constructor becomes `New(conn driver.Conn) *Queries`; the
`bindDatabase` helper and its per-call `strings.ReplaceAll` plus identifier
validation are deleted.

### 3.2 Why remove, not keep or make optional

The common connection model gives each generated package a connection with one
`Auth.Database`. A query set works against that database and does not need a
second run-time database argument. This is also the usual `sqlc` model. The
run-time helper otherwise costs a full-text replace and a per-character
identifier scan on every call.

The only capability the marker gives over the connection is one connection
serving several databases (per-tenant databases, or a join across databases).
ClickHouse joins across databases would need qualified names on only some
tables, which the marker syntax cannot express anyway. Keeping this mechanism
for a possible future use fails the "prefer the right long-term interface"
rule. The "optional marker" variant keeps the whole runtime helper, the README
concept and a new consistency rule. If the multi-database-per-connection case
appears, a per-package opt-in can be added then. Section 9 records this open
question.

The `schema_migrations` counter argument in R3 is about how `golang-migrate`
applies migrations. It constrains deployments, not how the application
addresses tables, so it does not require the marker.

### 3.3 The fail-loud rule that remains

R3's core demand — no silent divergence — survives the removal:

- `FROM __DATABASE__.x` is now a generation error with a migration hint
  (section 5). The marker must not silently degrade to a literal identifier.
- Any qualified reference `db.table` in a query is a generation error. The
  database is a connection property; a name burned into the SQL text would
  silently disagree with the connection. This also closes the original R3
  hole in reverse: there is no longer a "marked" and an "unmarked" way to
  write the same reference, so nothing can be forgotten.
- The generated code keeps one start-time guard: `New` panics on a nil
  connection, and the first call still fails loudly on a closed connection.
  Verifying that `Auth.Database` points at the intended database is the
  application's start-time check.

### 3.4 Generation-time resolution: recommendation

R3 asked for an evaluation of resolving the database name at generation time
from configuration. Recommendation: do not. It has the worst properties of
both worlds: the name is fixed in the generated file, so one build cannot
serve two environments with different database names, yet the config must
duplicate a value the connection already holds. Removal gives the same
simplification of the generated code without fixing any name anywhere.

## 4. Directory and glob reading rules

For a `schema` or `queries` entry that is a directory:

1. List the immediate files of the directory. Subdirectories are not
   descended. A migration tree with subdirectories must be listed as several
   entries or a glob; implicit recursion hides inputs.
2. Keep only files whose name ends in `.sql`.
3. Drop files whose name ends in `.down.sql`. A down migration reverses its
   up migration and would corrupt the catalog.
4. Sort the remaining names byte-wise ascending. `000001_*.up.sql` naming
   sorts correctly under this rule.
5. If no file remains, fail (message in section 5).

A glob entry matches files only, is filtered and sorted by the same rules
2–4, and fails on zero matches after filtering. It also fails on a file system
read error. Its pattern grammar is the Go `path/filepath.Glob` grammar.

`schema_migrations` handling, in any schema input: a `CREATE TABLE`,
`ALTER TABLE`, or `DROP TABLE` whose target table is exactly
`schema_migrations` is skipped without error and produces no catalog entry.
The table belongs to `golang-migrate`, not to the domain schema. A query that
references `schema_migrations` therefore fails with the ordinary unknown-table
error. Every other unmodelled DDL statement is still rejected, unchanged.

`DROP TABLE` removes a physical table from the ordered catalog. An unknown
table is an error unless the statement has `IF EXISTS`. `DROP VIEW` is an
explicit no-op because views never enter either catalog. `DROP DATABASE`,
`DROP DICTIONARY`, and `DROP USER` or `DROP ROLE` remain unsupported.

Duplicate `CREATE TABLE` for one name across ordered inputs remains an error
unless an intervening `DROP TABLE` removed the earlier definition. A VIEW stub
can add a relation that the migrations do not define, so it does not collide.

## 5. Error messages

Messages follow the existing standard: say the location, say what is wrong,
say how to fix it, never print internal Go type names. Semantic errors carry
`file:line` wherever a statement or query position is known. `%s` marks a
filled-in value.

Configuration:

- `chgen.yaml not found in %s; create one or pass -f <path>`
- `%s: unsupported config version %d; this chgen supports version 1`
- `%s: package %d: missing required key "name"` (same shape for `output`,
  `queries`, `schema`)
- `%s: unknown key %q; supported keys: version, packages, name, output,
  queries, schema`
- `%s: packages %q and %q declare the same output %s`

Inputs:

- `schema entry %q: no such file or directory`
- `schema directory %q contains no .sql files (after ignoring .down.sql)`
- `schema glob %q matches no files`
  (the same three shapes for `queries`)

Schema and DDL:

- `%s:%d: RENAME COLUMN is not supported; supported ALTER TABLE operations:
  ADD COLUMN, MODIFY COLUMN, DROP COLUMN; projection operations ADD
  PROJECTION, MATERIALIZE PROJECTION, DROP PROJECTION, CLEAR PROJECTION and
  index operations ADD INDEX, MATERIALIZE INDEX, DROP INDEX, CLEAR INDEX are
  ignored` (one message per unsupported clause, named by its SQL keyword,
  never by its Go AST type)
- `%s:%d: DROP TABLE %q targets an unknown table; add IF EXISTS if the table
  may be absent`
- `%s:%d: DROP DICTIONARY is not supported; supported DROP operations: DROP
  TABLE, DROP VIEW`
- `%s:%d: statement is not CREATE TABLE, DROP TABLE/VIEW, RENAME TABLE, EXCHANGE TABLES, or a supported ALTER
  TABLE; move non-schema SQL out of the schema inputs`
- `%s:%d: duplicate CREATE TABLE %q; first declared at %s:%d`

External tables:

- `%s:%d: -- chgen:external marker is not followed by CREATE TABLE`
- `%s:%d: external schema %q must not declare ENGINE/ORDER BY/PARTITION
  BY/TTL; an external schema is a column list only`
- `%s:%d: ALTER TABLE %q targets an external schema; edit its CREATE TABLE
  instead`
- `%s:%d: external schema %q collides with physical table %q declared at
  %s:%d; rename one of them`
- `%s:%d: chgen.external(%s): unknown external schema; declare it with
  "-- chgen:external" before its CREATE TABLE in a schema input` — plus,
  when a physical table of that name exists: `a physical table %q exists;
  chgen.external only accepts schemas marked -- chgen:external` — plus a
  near-miss suggestion when applicable: `did you mean %q?`

Database references:

- `%s:%d: query %s: __DATABASE__ was removed; the database comes from the
  connection (clickhouse Auth.Database); use the unqualified name %s`
- `%s:%d: query %s: qualified reference %s.%s is not allowed; the database
  comes from the connection; use the unqualified name %s`

Queries:

- `%s:%d: duplicate query name %s; first declared at %s:%d`
- `%s:%d: query %s: table %q is not present in the schema or the query
  scope` — plus `did you mean %q?` on a near miss (edit distance <= 2 against
  physical tables, CTE names and aliases in scope).

"Did you mean" applies to unknown tables, unknown columns and unknown
external schemas. It never applies when two candidates tie.

## 6. Duplicate query names (R4)

With `queries` as a list, one package can read several files. During parsing,
every `-- name:` records `file:line`. A second declaration of a name already
seen in the same package fails with the message above, naming both locations.
The check is per package: two packages may both declare `ListOrders`, because
they are different Go packages.

`Query` gains `File string; Line int` fields for this and for every
per-query error message in section 5.

## 7. Command-line flags

All input flags are removed: `-input`, `-output`, `-package`, `-schema`,
`-schema-manifest`. Remaining flags: `-f <path>` (configuration file,
default `chgen.yaml` in the working directory) and `-version`.

Reason to remove rather than keep as an escape hatch: R1's root problem was
two ways to supply one input, separated by a subtle rule. Flags beside a
config file recreate exactly that, with new questions (do flags override or
extend? against which directory do they resolve?). A scripting need is served
by `-f /dev/stdin` or a generated config file, not by a parallel flag
interface. `ParseSchemaManifest` and the flag plumbing are deleted; the
library-level functions (`ParseSchemaCatalogs`, `ParseQueryFiles`, `Generate`)
remain public.

## 8. Migration plan for an existing configuration

1. Add `chgen.yaml` at the repository root with one entry for each generated
   package, as shown in section 1.4.
2. Delete the old schema manifest files after all paths move to `schema:`.
3. Add `-- chgen:external` before each external `CREATE TABLE` declaration.
4. Delete each `-- chgen:external-schema` header and remove each
   `__DATABASE__.` prefix from query files.
5. Replace separate generation commands with one `go tool chgen` command.
6. Regenerate. Change each `New(conn, database)` call to `New(conn)`. Verify
   that the connection sets `Auth.Database` to the intended database.
7. Run the generated-code drift guard and the test suite.

The changes are mechanical. Query semantics do not change. The generated SQL
text changes only by losing the database qualifier.

## 9. Open questions

1. Re-introduction of an explicit database qualifier, if a one-connection,
   many-databases consumer appears. Recommendation: add it then as a
   per-package config key (for example `database_param: true`) that makes
   `New` take the database again and re-enables a marker — all-or-nothing per
   package, mixing marked and unmarked references an error. Do not build it
   now.
2. Config file name: `chgen.yaml` versus reusing `sqlc.yaml` conventions such
   as `chgen.json`. Recommendation: `chgen.yaml` only, no alternate formats;
   one parser, one example.
3. Whether `queries` directories should be common. Recommendation: allowed by
   the uniform `<inputs>` rules, but the documented layout stays one
   `queries.sql` per package until a package outgrows one file; R4 makes the
   multi-file case safe when it happens.
