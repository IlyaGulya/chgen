# Decision: record the measured server version, do not enforce it

Status: accepted. This closes the regression. Later work must not silently
reverse it; read "What must not be reversed" before you change it.

## The question

Every type rule in chgen was measured against one ClickHouse version,
25.8.29.51. Nothing in the tool or in the generated code records that
fact. A user on another version gets answers that were never measured
for their server, and neither the tool nor the user can see the risk.

Should chgen hold the version its rules were measured against, and
should it validate that version?

## The decision

**Record the measured version. Do not enforce it.**

Two of the four options are ruled out on measured facts, not on taste.

## Why "do nothing" is wrong

A version difference exists and it is not theoretical. On 24.8.14.39,
seven cells of the wrapper grid are a real `MISMATCH`, thus a 24.8 user
gets seven silently wrong types today and nothing tells them. A silently
wrong type is the worst defect class of this project, so the cheapest
option is the one that hides the defect.

## Why "ask the server at generation time" is CLOSED

The generator does not connect to anything, and giving it a connection
would change what the tool IS.

Measured, not assumed. `cmd/chgen/main.go` takes a `chgen.yaml` path and
`-version`; there is no DSN flag and no positional argument. The
generation path in `internal/project/run.go` is `expandInputEntries` ->
`ParseSchemaCatalogs` -> `ParseQueryFiles` -> `Generate` -> write the
file: it reads the schema and the query FILES only. Every non-test hit
for `database/sql`, `net/http` or `clickhouse-go` in the generator is
either a comment that records measured driver behaviour or a string that
`emit.go` WRITES INTO the generated file (`emit.go:380-389`).
Verified by grepping the import paths, not the word "clickhouse".

chgen is therefore a pure offline text-to-text generator. That is also
why it runs in CI with no service. A warning at generation time needs a
server connection that the tool does not have and does not otherwise
need, and the measured difference below does not justify paying for it.

The ORACLE path does connect, and that is a different program. Nothing
here stops the oracle from asking the server its version; it already
does, and the version gate depends on it.

## Why "a per-rule version range in the registry" is NOT justified

That option is the most correct and by far the most expensive: it
doubles the measurement work for every rule it covers. The measurement
says the difference is far too narrow to earn it.

| Comparison | Cells differing, of 7072 |
|---|---|
| 25.3.14.14 against 25.8.29.51 | **0** |
| 24.8.14.39 against 25.8.29.51 | 31 |

Inside the 25.x line the measured rules are STABLE: not one server
answer and not one verdict moved. The break is at the 24.8 boundary, and
it is not spread over the rule set. It falls in three named places:

- 21 cells: `trim` / `trimLeft` / `trimRight`;
- 4 cells: `greatest` / `least` over a `SimpleAggregateFunction` marker;
- 3 cells: `cityHash64` over array shapes.

All 7 of the real `MISMATCH` cells are about the `SimpleAggregateFunction`
MARKER, thus ONE rule family and not scattered damage.

The `trim` family narrows further. The 24.8-only blindness also appeared
as `concat`, `empty`, `substring`, `upper`, `LIKE`, `ILIKE`, `NOT LIKE`
and `v2-op-regexp`, which looks like a dozen version-dependent rules and
is not. Measured on both servers over a real `FixedString(8)` column:
each of those functions accepts `FixedString` DIRECTLY on 24.8 and
answers the same type as 25.8. They fail only with `trim(fs)` nested
inside, and the error text is verbatim the trim message. They are one
root cause seen through a caller. See the regression.

So the bill for a per-rule version range would be three registry entries
(`registry.go:346-348`) plus one marker family, described at the cost of
re-measuring everything. Do not pay for it yet.

## What "record" means here

The measured version is a fact about the rules, so it belongs with the
rules and in the output that the rules produce. When this decision was
taken, `25.8.29.51` appeared in many source comments (`temporal.go`,
`sized_constructor.go`, `constant_condition.go` and others) and in no
single constant, thus a reader could not ask the tool one question and
get one answer.

Recording it means one authoritative value that the generated code
carries, so that a reader on another major version can see that the
rules were never measured for their server. It tells a reader and it
enforces nothing, which is the honest limit of an offline tool.

**This shipped.** The value is the `MeasuredCHVersion` constant in
`version.go`, and `emit.go` puts it in the header of every
generated file. The version in a source comment stays what it always
was: a record of when ONE rule was measured. The constant is the version
of the rule set as a whole.

This is the shape that already works elsewhere in the project: the grid
goldens record the version they were measured on, and a gate compares
that recorded version against a live `SELECT version()`, refusing on
disagreement. The goldens can enforce because the oracle has a server.
The generator cannot, and must not pretend to.

## What must not be reversed

- **Do not "fix" `trim` to refuse `FixedString`.** On the pinned version
  chgen is RIGHT: 25.8.29.51 answers `String`. Refusing it would make
  chgen wrong on its own measured target in order to be right on an
  older one. See the regression.
- **Do not give the generator a database connection to validate a
  version.** That is a change in what the tool is, and this decision
  rejects it on cost. If a future need forces a connection, reopen this
  decision explicitly rather than adding one quietly.
- **Do not add a version range to a rule without the matrix to fill
  it.** A range that nobody measured is a second answer that nothing
  supports.
- **Do not read "the 25.x line is stable" as "server versions do not
  matter".** It is one measured window. When a new major version is
  added, re-measure the `SimpleAggregateFunction` marker family and the
  three `trim` entries FIRST: they are the known-divergent area.
- **Do not measure a version cell with `toTypeName`.** On 24.8,
  `toTypeName(trim(fs))` answers `FixedString(8)` quite happily and only
  EXECUTION gives Code 43. A matrix built on analysis would have
  concluded "both versions accept it, the answers differ" and would have
  missed that 24.8 cannot run it at all. Confirm every cell with
  `FORMAT TSVWithNamesAndTypes` over a real column, never over a
  literal, because the server folds constants.

## Evidence

- the regression: the version matrix and the difference table; the CI job
  that keeps the comparison visible. See
  `docs/ci-and-the-oracle-baseline.md`.
- the regression: `trim` over `FixedString` is version-dependent, and the
  attribution that reduces a dozen apparent differences to it.
- the regression: a caution against reading every difference as version
  dependence. `groupBitAnd`/`Or`/`Xor` answered `Bool` where the server
  widens to `UInt8` on the PINNED version. That was simply wrong, not
  version-dependent. Separate "changed between versions" from "was never
  right".
