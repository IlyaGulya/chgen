# Continuous integration and the oracle baseline

This document defines the current type-oracle contract and the baseline
procedure. Read it before you change `.github/workflows/ci.yml` or
`testdata/oracle-baseline.json`.

Historical grammar measurements are not part of this contract. See the
[historical evidence index](historical-oracle-evidence.md).

## Current identity

The type oracle has one current population:

| Field | Value |
| --- | --- |
| Sampling profile | `current-combined-v1` |
| Fixture | `current-fixture` |
| Profile selector | `CHGEN_ORACLE_PLAN=current-combined-v1` |
| Fixture selector | `CHGEN_ORACLE_FIXTURE_ID=current-fixture` |
| Pinned ClickHouse version | `25.8.29.51` |
| Fixed seed set | `1 7 42 99` |

The profile hash identifies the full scheduler specification. The report also
stores the fixture hash, the observed fixture shape, the ClickHouse version,
and a nonzero server-run value. The report owns this identity.

`CHGEN_ORACLE_GRAMMAR` is retired. Its presence must cause an explicit refusal.
Do not translate an old grammar value to the current profile. Such a
translation would give historical evidence a false current identity.

## Current reports

A current type-oracle report has report version 2 and the full population
identity. The JSON reader refuses unknown fields and trailing JSON values.

The tools in `internal/tooling/cmd/oraclediff` and
`internal/tooling/cmd/oraclegate`, and the union gate, refuse a missing or mixed
identity. A normal comparison needs the same profile, profile hash, fixture,
ClickHouse version, and server run. A union also needs distinct seeds and a
nonempty generated population.

Pass report paths without labels:

```text
go run ./internal/tooling/cmd/oraclediff report-a.json report-b.json
go run ./internal/tooling/cmd/oraclegate -report report-seed1.json
```

The command-line tools reject `profile=path`. A path cannot relabel the
identity that the report contains.

## Why the union gate uses signatures

The server can give different error totals after a restart or after a change
in uptime. Therefore, the blocking gate does not compare totals. It compares
sets of named signatures.

A mismatch signature has the form `kind: chgen=X ch=Y`. A blindness signature
has the form `kind: chgen=X` and comes from
`CH_ERROR_43_CHGEN_TYPED`.

The gate applies these rules:

- A signature in the run but not in the baseline fails the gate.
- A signature in the baseline but not in the run does not fail the gate.
- A missing report, a repeated seed, an empty population, or mixed identity
  causes a refusal.
- `CH_ERROR` totals do not gate the build.

The CI job runs seeds `1`, `7`, `42`, and `99` on one server instance. It
combines the reports into one set for `current-combined-v1`. A new seed does
not need a new union-cell key.

Run the same gate locally after the four reports exist:

```text
go run ./internal/tooling/cmd/oraclegate -union \
  -report report-seed1.json \
  -report report-seed7.json \
  -report report-seed42.json \
  -report report-seed99.json
```

Exit status 0 means that the run has no new signature. Exit status 1 means
that the run has a signature that the baseline does not accept. Exit status 2
means that the tool refused the measurement. A refusal is not a difference.

## Update the current union cell

Keep one pinned server instance for all seeds. Set these variables for each
oracle run:

```text
CHGEN_ORACLE_PLAN=current-combined-v1
CHGEN_ORACLE_SEED=<seed>
CHGEN_ORACLE_OUT=<report-path>
```

Then update the current union cell:

```text
go run ./internal/tooling/cmd/oraclegate -update-union \
  -i-measured-this=25.8.29.51 \
  -note "<reason and issue ID>" \
  -report report-seed1.json \
  -report report-seed7.json \
  -report report-seed42.json \
  -report report-seed99.json
```

Read the diff. Each added signature is a new wrong answer, a new blindness, or
a newly accepted and tracked defect. A missing signature is not proof that a
defect is fixed. The update command keeps accepted signatures that the new
sample does not reach. Remove a fixed signature only with the retirement
command. The update command refuses a fixture change because it cannot carry
an accepted set from one fixture to another.

## Retire one signature

Use `-retire-union` only after a focused measurement and a regression test show
that the named defect is fixed:

```text
go run ./internal/tooling/cmd/oraclegate \
  -i-measured-this=25.8.29.51 \
  -retire-union='current-combined-v1:<signature>=<issue and reason>' \
  -report report-seed1.json \
  -report report-seed7.json \
  -report report-seed42.json \
  -report report-seed99.json
```

The command refuses a signature that a supplied report still contains. It
also refuses a signature that the current union cell does not contain. The
baseline keeps the ClickHouse version and reason in the retirement record.

An update must preserve all existing retirement records. A retired signature
must not also occur in an accepted signature set.

## Legacy evidence and retirement conversion

Reports without the current report identity are archive-only. Do not edit an
old report to add current fields. Run the current oracle to make current
evidence.

The optional `-migrate-legacy-retirements` path is available only with
`-update-union`. The converter checks the SHA-256 digest of the exact original
bytes against its allowlist. It copies retirement audit only. It does not copy
accepted signatures and it does not create a current report from an old
report. The conversion has no reverse path.

## Version-boundary measurement

The version-boundary job compares the current profile on the pinned server and
on the selected older server. Both runs use the explicit
`cross-version-common-v1` fixture. This fixture is a static common type set,
not a current fixture with unsupported columns removed at run time. The full
`current-fixture` keeps BFloat16. ClickHouse 24.8.14.39 refuses BFloat16, so a
report from the full fixture cannot be a cross-version report.

The oracle reads `CHGEN_ORACLE_FIXTURE_ID` and refuses every unknown value. The
report stores the exact DDL hash and the observed live signature. The two sides
also use the same experimental-type settings. Therefore the comparison cannot
silently compare different fixture shapes under one file label.

The job uses both `-cross-instance` and `-cross-version`. It reports named
differences but does not gate the pinned baseline. A server-version difference
can be a real ClickHouse behavior change and not a chgen defect.

Before the sampled oracle runs, the job runs the fixed type-boundary probe on
both servers. `probegate -known-version-boundary` requires executed witnesses
for trim over `FixedString`, `greatest` and `least` over a nullable
`SimpleAggregateFunction` marker, and `cityHash64` over nullable array shapes.
The gate checks each endpoint independently and refuses different query
settings, a missing cell, a changed expression, or an analysis-only witness.
The job uploads both probe artifacts, both oracle reports, and both difference
files. A completeness step fails if one producer did not create its artifact.

## Execution-oracle baseline

The execution oracle has a separate report and baseline. Run it with the
`execoracle` build tag and write `CHGEN_EXEC_OUT`. Update its baseline with:

```text
go run ./internal/tooling/cmd/execoraclegate -update \
  -i-measured-this=<version> \
  -report <path>
```

It also uses a subset-of-signatures gate. Its mandatory signatures are live
witnesses that prove that the harness can see value distortion and an explicit
generation refusal.
