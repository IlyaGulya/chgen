# Automatic function rule measurement

Maintainers can measure safe function recipes on disposable ClickHouse
25.8.29.51 and generate candidates for the existing central semantic registry.
Users receive the verified rules in the normal chgen distribution. This tool
does not introduce a second resolver, downloadable support packages, or a
requirement for users to run a server.

## Measure and generate

Use a disposable server with no credentials or application data. The command
creates one uniquely named Memory table and removes it on completion. Function
names come from an explicit safe allowlist, not arbitrary execution of every
entry in `system.functions`.

```bash
go run ./internal/tooling/cmd/functionrules \
  -url http://localhost:18123 \
  -evidence /tmp/function-measurements.json

go run ./internal/tooling/cmd/functionrules \
  -evidence /tmp/function-measurements.json \
  -out /tmp/registry-candidate.go
```

`-functions sin,cos` limits exploratory measurement. Review the candidate before
replacing the central registry. Generation preserves manual rules and refuses
to overwrite them. Existing generated rules cannot silently retain support if
their replacement evidence no longer satisfies the signature contract.
Regenerating an existing registry requires evidence for every rule owned by
this profile; a partial experiment cannot orphan previously generated rules.

## What the first profile proves

The numeric unary profile measures 22 scalar functions. It checks primitive
numeric types, every valid scale of Decimal32, Decimal64, Decimal128 and
Decimal256, and representative generic Decimal spellings. Real columns carry
four rows, including zero and NULL where the type permits it. Valid Nullable,
LowCardinality and SimpleAggregateFunction wrappers are checked separately.
ClickHouse does not permit LowCardinality Decimal columns; those are not
treated as function refusals.

The first profile records 15,400 cells across its 22 functions. Each accepted
cell has independent DESCRIBE and execution type witnesses.
Batch refusal triggers individual measurements rather than assigning the
batch error to every expression. Negative domains, arity, aggregate parameter
syntax and window placement are also measured.

Only uniform Float64 or Int8 results with the measured wrapper behavior become
fixed-result rules. Case-sensitive functions are deferred because the current
generic registry folds function names. Consequently this profile generates
13 rules, not all 22 measured names. Other function families need their own
bounded recipes and signature contracts; discovery alone is not proof of
support.

## Verify without changing evidence

```bash
go run ./internal/tooling/cmd/functionrules \
  -evidence testdata/clickhouse-function-rules.json -check

go run ./internal/tooling/cmd/functionrules \
  -url http://localhost:18123 \
  -evidence testdata/clickhouse-function-rules.json -check \
  -report /tmp/function-live-report.json
```

The live report path must be new. Live comparison does not overwrite pinned
measurements. It compares the version, revision and measured behavior while
retaining the actual architecture-specific build ID in the report.

The unified verification runner checks regeneration offline and remeasurement
in the live type suite. It also executes generated consumer code against both
supported driver versions. Existing function, type and execution oracles stay
independent; generated candidates must pass those checks too. A measured
matrix is finite evidence, not a proof of every value or every ClickHouse API.

The expanded sampling population also exposed pre-existing array result and
size-validation defects. Dedicated live regressions cover nested geometry
aliases and refusal of Nullable or Enum arrayResize sizes. They run in the
type suite rather than depending on a random sample to rediscover them.
