# The type boundary probe: `chgen probe` and `probegate`

## The problem

Every measured grid in this repository was taken by hand: a person starts an
ephemeral ClickHouse container, runs the sweep, and reads the result.
Nothing watches the boundary between two SERVER VERSIONS. An upgrade of the
pinned image would arrive silently, and the first witness would be a user
whose generated Go does not compile, or compiles and fails at run time
against a query that used to work.

`version-matrix` (see `docs/ci-and-the-oracle-baseline.md`) already runs the
full type-oracle sweep against two server versions and reports the
difference — but it does not gate the build, and it cannot: the oracle
samples a grammar at random, so a single-seed cell can be green while a
defect is live on another seed, and the oracle's own counts move with the
uptime of the server on one and the same commit. Gating on that sample would
gate on noise as often as on signal.

`chgen probe` and `probegate` answer a narrower question with a tool built
to gate: not "which of thousands of randomly sampled expressions changed",
but "did any of these committed, hand-picked, ALWAYS-ASKED questions change
their answer". That narrower question can be gated safely, because it does
not depend on a random draw.

## The uptime problem, addressed directly

The type oracle's instability is measured and recorded: on one commit, one
seed, and one server version, `CH_ERROR` and `MISMATCH` counts moved from
347/1457 findings to 348/1456 findings across two hours of uptime on the
same container (`typeoracle_fuzz_test.go`). That
instability is a property of the SAMPLE, not of the server answering any
one fixed query inconsistently: the random expression generator draws from
a population the server answers a shifting subset of as it warms, so two
runs of the oracle do not literally ask the server the same questions even
with the same seed.

A probe cell is not a sample. Every cell in `internal/typeboundary.Catalog` is
one committed, named, fixed SELECT over real table columns (never a
literal — ClickHouse's constant folder answers a literal probe before the
type rule under test ever runs). Sent to one warm server twice, a fixed
cell gives the same verdict both times. This is proven, not assumed:
`internal/typeboundary/probe_test.go`'s `TestFixedQueryIsStableAcrossTime` runs
the full catalog twice, forty seconds apart, against one live server and
asserts the two artifacts compare identical; it passed on ClickHouse
25.8.29.51 (measured in this session).

Because of this, `probegate` compares two probe artifacts from ANY two
server runs directly — it does not need the oracle difference tool's
`-cross-instance` escape hatch, and comparing across a version boundary is
exactly the tool's purpose, not a case it must refuse.

This is a narrower promise than "the ClickHouse server is fully
deterministic", not a claim that it is. A probe artifact still carries the
`server_run` (boot moment) and `server_uptime_s` of the run that produced
it, in the same shape the type oracle's own report uses
(`internal/oraclereport.Report`), and `probegate` always prints both sides'
identity in its verdict. Nothing here reads that identity into a pass/fail
decision — the artifact only records it, so a reader who suspects a future
cell of secretly depending on server age can see the exact instance and
re-measure it, rather than trusting a comparison that hid the axis.

## The artifact

`chgen probe -url <http-url> -out probe.json` creates a private fixture
table (`chgen_probe_t`, `internal/typeboundary/schema.go`), runs every cell of
`Catalog` against it as `SELECT toTypeName(expr), ignore(expr) FROM
chgen_probe_t`, and writes the result as JSON. The `ignore(expr)` half is
not decoration: `toTypeName` alone answers from ANALYSIS and never runs the
expression, so a type the server would refuse to compute at run time could
be recorded as if it were safe. This is the exact technique and the exact
motivating case (`toStartOfInterval` over a `Date`) already documented on
the type oracle's own witness in `typeoracle_fuzz_test.go`; the probe
reuses it rather than re-deriving it.

Each cell records either the analysed type (`type_name`) or the numeric
ClickHouse error code (`error_code`) — never a free-text message, because
wording is not a contract and changes between versions without the
underlying rule changing.

## The gate

`probegate old.json new.json` compares two artifacts cell by cell and names
every move, classified into one of four kinds:

| Kind               | Meaning                                                         |
|---------------------|------------------------------------------------------------------|
| `TYPED_TO_REFUSED`  | the server used to answer a type, now refuses. A narrowing.      |
| `REFUSED_TO_TYPED`  | the server used to refuse, now answers a type. A widening.       |
| `RETYPED`           | both answer, but the type changed.                                |
| `REFUSAL_CHANGED`   | both refuse, but the numeric code changed.                        |

This follows the project's governing rule directly: a refusal wider than
the server's breaks a query that runs, so a widening is recorded with the
same weight as a narrowing, never folded into one "differs" bit.

Exit codes match the convention already used by `oraclegate` and
`oraclediff`: `0` identical, `1` a real, named difference, `2` the
comparison could not answer the question (a missing file, an empty
artifact, or two artifacts that do not share a catalog).

## The self-test

`probegate -selftest` builds two artifacts entirely from
`internal/typeboundary.Catalog` — the live catalog, so the injected shape comes
from the same code that produces a real artifact, never from a remembered
format — flips exactly one cell from a type to a refusal, and checks that
`Compare` names exactly that cell with kind `TYPED_TO_REFUSED`. Anything
else, including "no difference reported", fails the self-test with a
non-zero exit. This is the check the CI job runs first, before the real
comparison: a gate that cannot tell "no difference" from "never ran" is
worse than no gate, and this project has hit that exact failure before.

## Proof against a real second version

`docs/ci-and-the-oracle-baseline.md` records that ClickHouse 24.8.14.39
differs from the pinned 25.8.29.51 in three named families:
`trim`/`trimLeft`/`trimRight` over `FixedString`, `greatest`/`least` over a
`SimpleAggregateFunction` marker, and `cityHash64` over array shapes. The
probe fixture uses `AggregatingMergeTree`. The fixed catalog carries all
three families. It has three direct trim cells, four `greatest` and `least`
cells over nullable `SimpleAggregateFunction` markers, and three
`cityHash64` cells over nullable array shapes. The nullable array seed holds
a real NULL. This value is necessary to execute the 24.8 boundary.

`probegate -known-version-boundary` checks the exact expression, analysis
answer, and execution answer for each required cell. It also checks the two
server versions and the fixture, seed, and matrix hashes. It refuses a
missing or renamed cell. Other probe cells can be added without changing
this boundary contract.

Measured directly against a real 24.8.14.39 container (this session):

```
$ probe -url <25.8.29.51> -out old.json
$ probe -url <24.8.14.39> -out new.json
$ probegate old.json new.json
MOVED (11 of 43 cells)
  old server: 25.8.29.51 (server_run=... uptime_s=569)
  new server: 24.8.14.39 (server_run=... uptime_s=6)
  datetime_sub: TYPED_TO_REFUSED  old=Decimal(18, 3) new=CH_ERROR(43)
  v24_8_cityhash64_array_nullable: TYPED_TO_REFUSED  old=UInt64 new=CH_ERROR(48)
  v24_8_cityhash64_saf_array_array_nullable: TYPED_TO_REFUSED  old=UInt64 new=CH_ERROR(48)
  v24_8_cityhash64_saf_array_nullable: TYPED_TO_REFUSED  old=UInt64 new=CH_ERROR(48)
  v24_8_greatest_safn_i32: RETYPED  old=SimpleAggregateFunction(anyLast, Nullable(Int32)) new=Nullable(Int32)
  v24_8_greatest_safn_u64: RETYPED  old=SimpleAggregateFunction(anyLast, Nullable(UInt64)) new=Nullable(UInt64)
  v24_8_least_safn_i32: RETYPED  old=SimpleAggregateFunction(anyLast, Nullable(Int32)) new=Nullable(Int32)
  v24_8_least_safn_u64: RETYPED  old=SimpleAggregateFunction(anyLast, Nullable(UInt64)) new=Nullable(UInt64)
  v24_8_trim_fixedstring: TYPED_TO_REFUSED  old=String new=CH_ERROR(43)
  v24_8_trimleft_fixedstring: TYPED_TO_REFUSED  old=String new=CH_ERROR(43)
  v24_8_trimright_fixedstring: TYPED_TO_REFUSED  old=String new=CH_ERROR(43)
```

The gate names all three documented families. It also includes the separate
`datetime_sub` boundary that the first probe found. The two 25.8.29.51 runs
used to produce this
comparison's baseline side, taken 22 seconds and 569 seconds of uptime
apart, compared IDENTICAL to each other, which is the direct empirical
check that uptime alone does not move a probe verdict.

## CI wiring

The `type-boundary-probe` job in `.github/workflows/ci.yml`:

1. Builds `probe` and `probegate`.
2. Runs `probegate -selftest` and fails the job if it does not pass.
3. Probes the pinned server (25.8.29.51, the same pin every other oracle job
   uses).
4. Gates the result against `testdata/probe-baseline-25.8.29.51.json`.
5. Uploads the current probe artifact regardless of outcome.

This job gates the pinned probe baseline. The `version-matrix` job also gates
the fixed known-boundary witnesses, but it does not gate on the sampled oracle
difference. A probe cell's
fixed nature is exactly what makes that safe, per the reasoning above.

## Running it by hand

There is no Makefile or task runner in this repository; run the two
binaries directly, the same way every other oracle tool here is run:

```bash
go build -o /tmp/probe ./internal/tooling/cmd/probe
go build -o /tmp/probegate ./internal/tooling/cmd/probegate

# prove the gate can name a known difference before trusting it
/tmp/probegate -selftest

# take a snapshot
/tmp/probe -url http://default:PASSWORD@localhost:8123 -out probe.json

# compare it against the committed baseline
/tmp/probegate testdata/probe-baseline-25.8.29.51.json probe.json
```

`go run` must not be used for `probegate` in a script that branches on the
exit code: it collapses every non-zero exit into `1` and prints "exit
status N" to stderr instead of the real code, which erases the difference
between "a real boundary move" (1) and "the comparison could not answer the
question" (2 or more). Build the binary and run it directly, as the CI job
does.

## Regenerating the baseline

`testdata/probe-baseline-25.8.29.51.json` is committed and reviewed
like the oracle's own baseline (`testdata/oracle-baseline.json`): a
git diff shows exactly which named cell moved and how, never a count. To
regenerate it after a deliberate change to the pinned server version or the
catalog:

```bash
go build -o /tmp/probe ./internal/tooling/cmd/probe
/tmp/probe -url http://default:PASSWORD@localhost:8123 -out testdata/probe-baseline-25.8.29.51.json
```

Then edit the written `server.server_run` and `server.server_uptime_s`
fields back to `0`: those two fields identify one specific ephemeral
container's boot moment, which is meaningless once committed, and leaving a
real value in a checked-in file would read as more precision than the file
actually carries. `probegate` never compares them; only `Cells` and
`CatalogNames` are load-bearing.
