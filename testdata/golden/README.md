# The golden corpus

Each directory here is one generation case. The harness is
`golden_test.go`.

## Files in a case

| File | Need | Content |
|---|---|---|
| `schema.sql` | required | the `CREATE TABLE` catalog |
| `queries.sql` | required | the annotated queries |
| `external.sql` | optional | the external-table row catalog |
| `want.go` | expected | the complete generated Go source |
| `want_error.txt` | expected | the exact refusal text |

A case gives `want.go` OR `want_error.txt`, never both and never neither.
The generated package name is always `golden`, thus a difference between
two cases is a real difference and not a name.

## Why the whole file

The other tests in the package look for SUBSTRINGS of the generated text.
A substring test proves one rule, but it says nothing about the lines it
does not name, thus it cannot see that a merge REMOVED a code path.

A golden case pins the whole output. A lost code path becomes a text
difference in a committed file and fails immediately.

## Regenerate

```
go test ./internal/engine -run TestGolden -update
```

Then READ the diff. A change to a want file is a change to the code that
users get. If the diff has a line that your change does not explain, the
change did more than you intended.

## Add a case

Make the directory, write `schema.sql` and `queries.sql`, run the command
above, then read the new want file before you commit it. The corpus is
only worth its cost if a person checks what the update wrote.

## No server

Generation is a pure function of the SQL text, thus the corpus needs no
ClickHouse and `go test ./...` stays offline. The TYPES that the corpus
pins were measured against a real server by the tests that introduced
each rule (see `interval_temporal_test.go`, `sized_constructor_test.go`
and the others). This corpus locks the TEXT, not the truth.

`TestGoldenSourceCompiles` also builds every `want.go` with the module's
own dependencies. A golden file that pinned text which does not build
would be worse than no corpus, because it would let a broken generator
stay green.

## Coverage

| Case | What it pins |
|---|---|
| `wrappers_nullable_lowcardinality` | Nullable becomes a pointer; LowCardinality leaves the Go type; both wrappers inside `Array` and `Map` |
| `temporal_types_and_interval` | `Date`, `Date32`, `DateTime`, `DateTime64`, the timezone forms, the INTERVAL results, and the write and read guards |
| `scalar_families` | `UUID`, `Decimal`, `Enum8`, `Enum16`, `IPv4`, `IPv6` and their Nullable forms |
| `containers_array_map_tuple` | `Array`, nested `Array`, `Map`, and tuple element access by position and by name |
| `aggregate_combinators` | the `-Merge` and the `-If` combinators |
| `insert_select_aggregate_state` | the `-State` combinator on the write side, where no state crosses into Go |
| `sized_constructors` | `toFixedString`, the `Decimal` constructors, `*OrNull` and `*OrZero` |
| `insert_batch_native` | the native `PrepareBatch`/`Append` path |
| `insert_exec_text_nullable` | the `conn.Exec` text path and the `chgenNullableParam` wrap that keeps a typed nil away from the driver |
| `external_table_join` | a request-scoped external table |
| `refuse_aggregate_function_read` | reading an `AggregateFunction` column |
| `refuse_aggregate_state_result` | SELECTing a `-State` result |
| `refuse_argument_domain` | `sum()` over a `String` |
| `refuse_whole_tuple_result` | reading a whole `Tuple` |
| `refuse_wide_integer_result` | reading an `Int128` |
