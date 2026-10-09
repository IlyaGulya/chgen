# Wildcard projection support

chgen resolves SELECT * and source.* for exactly one FROM source, preserving
catalog column order without rewriting runtime SQL. Sources may be application
tables, external tables, measured table functions, CTEs or derived tables.
Each UNION branch is expanded before checking its result width and types.

By default, DEFAULT columns are included; MATERIALIZED and ALIAS columns are
excluded. Explicit boolean SETTINGS asterisk_include_materialized_columns and
asterisk_include_alias_columns change the expansion, including inside nested
relations. Both settings accept 0, 1, true or false.

```sql
-- name: Read :many
SELECT e.* FROM events AS e ORDER BY id;
```

Generated wildcard readers check result count, names and ClickHouse types before
Scan, including empty results. A changed server schema or connection setting
therefore produces a metadata error rather than silently using a stale shape.
This is a runtime schema check, not a client type assertion.

Queries returned by ParseQueryFiles retain hidden catalog-resolution state
through the public facade. Changing their SQL invalidates that state: parse
again before generation. A manually constructed Query containing a wildcard
cannot supply its own proof of the expansion.

## Measured behavior

Measured on 2026-10-09 with ClickHouse 25.8.29.51. For a table declared with
z UInt64, a String DEFAULT 'value', m UInt64 MATERIALIZED z and x UInt64
ALIAS z, an inserted z=7 produced ordinary wildcard metadata z UInt64,
a String and values 7, 'value'. Enabling both inclusion settings added m and x
in declaration order, both UInt64 and both 7. An outer SETTINGS clause also
included these columns in a wildcard inside a relation CTE.

[TestServerWildcardGeneratedRuntime](../server_generation_test.go) compiles and
executes generated readers with clickhouse-go v2.42.0 and v2.47.0. It covers
ordinary and qualified wildcards, nested relation CTEs, inclusion settings,
numbers(3), UNION ALL, changed connection settings and an added server column.
Public API tests cover preserved SQL, ordered types, stale resolution,
unknown qualifiers, invalid settings, missing annotations and refused modifiers.

The unchanged bundled corpus now resolves and generates 17 of 27 query cases.
This count is not general ClickHouse syntax coverage.

## Remaining boundaries

JOIN and ARRAY JOIN wildcards, wildcard aliases, and projection modifiers such
as EXCEPT, REPLACE and APPLY remain refused. Use explicit projections or the
server-analysis workflow for unsupported shapes. -- result-chtype remains
restricted to explicit aliases and does not contract a wildcard.

The observation IR retains the original wildcard. The resolver's expansion is
not a replacement parser frontend and does not migrate binding to the IR.
