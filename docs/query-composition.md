# Typed finite query composition

Keep shared SQL in one annotated query. Declare optional blocks and a finite
set of physical tables; chgen resolves **every combination** before generating
one typed Go method. No runtime SQL strings or unchecked identifiers are accepted.

```sql
-- name: ReadPage :many
-- chgen:table Source events events_archive
SELECT id, payload FROM chgen.table('Source') FINAL
WHERE scope_key = chgen.arg('ScopeKey')
AND id IN (SELECT id FROM chgen.external('Keys', requested_ids))
-- chgen:if After
AND id > chgen.arg('AfterID')
-- chgen:end
ORDER BY id LIMIT chgen.arg('PageRows');
```

Both physical tables and the `-- chgen:external` row schema must be present in
the package's schema inputs. Use a connection with the intended database;
`chgen.table` does not introduce database-name interpolation.

For String keys and payloads, the generated call looks like:

```go
arg := ReadPageParams{
    Source:   ReadPageSourceEvents,
    ScopeKey: scopeKey,
    Keys:     keys,
    PageRows: 100,
}
rows, err := queries.ReadPage(ctx, arg) // no cursor predicate

arg.After = &ReadPageAfterParams{AfterID: lastID}
rows, err = queries.ReadPage(ctx, arg) // cursor predicate included
```

`Source` has a query-specific enum type. Only generated constants are accepted;
the zero value and forged strings return an error before a database call.
Every occurrence of `chgen.table('Source')`, including nested SELECTs, chooses
the same table. Table slots require distinct simple names from the physical
catalog, must be declared in the header, and must be used in every variant.

## Parameters and results

- `nil` omits an optional block. A non-nil pointer enables it, even when its
  fields contain valid zero values. A block with no parameters has an empty
  option struct. Repeated blocks may use the same option name.
- A parameter used exclusively by one option lives in that option's struct.
  Parameters shared with the base query or multiple options remain in the
  main Params struct. Repeated `chgen.arg` occurrences keep their correct
  positional bindings in each variant.
- External tables can also belong to an option. Only selected external tables
  are built and sent. An empty row slice is still an empty external table,
  not a signal to omit the predicate. `-- result-capacity:` supports optional
  slices and uses zero capacity when the corresponding option is absent.
- Result names, order, ClickHouse types and Go types must be identical across
  variants. Shared scalar parameters and external-table schemas must also
  agree. A mismatch is a generation error, not an implicit coercion.
- `:one` keeps `ErrNoRows`. Existing temporal guards and result metadata checks
  run on the selected variant. As with static queries, a Go-only parameter
  annotation does not supply missing ClickHouse type information.
- A type assertion or unchecked setting in **any** variant remains visible
  in `check`, including when absent from the default SQL. Strict checking
  rejects unconfirmed assumptions. Query counts count logical methods, not
  their internal variant count.

## Deliberate bounds

This is finite composition, not a general query-builder language. It supports
`:one` and `:many`, up to 32 fully checked combinations, and independent
`-- chgen:if Name` / `-- chgen:end` blocks inside the SQL body. Nested blocks,
`else`, arbitrary fragments, unbounded table names and `:exec` composition
are refused. Keep the keyset predicate consistent with your complete ordering
key; chgen checks types, not a pagination algorithm's business invariant.

Composition directives must be standalone SQL comment lines. Quoted SQL data
and block comments are not interpreted as directives. Generated SQL contains
only the selected blocks and literal, declared table names; it is not rewritten
into boolean-OR predicates that could change the execution plan.

The public runtime scenario exercises `FINAL`, mutable run membership, a
repeated external-key set, first and following pages, an archive table,
independent options, optional external tables, temporal arguments and empty
results on ClickHouse 25.8.29.51 with both supported Go drivers.
