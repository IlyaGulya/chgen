# ClickHouse API inventory

The API inventory records the public surface of the pinned ClickHouse server.
It gives later coverage tools one full list to measure. The file is
`testdata/clickhouse-api-inventory.json`.

The inventory has these classes:

- built-in canonical functions;
- built-in function aliases;
- user-defined canonical functions and aliases in separate lists;
- canonical data type families and their aliases;
- aggregate function combinators, including the server internal marker;
- table functions;
- settings with stable type and default metadata.

The source identity has the ClickHouse version, revision, and build ID. The
inventory does not record process uptime, current setting values, or local
setting constraints. These values can change without an API change.

The function inventory records names and machine metadata. It does not copy
function syntax, argument descriptions, returned-value descriptions, or
documentation categories from ClickHouse. A documentation text change is not
an API change.

Use the pinned ClickHouse server to write the file:

```bash
go run ./internal/tooling/cmd/apiinventory \
  -url http://localhost:18123 \
  -out testdata/clickhouse-api-inventory.json
```

Use the same command as a drift gate:

```bash
go run ./internal/tooling/cmd/apiinventory \
  -url http://localhost:18123 \
  -check
```

The check returns exit code 0 for an identical API. It returns exit code 1
for API drift and lists added, removed, and modified names by class. It
returns exit code 2 when it cannot make a valid comparison. The command
refuses a server version that differs from `MeasuredCHVersion`.

The default test run checks the file format, source version, stable order,
and all required classes. Set `CHGEN_API_INVENTORY_URL` to run the live drift
test. A normal test run does not need a server.
