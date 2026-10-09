# Series table functions

Measured on 2026-10-09 with ClickHouse 25.8.29.51. This is a bounded
relation family, not general support for arbitrary table functions.

| Function | Column | Type | Arguments |
| --- | --- | --- | --- |
| numbers, numbers_mt | number | UInt64 | zero to three |
| zeros, zeros_mt | zero | UInt8 | zero or one |
| generate_series, generateSeries | generate_series | UInt64 | two or three |

Arguments may be unsigned decimal integer literals or named chgen parameters.
Other expressions and implicit conversions are deliberately not inferred.
Parameters have Go type uint64. A literal zero step is refused; a parameter
whose value is zero fails at runtime. FINAL and SAMPLE are not supported here.
Zero-argument numbers/zeros are unbounded: the caller must supply a suitable
query bound. chgen never inserts a LIMIT or changes SQL.

```sql
-- name: ReadSeries :many
SELECT number AS value
FROM numbers(chgen.arg('Start'), chgen.arg('Length'), chgen.arg('Step'))
ORDER BY number;
```

## Live witnesses

numbers(10, 6, 2) and numbers_mt(10, 6, 2), ordered by number, returned
10, 12, 14. generate_series(2, 8, 2) and generateSeries(2, 8, 2) returned
2, 4, 6, 8: the stop is inclusive, unlike the numbers length argument.
zeros_mt(3) returned three zero values. numbers() LIMIT 3 returned 0, 1, 2;
zeros() LIMIT 3 returned three zeros. numbers(0) returned no rows.
Do not assume parallel output order without ORDER BY.

The server refused negative, fractional and string numbers arguments (code
43), a zero numbers step (36), excess arguments (42), and a zero
generate_series step (471). generate_series(3, 1) returned an empty set.

[TestServerSeriesGeneratedRuntime](../server_generation_test.go) compiles and
executes generated methods using clickhouse-go v2.42.0 and v2.47.0. It checks
literal and parameterized forms, both parallel variants, both inclusive-series
spellings, empty results, bounded infinite input, and a runtime zero-step error.
Public API tests additionally cover CTEs, joins, invalid domains and preservation
of SQL through the parameter parser adapter.

These roster entries remain partially_measured. On the unchanged bundled
107-case syntax corpus, series support initially resolved 14 of 27 query cases.
With [wildcard projection support](wildcard-evidence.md), 17 now resolve and
generate locally. Unaliased aggregate results and invalid
steps remain explicit gaps or refusals; parser acceptance is not runtime proof.
