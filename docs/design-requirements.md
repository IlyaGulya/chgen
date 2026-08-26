# Design requirements: configuration, external tables, database binding

This document records the decisions that the next design pass must satisfy. It
states WHAT the tool must do and WHY. It does not state the final syntax; the
design pass proposes that.

The tool was recently made a standalone module. Its current interface is five
command-line flags and a manifest file. A review of the developer experience,
plus the measured facts below, gives the four requirements in this document.

## Background: what the tool is

`chgen` generates typed Go query wrappers from annotated ClickHouse SQL. It
parses DDL first into a table/column/type catalog, resolves query expressions
against that catalog, and only then generates Go. `sqlc` covers PostgreSQL and
MySQL; there is no equivalent compiler for ClickHouse.

The comparison with `sqlc` matters, because users arrive with `sqlc` habits.
Where the two tools differ, the difference must be deliberate.

## R1. Replace the flags and the manifest with a configuration file

### The problem

Schema inputs are supplied in one of two ways that do the same thing:

- `-schema`, a repeatable flag;
- `-schema-manifest`, a file that lists paths, one per line.

They differ only in how relative paths resolve: the flag resolves against the
working directory, the manifest against its own directory. Two ways to do one
thing, separated by a subtle rule, is a source of confusion.

The manifest is a list-of-paths file for a flag that is already repeatable.
The tool does not need a separate file format only to hold the same paths.

The manifest is documented as an ordered migration chain. In practice, schema
inputs also need to combine a migration directory with overlay DDL. An overlay
can supply a column stub for a VIEW because the parser does not give view
columns. The useful capability is an ordered list of DDL inputs, not a second
input format.

The ordered-delta machinery itself is good and must stay. Only the separate
file that carries the list is unnecessary.

### The requirement

Move the inputs into a configuration file, so the usual invocation is `chgen`
with no arguments. Follow the `sqlc` model, because users know it.

`schema`, `queries` and any external-schema input MUST each accept either one
path or a list of paths. Each entry MUST accept a file, a directory or a glob.

A directory MUST be usable directly, so a project can point at its existing
migration directory. When reading a directory:

- apply the files in a defined, stable order. Conventional migration names
  such as `000001_*.up.sql` sort correctly by file name;
- ignore `.down.sql` files. A down migration reverses what the up migration
  created and would corrupt the catalog;
- ignore the `schema_migrations` table. It belongs to `golang-migrate`, which
  creates it inside the target database to record the applied version. It is
  not part of the domain schema and must not produce generated types.

`golang-migrate` and `chgen` read the SAME migration files for different
purposes: `golang-migrate` EXECUTES them against a server and records the
version; `chgen` only PARSES them to build a type catalog and writes nothing.

The ordered application of `ALTER TABLE ADD/MODIFY/DROP COLUMN` MUST stay, and
unmodelled DDL operations MUST still be rejected rather than ignored.

The configuration file MUST support more than one generation unit. Different
packages can use overlapping schema inputs and separate query files.

Type overrides MUST stay as annotations inside the SQL (`-- param:`,
`-- result:`, `-- result-capacity:`). This is a deliberate difference from
`sqlc`, which puts overrides in the configuration keyed by column. An override
belongs next to the query that needs it. Do not move these into the config.

Whether the flags remain as an escape hatch is for the design pass to decide
and to justify.

## R2. Mark external tables in the DDL, not by which file holds them

### Background

A request-scoped external table is a row set that the CLIENT sends with the
query over the native protocol. It exists so a large key set is not expanded
into a repeated `IN (...)` binding. `sqlc` has no equivalent.

Today a query declares one with `chgen.external(schema)` or
`chgen.external('ParamName', schema)`. The row schemas are ordinary
`CREATE TABLE` declarations that are NEVER executed against ClickHouse. They
live in a separate file, which the query file includes from its header with a
`-- chgen:external-schema <file>` directive.

External schemas and physical tables are two SEPARATE catalogs. Verified by
experiment: a `CREATE TABLE` in the physical schema is NOT visible to
`chgen.external`, which fails with "unknown schema".

### Why the separation must survive

`FROM orders` and `FROM chgen.external(orders)` are fundamentally different:
the first reads server-side data, the second ships client-side data over the
network. If the catalogs merged and the marker were absent, a typo in a name
would silently turn one into the other: a query meant to send fifty thousand
keys would instead read a physical table. This is the same class of silent
failure as R3 below.

A wire-name collision with a physical table is already an error and must stay.

### The problem

Membership is decided by WHICH FILE holds the declaration. A file is a movable
thing: a declaration copied into the wrong file changes meaning silently.

The `-- chgen:external-schema` header directive makes one concept occupy three
places: the directive, the separate file, and the call in the query. A
forgotten directive produces the unhelpful error "chgen.external references
unknown schema X", which does not say that an include is missing.

### The requirement

An external row schema MUST be marked explicitly in the DDL itself, so its
meaning travels with the declaration instead of depending on its file.

The marker MUST be mandatory. An unmarked table MUST NEVER become an external
input, because that is exactly the silent failure the separation prevents.

With the marker in place, external schemas MAY live in any schema input,
including a file that also holds physical tables. A project MAY still keep
reusable external schemas in one shared file. It must be a choice, not a
requirement of the tool.

The header directive MUST be removed. The design pass decides whether a
dedicated config key for external inputs is still worth having.

Three marker candidates were tested against the parser and all three parse:

- a `-- chgen:external` comment before `CREATE TABLE`. The AST node exposes
  `CreatePos`, so a comment can be attached to the statement that follows it.
  Consistent with the existing `-- name:` annotation language;
- `CREATE TEMPORARY TABLE`. Close to the ClickHouse meaning and needs no
  comment directive, but the form is arguable when an `ENGINE` is present;
- `ENGINE = Memory`. NOT recommended: it is a valid engine for a real table,
  so overloading it is dangerous.

The design pass chooses one and justifies the choice.

## R3. A missing database marker must fail generation

### The problem

A query names its target database with the `__DATABASE__` marker, which the
generated wrapper replaces at run time with a validated identifier from the
connection configuration. The marker exists because ClickHouse database and
table identifiers cannot be passed as ordinary positional arguments.

The marker can select a database at run time. This can be useful when one build
uses different databases in different environments. Each database also holds
its own `schema_migrations` table and migration counter.

A query that omits the marker, `SELECT ... FROM orders`, generates with NO
error and silently runs against the connection's default database, bypassing
the whole binding mechanism.

The generator resolves the table against its catalog, so at generation time it
KNOWS the reference is a physical table with no marker. It has everything it
needs to reject this.

This is the most serious defect in this document. It is a correctness bug, not
an inconvenience, and it surfaces in production.

### The requirement

A physical table reference without the database marker MUST fail generation.

If an escape hatch for an unqualified reference is offered, it MUST be
explicit in the source. The default MUST be to fail.

The design pass SHOULD also evaluate a further option: resolving the database
name at generation time from configuration, which would remove the run-time
binding helper and its per-call identifier validation. Note the trade-off,
which the design pass must weigh: a generation-time name is fixed in the
generated file, whereas the run-time marker lets one build serve more than one
database. State a recommendation either way.

## R4. Detect duplicate query names across files

Once `queries` accepts a list, two files can declare the same
`-- name: Foo`. This cannot happen today because there is one query file.

Duplicate query names MUST be an error that names both locations.

## Cross-cutting: error messages are part of the interface

The review found the errors in the main path to be good and specifically worth
preserving: `column "X" is not present in FROM tables`, incompatible inferred
types, the explicit-alias requirement, and syntax errors with a caret. The
fail-loud philosophy, where an unknown function fails instead of producing
`any`, is the property that separates this tool from a regex generator.

New and changed errors MUST meet that same standard. Specifically:

- an error MUST say how to fix the problem, not only what is wrong. The
  external-schema error above is the counter-example to avoid;
- an error MUST NOT leak internal Go types. The current manifest error prints
  `unsupported ALTER TABLE orders clause *parser.AlterTableRenameColumn`; a
  user needs "RENAME COLUMN is not supported; supported: ADD/MODIFY/DROP";
- semantic errors SHOULD carry `file:line`. The parser knows positions, and
  syntax errors already report them well;
- a near-miss name SHOULD produce a "did you mean" suggestion.

## Compatibility

The tool has no stable public interface before version 1. A breaking change is
acceptable when it is justified. Prefer the right long-term interface over
compatibility with the flags.

The design MUST state the migration cost for an existing configuration.

## Out of scope

The annotation-only mode, which runs without any schema, was called a trap by
the review: it is effectively a second dialect that requires an explicit alias
on every column and a `-- param:` annotation for every parameter, and the same
file that works in schema mode fails in it. Whether to keep this mode at all
was a separate decision and was NOT part of this design pass.

That decision was made later: the mode is removed. The legacy query front
doors that reached it (`Parse`, `ParseFile`, `ParseWithSchema`,
`ParseWithSchemas`, `ParseFileWithSchema`) and the legacy schema loaders
(`ParseSchemaFile`, `ParseSchemaFiles`, `ParseSchema`) are deleted.
`ParseSchemaCatalogs` and `ParseQueryFiles` are the only entry points, thus a
library user and the CLI build the same catalog.
