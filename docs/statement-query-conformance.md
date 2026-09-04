# Statement query conformance

The expression oracle measures an expression in a simple `SELECT`. The
statement matrix measures the type resolver in complete query contexts and in
nested query placements.

The matrix covers CTEs, derived tables, scalar subqueries, aliases, joins,
table modifiers, set operations, window
expressions, and all executable `SELECT` clauses from `PREWHERE` through
`SETTINGS`. Each context has an execution-accepted cell and an
execution-refused cell. Condition and limit clauses also have measured type
domain cells.

Each statement uses one named lane:

| Lane | Chgen | Analysis | Execution |
|---|---|---|---|
| `supported` | Exact result vector | Exact result vector | Runs |
| `refused` | Refuses | Types or refuses | Refuses |
| `execution_only` | Exact result vector | Exact result vector | Refuses |
| `known_chgen_refusal` | Refuses with an owned gap | Exact result vector | Runs |
| `known_chgen_acceptance` | Returns a known wrong result | Types or refuses | Runs or refuses |

A result vector contains every result name and type in projection order. The
gate compares the complete vector. It does not compare only the first result.

The known-gap rosters are exact. Each entry names its owner task. A missing
entry, an extra entry, a changed owner, or a changed outcome causes the gate to
fail. This rule makes the support-gap count decrease when its owner task lands.

Important refusals also record the expected chgen error boundary and the
expected ClickHouse error codes. An unrelated broad refusal cannot satisfy
these cells.

The ClickHouse analysis lane uses `DESCRIBE TABLE (query)`. The execution lane
runs the complete query against real rows. These witnesses can disagree. The
`execution_only` lane records that disagreement without calling it a chgen
refusal.

Run the matrix against the same disposable ClickHouse instance as the type
oracle:

```sh
CHGEN_ORACLE_URL=http://localhost:18123 \
CHGEN_STATEMENT_ORACLE_OUT=/tmp/chgen-statement.json \
go test -tags fuzzoracle -run TestStatementOracle -v ./internal/engine
```

The matrix cell count and hash are pinned. Mutation tests change result width,
name, type, lane, and known-gap ownership. Each change must make validation
fail.

## Measured SETTINGS

The resolver has an exact measured roster. It includes `log_comment`,
`max_block_size`, `max_bytes_before_external_group_by`,
`max_bytes_before_external_sort`, `max_execution_time`, `max_memory_usage`,
`max_threads`, `optimize_read_in_order`, both memory overcommit denominator settings,
`preferred_block_size_bytes`, `use_uncompressed_cache`, and
`do_not_merge_across_partitions_select_final`. It refuses all other setting
names unless a query explicitly opts in with the
[`-- chgen:unchecked-setting` annotation](query-source.md#unchecked-settings).
Setting names are case-sensitive.

The numeric settings accept unsigned integer literals. `max_block_size` must
be greater than zero. `log_comment` accepts a string literal. The resolver
refuses parameters and other literal types. ClickHouse can convert some other
values, but the resolver does not depend on these implicit conversions.
The three Boolean settings accept `0`, `1`, `true`, and `false`.
`max_threads` accepts unsigned integer literals, including the existing `0`
auto-selection form. `optimize_read_in_order=1, max_threads=1` needs no opt-in.

`TestSettingsReadControlsAgainstClickHouse` checks analysis types and the first
row of a key-ordered MergeTree query with both controls on the pinned server.
It also checks an explicitly opted-in `max_rows_to_read`. CI runs this witness
in the type-oracle job. It is not a benchmark or a promise of identical
performance on another dataset.

ClickHouse accepts duplicate settings and applies the last value. The resolver
validates each value. The tests cover settings in `SELECT`, set operations,
derived queries, and `INSERT SELECT`. Six synthetic query shapes also pass
generation and keep the setting text in the generated SQL.

## Set operations and CTE dependencies

The set-operation matrix has 44 cells. It covers `UNION ALL`, `UNION
DISTINCT`, `INTERSECT`, and `EXCEPT`. It also covers result names and order,
column counts, positional common types, parentheses, precedence, branch
scopes, and exact CTE dependency scopes. It keeps recursive CTEs as an owned
refusal.

The set resolver joins all source leaves in one positional type operation.
The first source leaf gives the result names and column order. Each leaf has
its own row scope. A `WITH` clause on the first unwrapped leaf is visible to
later leaves. A `WITH` clause inside parentheses stays inside those
parentheses.

The matrix uses analysis and execution witnesses on real table columns. The
11 linked execution probes scan generated code and compare the values with
the HTTP text channel. The probes include all four operations, precedence,
associativity, first-leaf result identity, `LowCardinality`, and forward CTE
shadow rules.

Run the set-operation live matrix:

```sh
CHGEN_ORACLE_URL=http://localhost:18123 \
go test -tags fuzzoracle -run TestSetOperationMatrixAgainstClickHouse -v ./internal/engine
```
