# Configuration reference

`chgen` reads one YAML configuration file. The default path is
`./chgen.yaml`. Use `chgen -f <path>` to select another file.

All relative paths start at the directory that contains the configuration
file. An absolute path stays absolute. A relative input or output path can
contain `..` and can select a location outside the configuration directory.
The normal collision and file identity checks still apply to this path.

## Complete example

```yaml
version: 1
packages:
  - name: gen
    output: gen/queries.sql.go
    queries:
      - queries.sql
      - reports/*.sql
    schema:
      - schema.sql
      - migrations
      - external_tables.sql
```

## Top-level fields

| Field | Type | Requirement |
| --- | --- | --- |
| `version` | integer | Required. The only supported value is `1`. |
| `packages` | list | Required. The list must contain at least one package. |

The file must contain exactly one YAML document. Duplicate keys, unknown
keys, aliases, merge keys, and values of the wrong YAML type are errors.

## Package fields

Each item in `packages` has these fields:

| Field | Type | Requirement |
| --- | --- | --- |
| `name` | string | Required Go package identifier. |
| `output` | string | Required path to a `.go` file. |
| `queries` | string or list | Required. One or more paths. |
| `schema` | string or list | Required. One or more paths. |

`name` accepts the identifiers that Go accepts, including valid Unicode
identifiers. It must not be empty, `_`, or a Go keyword. `chgen` does not
change this name. The `output` value must not be empty. Each `queries` and
`schema` path must also be a non-empty string.

One run processes all listed packages. A query or schema input can be shared
by different packages. One package cannot select the same file more than once.

## Input entries

Each `queries` or `schema` entry can select one file, one directory, or one
glob pattern.

### Files

A file entry must resolve to a regular file. The file name does not need to
end in `.sql` when the entry names the file directly.

### Directories

A directory entry selects its immediate files whose names end in `.sql`.
It excludes names that end in `.down.sql`. It does not search subdirectories.
The selected paths use byte-wise sort order.

This order lets names such as `000001_create.up.sql` and
`000002_add_column.up.sql` define the schema order. `chgen` applies schema
files in the selected order.

A directory that selects no files is an error.

### Glob patterns

Glob patterns use the Go `path/filepath.Match` syntax:

- `*` matches a sequence of characters in one path element.
- `?` matches one character in one path element.
- `[0-9]` matches one character in the given class.

Glob patterns are not recursive. Adjacent `*` characters have the same
non-recursive meaning as one `*`. Braces are normal characters and do not
define alternatives.

A glob result keeps only files whose names end in `.sql` and do not end in
`.down.sql`. It ignores matching directories. It sorts the selected paths
byte-wise. A malformed pattern, a file-system read error, or a result with no
selected files is an error.

### Symbolic links and file identity

An input symbolic link is allowed when its target is a regular file or a
directory. `chgen` resolves symbolic links before it compares input paths.
It also uses operating-system file identity where it is available. Thus, two
entries cannot select one file through different links or hard links.

The process that runs `chgen` must have stable access to the configuration
file system. Another process can change a link or path after a check. Go has
no portable path lock that can prevent this change.

## Schema order

The configuration is the complete list of DDL inputs. `chgen` does not find
migrations outside these entries.

Schema inputs are applied in order. The catalog supports these changes:

- `DROP TABLE`, which removes the table from the catalog; `IF EXISTS` permits
  an already absent table
- `ALTER TABLE ... ADD COLUMN`, including `AFTER` placement
- `ALTER TABLE ... MODIFY COLUMN`
- `ALTER TABLE ... DROP COLUMN`

Projection operations (`ADD PROJECTION`, `MATERIALIZE PROJECTION`,
`DROP PROJECTION`, and `CLEAR PROJECTION`) are parsed and ignored. They change
physical projection storage, not the table's query column catalog.

Data-skipping index operations (`ADD INDEX`, `MATERIALIZE INDEX`, `DROP INDEX`,
and `CLEAR INDEX`) are likewise parsed and ignored: they do not change the
columns or engine metadata modeled by the catalog. Column changes in the same
statement or migration still apply. Keep these migrations in the schema inputs;
there is no need to hide a whole file because it contains index operations.
The target table must still exist, and other unsupported ALTER clauses still
fail. Ignoring index DDL does not validate index definitions or track whether
an index exists, and does not enable these commands in `:exec` queries.

`DROP VIEW` is also parsed and ignored because views never enter the catalog.
`DROP DATABASE`, `DROP DICTIONARY`, and `DROP USER` or `DROP ROLE` are rejected.

Engine clauses, engine arguments, and sort keys remain in the catalog. A
`CREATE TABLE`, `ALTER TABLE`, or `DROP TABLE` whose target is exactly
`schema_migrations` is ignored. This table belongs to `golang-migrate` and is
not part of the application schema.

Schema inputs can also contain `CREATE VIEW`, `CREATE MATERIALIZED VIEW`, and
`INSERT` seed statements. `chgen` parses and ignores these statements because
they do not add physical column definitions to the catalog. It refuses other
statement types. Move such SQL out of the schema inputs.

## Output rules

An output must have a normal Go source file name. These names are errors:

- names that start with `.` or `_`, because the Go tool ignores them
- names that do not end in `.go`
- names that end in `_test.go`
- names with a known GOOS or GOARCH suffix, such as `queries_linux.go` or
  `queries_arm64.go`

One directory can contain only one generated output. Each generated output
declares the common `Queries`, `Querier`, `MockQuerier`, and `New` names.
Two outputs in one package directory would declare these names twice.

The output path itself must not be a symbolic link. A symbolic link in a
parent directory is allowed and is resolved during planning. `chgen` checks
the resolved output directory again before it creates a temporary file. It
also checks the output for a symbolic link again before replacement.

## Collision checks

Before generation, `chgen` refuses these path overlaps:

- the configuration file and any input or output
- a schema input and a query input in the same package
- one input selected more than once in the same input list
- any output and any input in any configured package
- two outputs that resolve to the same file
- two outputs in the same directory

The checks resolve symbolic links and hard links. They also compare a folded,
Unicode-normalized form of each path. A configuration cannot depend on path
case or Unicode normalization behavior that changes between file systems.

## Validation and writes

`chgen` expands and validates all paths first. It then reads all inputs,
builds all schema catalogs, parses all queries, and generates all package
contents in memory. It does not start an output write until every package has
generated successfully.

`chgen` stages every output before it replaces the first output. In package
order, it creates a temporary file in each output directory, writes the
complete generated content, syncs the file, and closes it. If staging fails,
`chgen` removes the temporary files and replaces no output file.

After all files are staged, `chgen` calls `os.Rename` for each output in
package order. Replacement behavior follows the host implementation of
`os.Rename`.

A new output starts with mode `0666` after the caller's umask. An existing
output keeps its permission mode. Missing output directories are created with
mode `0755` after the caller's umask.

Files in different directories cannot be one atomic commit. If a later rename
fails, an earlier output can already be replaced while a later output stays
unchanged. Validation, generation, and staging errors occur before any
replacement.

## Related reference

- [Type mappings and generated API](type-mappings.md)
- [ClickHouse support manifest](clickhouse-support-manifest.md)
- [Examples](../examples/)
