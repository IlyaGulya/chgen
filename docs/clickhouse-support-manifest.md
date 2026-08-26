# ClickHouse support manifest

The support manifest joins the pinned ClickHouse API inventory with the
evidence that chgen has today. The file is
`testdata/clickhouse-support-manifest.json`.

Each inventory item has exactly one status:

- `supported_measured`: chgen has a measured rule;
- `explicitly_refused`: chgen has a deliberate refusal;
- `partially_measured`: measured products have accepted and refused verdicts;
- `not_applicable`: the item is outside the chgen API;
- `not_measured`: chgen makes no support claim.

Each entry also has a semantic family, an evidence reference, and a test
witness. The manifest refuses an unknown status or an empty evidence field.
It also refuses a missing inventory item, an extra inventory item, a wrong
alias target, or a stale override.

The generator reads these sources:

- `testdata/clickhouse-api-inventory.json` for the complete server API;
- `testdata/clickhouse-function-probes.json` for the exact function probe plan;
- `testdata/clickhouse-function-probe-evidence.json` for measured function
  families and normalized live outcomes, including generated sized
  constructors;
- `testdata/clickhouse-support-overrides.json` for strict type, aggregate
  combinator, and refusal records.

Run the generator after one of these sources changes:

```bash
go run ./internal/tooling/cmd/supportmanifest
```

Check the committed file without a server:

```bash
go run ./internal/tooling/cmd/supportmanifest -check
```

Write the machine-readable coverage report:

```bash
go run ./internal/tooling/cmd/supportmanifest -report
```

The report excludes `not_applicable` items from its denominator. A measured
item is supported, explicitly refused, or partial. The supported percentage
counts only full support. The report gives the partial count separately. This
distinction prevents a deliberate refusal or a mixed product matrix from
looking like missing evidence.

An inventory class is never `not_applicable` by default. Each such verdict
must be one exact override with evidence and a test witness. Settings stay
`not_measured` until a probe shows whether they can change parsing, inferred
types, or query execution. This rule prevents a broad scope exclusion from
hiding a semantic setting.

The aggregate combinator class comes from
`system.aggregate_function_combinators`. A supported primitive has a generated
value probe. An unmeasured public primitive has an explicit fail-closed parser
record. The server internal `Null` primitive is the only item in this class
that is not applicable.

A partial item must list at least one exact accepted product and one exact
refused product. `Distinct` is partial because `countDistinct(i32)` uses a
measured registered rule, while the unmodeled `sumDistinct(i32)` product stays
fail-closed. The manifest refuses a partial item when either product list is
empty or when a non-partial item carries product lists.
