# Front end gaps

chgen does not parse SQL. It reads the syntax tree that a front end gives
it, and it applies type rules to that tree. The front end is
`clickhouse-sql-parser`, a Go library. It is a separate implementation from
the ClickHouse server, thus it can refuse a statement that the server
accepts.

**Such a refusal is a chgen compatibility limitation, even when the cause is
upstream.** The type rule can be complete while the expression never reaches
the resolver. Record the parser boundary separately from type-inference
defects so we fix the right layer; do not send users away because of which
dependency contains the gap. `chgen check` labels parser refusals as unknown,
not as proof that ClickHouse rejects the SQL.

chgen does not implement a general SQL parser. A narrow parser-only
normalization is permitted only when it keeps every byte offset and line
number. The source SQL must stay unchanged. A test must also show the exact
accepted and refused boundary.

## The front end is an upstream dependency

`go.mod` pins the upstream module directly. It does not use a `replace`
directive. This lets another module import chgen and lets `go install` use a
released chgen module.

The upstream default branch is `master`, not `main`. A version command must
use the correct branch name.

## Schema-only adapter: EXCHANGE TABLES

The pinned upstream parser does not parse EXCHANGE. For schema inputs only,
`normalizeSchemaExchanges` recognizes `EXCHANGE TABLES a AND b` with an optional
ON CLUSTER clause. It changes EXCHANGE, TABLES, and AND to padded RENAME, TABLE,
and TO tokens in a parser-only copy. Every byte offset and newline is retained.
The original statement position selects a dedicated catalog exchange handler;
the operation is never applied as a rename. The upstream parser validates the
identifiers and cluster clause. Multiple pairs and qualified names are refused.

Quoted tokens and comments are skipped during recognition. Tests pin those
boundaries and check the complete exchanged definitions. This adapter does not
change query SQL, the upstream dependency, or the executable-query grammar.

## Open gap: sub-second INTERVAL units

Measured on ClickHouse 25.8.29.51, the server accepts both units:

| Expression | Server type |
| --- | --- |
| `dt + INTERVAL 1 MICROSECOND` | `DateTime64(6)` |
| `dt + INTERVAL 1 NANOSECOND` | `DateTime64(9)` |

The front end refuses both. Its `intervalUnits` set holds only MILLISECOND,
SECOND, MINUTE, HOUR, DAY, WEEK, MONTH, QUARTER and YEAR, thus such an
expression stops at the parse step and never reaches the type resolver.

The chgen rule for the two units is complete: `intervalUnitPrecision` covers
them, and the correct type comes out as soon as the front end lets the
expression through.

`TestSubSecondIntervalUnitsStayUnreachable` in
`interval_temporal_test.go` pins the gap. It asserts the refusal, not
a type, thus it fails when the front end gains the units. That failure is
the signal to move the two units into the measured matrix in
`TestIntervalArithmeticResultType`.

## Closed gap, kept as the worked example: a prefix operator in a CASE operand

From v0.5.5 the front end refused a prefix operator in the operand of the
simple CASE form, although ClickHouse accepts it:

```sql
CASE -1 WHEN 1 THEN 5 END
```

A bisection named the cause: commit b5bc152 (#305). Its keyword
disambiguator reads CASE as a column name when an operator follows.
`normalizeCasePrefixOperands` closes this one gap in the parser-only copy.
It replaces one horizontal space after CASE with `(` and one horizontal
space before WHEN with `)`. The copy has the same byte length and line
breaks as the source. Generated runtime SQL stays unchanged.

The normalization covers `+`, `-`, and `NOT`. It skips quoted text, comments,
parentheses, arrays, and nested CASE expressions when it finds the matching
WHEN. If the change needs a different byte offset or line break, chgen refuses
the query. `parser_normalize_test.go` records the six forms from the former
fork and the forms that must stay unchanged.

Two lessons from this example:

**Measure the boundary of a gap. Do not trust the report of one failing
case.** The first record of this gap named a negative literal. The gap was
wider: EVERY prefix operator in the CASE operand was refused, `NOT` as well
as the minus. A rule taken from one failing case would have been too narrow,
and the fix would have left part of the gap open.

**A version bump can trade one gap for another.** Release v0.5.5 fixed the
first argument of CAST, which could not hold an operator before it, and it
broke the CASE operand in the same release. A green test suite does not show
such a trade: the test suite pins what chgen already knows, and a new gap is
in the part of the grammar that no test reaches yet. The CASE findings
appeared ONLY in the type oracle report, as two CHGEN_ERROR entries that
read `parse: expected ')'`.

Thus a version bump of the front end needs the type oracle on both grammars,
against one and the same live server, and not only a green `go test ./...`.
Compare the two reports with `go run ./internal/tooling/cmd/oraclediff`.
