# ClickHouse type specimen catalog

The type specimen catalog gives the fuzzer one real column source for every
canonical type family in the pinned ClickHouse inventory. The file is
`testdata/clickhouse-type-specimens.json`.

The collector measures each family in an isolated database. It performs these
steps:

1. Create a table with one specimen column.
2. Insert one row and let ClickHouse write the type default.
3. Read `toTypeName(value)` as the analysis witness.
4. Run `ignore(value)` as an independent execution witness.

An accepted entry records all three queries and the observed canonical type.
A refused entry records the stage, ClickHouse error code, and the bounded
`clickhouse_refused` class. It does not copy the server diagnostic message.
The catalog refuses a missing family, a stale family, an unknown status, an
unknown refusal class, or an incomplete witness.

Run the live collector against the pinned server:

```bash
go run ./internal/tooling/cmd/typespecimens -url http://localhost:18123
```

The command writes the catalog and
`type_specimen_catalog_generated_test.go`. The generated Go file adds every
accepted observed type to the current oracle table. The existing seed query
omits these columns, so ClickHouse seeds each one with its type default.

Check both generated files without a server:

```bash
go run ./internal/tooling/cmd/typespecimens -check
```

Set `CHGEN_TYPE_SPECIMEN_URL` to repeat every live measurement in the test
suite. The default test suite checks exact inventory coverage, generated-file
drift, fixture reachability, and a mutation that removes one accepted column.

ClickHouse 25.8.29.51 accepts 62 of the 66 canonical families with default
server settings. It refuses these four real-column declarations:

- `Nothing`, because this type cannot be used in a table;
- `Object`, because the experimental Object setting is off;
- `Time` and `Time64`, because the Time type setting is off.

The catalog keeps these refusals as measured evidence. It does not enable a
setting to make a family pass.
