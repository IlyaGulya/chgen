# Survey: the historic fallback of the binary operators

Status: accepted. The survey is complete and the fallback is removed.

## Why this survey was necessary

`inferBinaryOperationType` ended with a historic fallback rule:

> If an operand is a Float, the result is Float64. If not, the type of the
> left operand is the result.

That rule is not a measured rule. It is a guess. The guess made `e8 || s`
answer `Enum8`, which is a silently wrong type, until the `||` operator
received its own measured rule. The same guess stayed live for every other
operator that came to the fallback, and no person had made a list of those
operators.

A wrong type is worse than a refusal. A refusal stops the build. A wrong
type makes the generated Go code read a column into a field of the wrong
kind, and nothing tells the user.

## Which operators came to the fallback

The parser makes a `BinaryOperation` node for a closed set of operators.
The set was read from the parser source
(`parser/parser_column.go`, function `parseInfix`) and then proved with a
test that parses each form and prints the operator token. Do not take this
list from the type oracle: the oracle grammar makes only the expressions
that it knows, thus an operator that the grammar never makes stays dark,
and a green oracle run says nothing about it.

| Operator token | Where it went before this change |
|---|---|
| `=`, `!=`, `<>`, `<`, `<=`, `>`, `>=` | the predicate rule |
| `AND`, `OR`, `IN`, `NOT IN` | the predicate rule |
| `LIKE`, `NOT LIKE`, `ILIKE`, `NOT ILIKE` | the predicate rule |
| `+`, `-`, `*`, `/`, `%` | the arithmetic rules |
| `\|\|` | its own measured rule |
| `==` | **the fallback** |
| `REGEXP` | **the fallback** |
| `::` | **the fallback** |
| `->` | the higher-order function rules, or **the fallback** |

Two results of the survey were not expected:

- `GLOBAL IN` and `GLOBAL NOT IN` do **not** come to the fallback. The
  parser normalizes both to the token `IN`, thus the predicate rule
  already covers them. A different `GlobalInOperation` node exists in the
  parser package, but the expression path does not make it.
- `==` came to the fallback. The predicate rule listed `=` but not `==`,
  and ClickHouse accepts both spellings. This is the same class of defect
  as `e8 || s`, and no person had seen it.

## The measurements

Every cell below was measured on ClickHouse 25.8.29.51 against the real
columns of the oracle table `uob.t`. No cell was measured over literals
alone, because ClickHouse folds constants and a rule measured over
literals lies.

Each cell has two witnesses:

- `toTypeName(<expr>)`, which answers from analysis and does not execute;
- `SELECT <expr> FROM t LIMIT 1`, which executes.

The two witnesses agreed in every cell of this survey. An expression that
analysis rejected also failed at execution, with the same code.

### `REGEXP`

`REGEXP` is the `match` function. The base result is `UInt8`, never an
operand type. The wrappers of the operand move into the result.

| Left operand type | Server answer | Execution |
|---|---|---|
| `String` | `UInt8` | ok |
| `FixedString(8)` | `UInt8` | ok |
| `Enum8` | `UInt8` | ok |
| `Enum16` | `UInt8` | ok |
| `LowCardinality(String)` | `LowCardinality(UInt8)` | ok |
| `Nullable(String)` | `Nullable(UInt8)` | ok |
| `LowCardinality(Nullable(String))` | `LowCardinality(Nullable(UInt8))` | ok |
| `Bool` | Code: 43 | Code: 43 |
| `UInt8`, `Int32`, `Int128` | Code: 43 | Code: 43 |
| `Float64` | Code: 43 | Code: 43 |
| `Decimal(18, 4)` | Code: 43 | Code: 43 |
| `Date`, `DateTime` | Code: 43 | Code: 43 |
| `UUID` | Code: 43 | Code: 43 |
| `IPv4` | Code: 43 | Code: 43 |
| `Array(String)` | Code: 43 | Code: 43 |
| `Map(String, Int64)` | Code: 43 | Code: 43 |
| `Tuple(Int32, String)` | Code: 43 | Code: 43 |

The refusal message is
`Illegal type <T> of argument of function match` (ILLEGAL_TYPE_OF_ARGUMENT).

The fallback answered `Enum8('a' = 1, 'zz' = 2)` for `e8 REGEXP 'a'`,
`Int32` for `i32 REGEXP 'a'` and `Date` for `d REGEXP 'a'`. The first is a
wrong type for a legal expression. The other two are types for an
expression that the server refuses.

### `==`

`==` is the same operator as `=`. Every measured cell agrees with `=`.

| Expression | Server answer | Execution |
|---|---|---|
| `e8 == s` | `UInt8` | ok |
| `e8 == e8` | `UInt8` | ok |
| `i32 == i64` | `UInt8` | ok |
| `f64 == i32` | `UInt8` | ok |
| `s == s` | `UInt8` | ok |
| `fs == s` | `UInt8` | ok |
| `d == dt` | `UInt8` | ok |
| `dt64 == dt` | `UInt8` | ok |
| `dec == dec` | `UInt8` | ok |
| `i128 == i128` | `UInt8` | ok |
| `uid == uid` | `UInt8` | ok |
| `ip4 == ip4` | `UInt8` | ok |
| `ip6 == ip6` | `UInt8` | ok |
| `arr_i == arr_i` | `UInt8` | ok |
| `tup == tup` | `UInt8` | ok |
| `m == m` | `UInt8` | ok |
| `lc == 'a'` | `LowCardinality(UInt8)` | ok |
| `lc == lc` | `UInt8` | ok |
| `ns == s` | `Nullable(UInt8)` | ok |
| `lc == ns` | `Nullable(UInt8)` | ok |
| `lcn == 'a'` | `LowCardinality(Nullable(UInt8))` | ok |

chgen reports `Bool` where the server reports `UInt8` for this family.
That is the accepted divergence of
[`decision-bool-vs-uint8.md`](decision-bool-vs-uint8.md), not a defect of
this survey. `==` now takes the same base as `=`, thus the two spellings
of one operator cannot give two different answers.

The fallback answered `Enum8('a' = 1, 'zz' = 2)` for `e8 == s`, `Int32`
for `i32 == i64` and `Float64` for `f64 == i32`.

### `::`

`::` is the cast operator. The result is the type that the right operand
names; no operand type reaches the result.

| Expression | Analysis | Execution |
|---|---|---|
| `i32 :: String` | `String` | ok |
| `e8 :: String` | `String` | ok |
| `lc :: String` | `String` | ok |
| `uid :: String` | `String` | ok |
| `d :: DateTime` | `DateTime` | ok |
| `dec :: Float64` | `Float64` | ok |
| `f64 :: Int32` | `Int32` | ok |
| `arr_i :: Array(Int64)` | `Array(Int64)` | ok |
| `s :: Int32` | `Int32` | Code: 6 |
| `ns :: Int64` | `Int64` | Code: 6 |

The two Code: 6 rows are a value that the target type cannot hold, not a
type failure. The analysis type is correct in those rows, and the analysis
type is what chgen must report.

chgen never gave a type here. The right operand is a type name, but the
inference read it as a column, thus the operator failed with the message
`column "String" is not present in FROM tables`. That message names the
wrong cause. The operator is now an explicit refusal that names the true
cause and points to the `CAST(x AS T)` form, which chgen does infer.

### `->`

`->` is the lambda arrow. A lambda has no result type of its own: only the
call that receives it has one. The higher-order array functions take the
lambda before the operand inference sees it, thus a lambda inside
`arrayMap` or `arrayFilter` never came to the fallback.

A lambda outside such a call did come to the fallback, where it took the
type of its own left side, which is the lambda **parameter**. That is a
type for an expression that the server has no result type for. The
operator is now an explicit refusal.

## What replaced the fallback

The fallback is gone. Every operator that came to it now has either a
measured rule or an explicit refusal:

| Operator | Result |
|---|---|
| `==` | measured: it joins the predicate rule of `=` |
| `REGEXP` | measured: `UInt8` base, with the operand wrappers, and a refusal for an operand type that the server refuses |
| `::` | explicit refusal that points to `CAST(x AS T)` |
| `->` | explicit refusal outside a higher-order call |
| any operator token that is new | explicit refusal |

The last row is the important one. A new operator in a later parser
version now refuses instead of taking a guessed type, thus the class of
defect that this survey closes cannot come back without a person seeing
it.

## The oracle was blind to all four operators

This survey was made by hand, and it had to be. The type oracle could not
have found any of the four wrong answers, because its grammar never made
any of the four operators. Measured at seed 42 with N=2000 on both
grammars: zero findings held `==`, zero held `REGEXP` and zero held `::`.
Grammar v2 held 37 findings with a `->` token, and every one of them was a lambda inside
`arrayMap`, `arrayAll`, `arrayCount` or another higher-order call, where
the call consumes the lambda and the operator never reaches the operand
inference. The standalone form, the only form that came to the fallback,
was never made.

A green oracle run over a grammar that cannot make the construction is not
evidence about the construction. The four defects lived through every run
of both grammars.

Grammar v2 now has a top-level lane, `fallbackOpsV2`, that makes all four
tokens directly. Grammar v1 is unchanged and stays byte-identical, thus
the first run series is still reproducible.

### The lane reproduces every one of the four defects

The proof required is that the new lane finds a defect that is already
fixed. The generator of this commit was run against the inference of
commit 691e0f7, the commit before the fix, on one and the same live
server. Every defect of this survey was found:

| Defect | Pre-fix chgen answer | Server | Class the lane reports | Found |
|---|---|---|---|---|
| `e8 == s` | `Enum8('a' = 1, 'zz' = 2)` | `UInt8` | MISMATCH | yes |
| `f64 == i32` | `Float64` | `UInt8` | MISMATCH | yes |
| `e8 REGEXP 'a'` | `Enum8('a' = 1, 'zz' = 2)` | `UInt8` | MISMATCH | yes |
| `d REGEXP 'a'` | `Date` | Code: 43 | CH_ERROR_43_CHGEN_TYPED | yes |

The fourth row needs its own counter and cannot use MISMATCH. The server
gives the expression no type, thus there is nothing to compare a type
against. The class that names it is the blindness counter
`CH_ERROR_43_CHGEN_TYPED`: chgen gave a type to an expression that the
server refuses with ILLEGAL_TYPE_OF_ARGUMENT. That counter is uncapped, so
it stays comparable between runs.

The two runs measured the same 21 refused-`REGEXP` expressions, thus the
counter compares like with like:

| Run | `v2-op-regexp-refused` sent | of them typed by chgen |
|---|---|---|
| pre-fix (691e0f7) | 21 | **21** |
| this commit | 21 | **0** |

The whole-run counter moved from 24 to 3 for the same reason. The
remaining 3 are older classes that this survey does not cover.

`::` and the bare `->` produce no MISMATCH on either side, because the
pre-fix code did not type them either: it failed with `column "String" is
not present in FROM tables` and `column "x" is not present in FROM
tables`. Those messages name the wrong cause but they are refusals, not
wrong types. The lane still carries both tokens, because the refusal is
now the measured behaviour and a later change that starts to type them
must be seen.

### What the new lane says about the current code

Run at seed 42 with N=2000 against this commit:

| Grammar | Before the lane | After the lane |
|---|---|---|
| v1 | `CH_ERROR` 348, `MISMATCH` 192, `OK` 1471 | unchanged, and the same `ch_error_digest` |
| v2 | `CHGEN_ERROR` 2, `CH_ERROR` 261, `MISMATCH` 322, `OK` 1426 | `CHGEN_ERROR` 8, `CH_ERROR` 272, `MISMATCH` 329, `OK` 1402 |

The new coverage surfaced **no new defect**.

- The 6 added `CHGEN_ERROR` entries are all `v2-op-cast`. They are the
  intended refusal of `::` that this survey specifies, not a defect.
- The added `MISMATCH` entries are all `v2-op-eqeq`, and every one of them
  is the accepted `Bool` against `UInt8` divergence of
  [`decision-bool-vs-uint8.md`](decision-bool-vs-uint8.md), in the same
  four wrapper shapes that `=` already reports.
- Every `REGEXP` cell with a legal operand is `OK`.
- The set of mismatch classes that are NOT the `Bool`-against-`UInt8`
  divergence is the same before and after: the known `LowCardinality`
  arithmetic classes and the known `Decimal` alias classes. The small
  count churn inside those classes is the random walk re-shuffling,
  because the new lane takes draws.
