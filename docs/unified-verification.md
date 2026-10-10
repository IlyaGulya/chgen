# Unified verification

`scripts/check.sh` runs the existing checks locally and in CI. It selects
checks, builds the needed tools, preserves their exit codes and collects a
common report. It does not change oracle rules, baselines or generated-code
fixtures.

## Run checks locally

Use Go 1.26.5 for the maintainer tools. The minimum-version and compatibility
suites select their own supported compilers.

```sh
./scripts/check.sh -profile quick -report /tmp/chgen-check-quick
```

`quick` runs the existing offline verification, including static analysis,
race checks and the offline oracle, followed by 30 seconds of native fuzzing.
It needs no ClickHouse server; it is not a replacement for the live checks.

`full` adds minimum Go, generated-code compatibility, API inventory drift,
generated-code execution, type and execution oracles, deterministic boundary
checks and cross-version comparison. Prepare two **disposable** servers first:
ClickHouse 25.8.29.51 and 24.8.14.39. These checks create and remove fixtures;
never point them at production.

```sh
./scripts/check.sh -profile full \
  -http http://localhost:8123 -native localhost:9000 \
  -candidate-http http://localhost:18123 \
  -report /tmp/chgen-check-full
```

Use `-profile deep` with the same endpoints for extended exploration. It
keeps the four fixed regression seeds, adds a daily UTC seed, increases type
samples from 2,000 to 10,000 and execution samples from 300 to 2,000, and
extends native fuzzing to ten minutes. The report records the chosen seed.
Use `-exploration-seed`, `-samples`, `-exec-samples` and `-fuzz-time` to
reproduce a run with the same budgets.

To run only part of the checks, use the same suite IDs CI uses:

```sh
./scripts/check.sh -list
./scripts/check.sh -suite boundary -http http://localhost:8123 \
  -report /tmp/chgen-check-boundary
./scripts/check.sh -suite compatibility -go 1.24.0 -driver v2.42.0 \
  -report /tmp/chgen-check-driver
```

The suite list is defined in `internal/tooling/cmd/verify/config.go`. CI supplies
compilers and healthy servers and calls these adapters. The previous offline
script remains in use underneath the `offline` adapter.

The offline suite checks deterministic regeneration of measured function
rules from both numeric and string profiles. The type suite remeasures them
and saves `function-measurements.json` and `string-function-measurements.json`,
then executes generated function queries with the supported drivers. Both
suites save `function-gaps.json`; the live suite discovers names from the
actual server. See [Automatic function rule measurement](function-rule-generation.md)
for the separate maintainer workflow that prepares candidates.

Both suites also save `argument-support-coverage.json`. It re-evaluates each
validated numeric/string recipe through the current public inference API and
records input type, call shape, inferred type or refusal diagnostic, server
analysis/execution types and codes. Counts are available globally and per
function. Build-dependent refusals include both measured witnesses and build
provenance; they are not counted as supported calls.

`chgen_refuses_server_accepts` requires successful analysis **and execution**.
If analysis succeeds but the measured values fail at execution and chgen also
refuses, the separate `chgen_refuses_execution_refuses` status preserves that
boundary instead of advertising a usable support gap. Both server codes stay
visible; this classification does not waive a failed execution of an accepted
chgen call.

The wrapper-grid section preserves its independent coordinates, SQL and
verdicts, including gaps and exclusions. Its denominator is separate because
grid cases overlap the numeric/string profiles, and grid measurements describe
analysis rather than execution. Neither section claims coverage of unmeasured
overloads. The existing roster-parity and live grid gates still check freshness.
Wrong inferred types, server refusals of accepted calls and execution/type
discrepancies fail the report command without hiding the offending JSON cells.

To inspect the report independently, without a server or writes:

```sh
go run ./internal/tooling/cmd/functionrules -argument-coverage
```

Missing, malformed or incomplete required evidence fails instead of silently
omitting a profile. This report complements name-level API coverage; a measured
function name is not a claim that every combination of its arguments works.

## Read the result

By default, each run creates a fresh directory under `verify-report/`.
An explicit `-report` requires a new or empty output directory. This prevents a gate from
reading a report left by an earlier run. `summary.md` links to each command log
and evidence artifact. `summary.json` records the configuration, commands,
explicit environment overrides, exit codes and durations. Logs keep the
original tools' explanations; the wrapper does not reinterpret their findings.

| Result | Meaning |
|---|---|
| `passed` | The selected checks completed successfully |
| `failed` | A check detected a failure or an unexpected difference |
| `refused` | Required infrastructure, execution or evidence was unavailable |
| `blocked` | A dependent gate could not run because its producer failed |
| `observed` | An expected cross-version difference was reported |

The command exits 0 for a passing run, 1 for failures and 2 for a refusal.
If both a failure and a refusal occur, it exits 2; both remain visible in the
individual checks. Live test adapters refuse a successful command that ran
no required tests or skipped a required test. The execution suite also runs
the full existing package: optional tests for other server environments may
skip, and those names are recorded explicitly. `TestExecOracle` must pass
and its artifact must satisfy the existing gate. Missing or empty evidence
is not a pass.

Passing tests do **not** mean complete ClickHouse API coverage. Syntax and API
coverage reports remain separate evidence linked from the common summary.
Cross-version sampled differences remain observations, while the executed
known boundary stays a gate. Baselines are never updated automatically.

## Combine CI results

CI retains separate jobs so checks run in parallel. Every job uploads a report
bundle even after failure. The final job combines them and requires all nine
cells, including both Go/driver compatibility pairs:

```sh
./scripts/check.sh -merge /tmp/downloaded-verification-bundles \
  -expect ci -report /tmp/chgen-check-combined
```

The merger copies logs and evidence into the combined bundle. A missing,
duplicate, invalid or incomplete required report refuses the combined result.
For a smaller local collection, use a comma-separated expected set such as
`-expect fuzz,boundary`.

## Reproduce and retain failures

Start with the suite, endpoints, seed and budgets in `summary.json`, using a
new report directory. Each check records its exact command and explicit
environment overrides. Tools are built once per invocation and retained under
the bundle's `tools` directory; the same tool is reused by later suites.

Native Go fuzzing saves minimized failures under `testdata/fuzz/`. CI retains
those files separately and caches the explored fuzz corpus between runs.
Promote a relevant minimized failure to a committed regression, fix the cause,
then rerun both the regression and the discovery suite. Do not make a failing
gate green by silently replacing its baseline.
