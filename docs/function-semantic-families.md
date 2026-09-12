# Function semantic families

The function registry assigns one semantic family to each supported function.
The family is an executable contract. It selects a result rule policy and a
probe policy. The registry refuses an unknown family or a family with an
incomplete policy.

Each family combines one typed result policy with one typed probe policy. For
example, fixed-result-scalar, fixed-result-aggregate, and fixed-result-window
are different families. The same result shape cannot silently move to a
different placement. A built specification keeps a seal for both policies.
The registry refuses a changed family or a changed seal.

The generated semantic roster records the family for each static registry
member. The function probe catalog also records the family, the typed probe
policy, and its required axes. The catalog is a plan and is not support
evidence by itself.

Expression-sensitive registry routes live in one executable dispatch table.
The registry guards consult that dispatch instead of copying the names from a
switch into test allowlists. Function-evidence coverage is checked against the
exact catalog names and families, not a manually incremented function count.
Neither change relaxes the live legal/illegal witness gates.

The function probe catalog records the family and its probe recipe. Its gate
refuses a missing family, a changed recipe, a missing argument position, and a
missing legal or illegal boundary. The live gate compares three type answers
for each legal probe:

1. The type from chgen.
2. The ClickHouse analysis type from `toTypeName`.
3. The ClickHouse execution type from a real `SELECT` over fixture columns.

The analysis and execution witnesses can disagree. A legal probe passes only
when both witnesses run and all three type answers agree. An illegal probe
passes only when chgen and the execution witness both refuse it.

The live gate writes `testdata/clickhouse-function-probe-evidence.json`. This
artifact binds the exact catalog digest to the server identity, fixture,
matrix, probe outcomes, and legal execution types. A refusal keeps
its phase, error code, expression, and a bounded project-owned class. It does
not copy the ClickHouse diagnostic message. The support manifest promotes only
functions from this validated artifact.

Use this command to update the generated artifacts after a measured family
change:

```sh
go generate ./internal/engine
CHGEN_UPDATE_FUNCTION_PROBES=1 go test ./internal/engine -run '^TestFunctionProbeCatalogIsCurrent$'
CHGEN_ORACLE_URL=http://localhost:18123 \
CHGEN_UPDATE_FUNCTION_FAMILY_EVIDENCE=1 \
  go test -tags fuzzoracle ./internal/engine -run '^TestFunctionProbeCatalogAgainstClickHouse$'
go run ./internal/tooling/cmd/supportmanifest
```
