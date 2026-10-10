# Automatic function rule measurement

Maintainers can measure safe function recipes on disposable ClickHouse
25.8.29.51 and generate candidates for the existing central semantic registry.
Users receive the verified rules in the normal chgen distribution. This tool
does not introduce a second resolver, downloadable support packages, or a
requirement for users to run a server.

## List function gaps

```bash
go run ./internal/tooling/cmd/functionrules -gaps
go run ./internal/tooling/cmd/functionrules -gaps -url http://localhost:18123
```

The offline report compares the pinned `system.functions` inventory with the
measured support manifest. The live form collects the actual inventory first.
Both preserve canonical spelling, aliases, aggregate status, origin and case
policy. Unsupported names include a reason: unmeasured, explicitly refused,
partially measured, or outside the built-in contract. Discovery never invokes
these functions. Coverage counts include aliases and are not a claim that all
argument forms of a supported function are accepted.

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
fixed-result rules. Build-dependent input products are excluded rather than
assigned a type from one build. The profile generates all 22 rules, including an exact
spelling contract for its nine case-sensitive names. Names declared
case-insensitive by ClickHouse keep accepting case variants. Other function
families need their own bounded recipes and signature contracts; discovery
alone is not proof of support.

## String profile

```bash
go run ./internal/tooling/cmd/functionrules \
  -profile string-unary-v1 -url http://localhost:18123 \
  -evidence /tmp/string-measurements.json
go run ./internal/tooling/cmd/functionrules \
  -evidence /tmp/string-measurements.json -out /tmp/registry-candidate.go
```

The string profile measures 13 safe unary functions in 2,587 cells: UTF-8
case conversion and reversal, four Unicode normalization forms, URL component
encoding and decoding, Base64 encoding and non-throwing decoding, soundex,
and regexp quoting. Real rows include ASCII, Unicode, empty strings and NULL.
String and two FixedString widths are tested with Nullable, LowCardinality
and SimpleAggregateFunction wrappers. Negative domains include all canonical
decimal scales, primitive numbers, dates, enums, arrays, tuples and maps.

Only a uniform String result with a witnessed wrapper contract becomes a
rule. A profile may reuse the existing case-folding wrapper transport when
the server preserves a non-nullable SimpleAggregateFunction marker. No
per-function inference exceptions are generated. FixedString is excluded
when execution refuses it, even if nullable analysis advertises a type.
Unrelated server failures never count as an argument-domain proof.

Both profiles own only their own generated rules. A partial snapshot cannot
orphan another rule in the same profile, and generation cannot overwrite a
manual rule or a rule owned by another profile.

## Verify without changing evidence

```bash
go run ./internal/tooling/cmd/functionrules \
  -evidence testdata/clickhouse-function-rules.json -check

go run ./internal/tooling/cmd/functionrules \
  -url http://localhost:18123 \
  -evidence testdata/clickhouse-function-rules.json -check \
  -report /tmp/function-live-report.json

go run ./internal/tooling/cmd/functionrules \
  -evidence testdata/clickhouse-string-function-rules.json -check
go run ./internal/tooling/cmd/functionrules \
  -url http://localhost:18123 \
  -evidence testdata/clickhouse-string-function-rules.json -check \
  -report /tmp/string-function-live-report.json
```

The live report path must be new. Live comparison does not overwrite pinned
measurements. It compares the version, revision and measured behavior while
retaining the actual architecture-specific build ID in the report.

### Build dependent math inputs

ClickHouse 25.8.29.51 has two implementations of `exp`, `log` and `tanh`.
The [FastOps build configuration](https://github.com/ClickHouse/ClickHouse/blob/v25.8.29.51-lts/contrib/fastops-cmake/CMakeLists.txt)
enables that library on supported x86 builds, not ARM. The
[unary math implementation](https://github.com/ClickHouse/ClickHouse/blob/v25.8.29.51-lts/src/Functions/FunctionMathUnary.h)
can preserve floating input types; the fallback always returns Float64.
Both DESCRIBE and execution witnessed seven differing products per function:
Float32 with its six measured wrapper forms, and a non-nullable
SimpleAggregateFunction marker over Float64.

The original ARM measurements remain unchanged. The exact 21-cell difference
from [the x86 CI run](https://github.com/IlyaGulya/chgen/actions/runs/38044757394)
is retained in `internal/functionrules/evidence/build-variants-v1.json`, with
both build IDs, semantic digests and the measurement plan digest. The generated
contract rejects Float32 through wrappers and non-nullable Float64 aggregate
markers, including aggregate names other than the fixture's anyLast. Integer,
Decimal, bare Float64 and the measured nullable Float64 forms remain supported.
An explicit input cast supplies a portable form:

```sql
SELECT exp(CAST(float32_column AS Float64));
SELECT log(CAST(aggregate_float64_column AS Float64));
SELECT tanh(CAST(nullable_float32_column AS Nullable(Float64)));
```

SQL is never rewritten automatically. Server-assisted generation remains an
option when the result must follow the particular server's type semantics.
This is evidence across the observed builds, not a universal guarantee for
every custom build sharing the same version number.

### Cell diagnostics and provenance

Live checks write both the actual measurement report and
`<report>.diff.json`. Logs enumerate each changed function and input, analysis
and execution types, error codes and row witnesses. Semantic identity is
versioned separately from build provenance: identical behavior on a different
build has the same semantic digest. The plan digest includes SQL and row values.
Generated contract identity also includes the exclusion policy.

Captured reports can be compared without a server:

```bash
go run ./internal/tooling/cmd/functionrules -check \
  -evidence testdata/clickhouse-function-rules.json \
  -compare /tmp/function-measurements.json \
  -report /tmp/comparison
```

The two recorded complete semantic variants pass with an explicit
`excluded-build-dependent-inputs-v1` policy; all 21 cells remain visible.
Any additional change, partial variant, metadata change or unexplained outcome
still fails. The checker does not waive wrong types on supported inputs, update
pinned measurements or expand oracle-baseline allowances.

The unified verification runner checks regeneration offline and remeasurement
in the live type suite. It also executes generated consumer code against both
supported driver versions. Existing function, type and execution oracles stay
independent; generated candidates must pass those checks too. A measured
matrix is finite evidence, not a proof of every value or every ClickHouse API.

The expanded sampling population also exposed pre-existing array result and
size-validation defects. Dedicated live regressions cover nested geometry
aliases and refusal of Nullable or Enum arrayResize sizes. They run in the
type suite rather than depending on a random sample to rediscover them.
