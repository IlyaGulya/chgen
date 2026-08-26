# ClickHouse function probe catalog

`testdata/clickhouse-function-probes.json` is the machine-readable function
probe catalog for ClickHouse 25.8.29.51. It covers each measured supported
function and each function candidate in the registry.

The builder reads the current real-column fixture and the executable function
signature. It creates the legal and illegal argument pools from one domain.
It records each accepted argument form, repeated group, aggregate parameter
form, linked argument rule, and placement rule. The current catalog has 232
functions and 1577 probes.

The generator recipe is only a useful random call. The executable signature
is the complete measured call shape. Inference and this catalog read the same
signature. Thus, an accepted form cannot stay outside inference as a catalog
omission.

Run the static drift and mutation gates:

```bash
go test ./internal/engine -run '^TestFunctionProbeCatalog'
```

Update the catalog after a measured registry or fixture change:

```bash
CHGEN_UPDATE_FUNCTION_PROBES=1 \
  go test ./internal/engine -run '^TestFunctionProbeCatalogIsCurrent$'
```

Run analysis, execution, and chgen checks against the live server:

```bash
CHGEN_ORACLE_URL=http://localhost:18123 \
  go test -tags fuzzoracle ./internal/engine \
  -run '^TestFunctionProbeCatalogAgainstClickHouse$'
```

The static mutation gates change optional forms, repeated groups, constant
arguments, linked arguments, aggregate parameters, and placement. The gate
must refuse each change. The live gate checks each legal probe with analysis
and execution witnesses. It also checks that chgen and ClickHouse refuse each
illegal probe.
