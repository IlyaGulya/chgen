# The wrapper grid

The wrapper grid is an ENUMERATION. It measures a closed cell space
against a live ClickHouse server and against chgen, and it stores both
answers, cell by cell, in a committed golden file.

It exists because a hand-built probe table cannot be trusted to find a
defect that nobody has thought of. Across earlier changes, the defect that
described a defect named the WRONG mechanism three times out of eight, and
twice only a measured table stopped a fix from making the behaviour worse.
Such a table was built by hand, per ticket, AFTER the defect was known.
The grid removes that order: it measures first, and the divergences it
reports need no hypothesis.

## What the grid measures

Each cell is one expression. The grid asks two questions about it:

- what type does the SERVER give, or which error code does it answer; and
- what type does chgen infer, or does chgen refuse.

The cell space is the product of three closed sets.

1. The registry tables, which hold 237 table uses: `functionRegistry`
   (161), `higherOrderArrayFunctions` (9), `sizedConstructors` (59) and
   `simpleStateSupportedBases` (8), together with the 25 tokens of
   `operatorCatalog`.
2. The CLOSED wrapper alphabet: bare, `LowCardinality`, `Nullable`,
   `LowCardinality(Nullable(T))`, `SimpleAggregateFunction(f, T)`,
   `SimpleAggregateFunction(f, Nullable(T))` and
   `SimpleAggregateFunction(f, LowCardinality(T))`.
3. One or two representative base types for each accepted domain.

## The cell count

The first committed run holds **3226 cells**:

| Family | Cells | Source |
|---|---|---|
| `fn` | 1844 | `functionRegistry` |
| `sized` | 708 | `sizedConstructors` |
| `op` | 398 | `operatorCatalog` |
| `var` | 126 | the variadic mixed-type lane |
| `simplestate` | 96 | `simpleStateSupportedBases` |
| `hof` | 54 | `higherOrderArrayFunctions` |

The count is DERIVED, never declared. An entry is enumerated over each
wrapper of the alphabet, and for each wrapper it takes at most two
representative base types that its measured domain accepts. Two is the
smallest number that still separates a rule which READS the argument type
from a rule that returns a constant.

An entry with NO declared domain gets every base type of the grid. That is
the point: 81 of the 161 `functionRegistry` entries declare no argument
domain, and the server's answer for those cells is what derives it.

A NULLARY entry is enumerated exactly once, with the address
`fn/<name>/nullary`. `today()` and `row_number()` spell the same call
whatever the alphabet says, so enumerating them per wrapper would count
one measurement twelve times and inflate the cell count without adding
evidence.

## The variadic rule

Every variadic entry is enumerated with MIXED argument types, in BOTH
orders.

This lane is not an extra. A grid that gives one type to every position
cannot see a rule that reads only the first argument: `array(i8, i8)` and
a correct `array(i8, i32)` both look right when the rule is wrong. Both
orders are needed because a rule that reads only the first argument and a
rule that reads only the last are different defects, and one order catches
only one of them.

`TestGridVariadicLaneMixesTypes` fails if either property is lost.

## Regeneration

The golden is REGENERATED from the server, never edited by hand:

```
go test -tags fuzzoracle ./internal/engine -run TestWrapperGrid \
    -chgen-grid-url http://localhost:18123 -chgen-grid-regenerate
```

Without `-chgen-grid-regenerate` the same command COMPARES and fails on
any difference. Regeneration therefore cannot happen by accident: it needs
an explicit flag that no ordinary run and no CI run passes.

The golden is one line per cell, sorted by a stable text address, with the
fields `id`, `verdict`, `server_type`, `server_code`, `chgen_type` and
`sql`. A rule change shows in review as the exact set of cells whose
answer moved, with the old and the new answer side by side. The address is
text and not a position, so a new registry entry in the middle of a table
cannot renumber the file.

A hand-written expectation is forbidden because it would be a second copy
of the type rules, and the two copies would drift. The header of the file
says so, and `TestGridGoldenCarriesTheRegenerationBanner` keeps the header
honest.

## Reproducibility

The grid is BYTE-reproducible against one server. Two consecutive
regenerations on ClickHouse 25.8.29.51 produced identical files.

An enumeration has no excuse to be otherwise. The oracle totals elsewhere
in this project move between runs because those runs SAMPLE at random; the
grid samples nothing. Every map is walked over its sorted key list, every
slice keeps its declared order, and the records are sorted by address
before they are written.
`TestGridEnumerationIsDeterministic` builds the grid five times in one
process and fails on any difference, which catches a forgotten sort
without needing a second process.

One value in the file is a property of the SERVER and not of the grid: the
`clickhouse_version` header. A different server version may legitimately
move cells, and the version line is what tells a reviewer that this is the
cause.

## The grid must not shrink

`TestGridDoesNotShrink` reads the per-family counts from the golden header
and compares them with the live enumeration. It runs in the DEFAULT test
run, with no server. A family that loses cells fails the build and the
failure names the family.

A count that only ever goes down without a reason is how coverage dies.
The gate has already earned itself once: it caught the intended removal of
88 duplicate nullary cells and demanded that the reduction be justified
and regenerated rather than absorbed in silence.

## What is EXCLUDED, and why

A cell that is illegal for a reason unrelated to types is not evidence
about typing. Counting it would bank noise as coverage. The grid
enumerates such cells, marks them `EXCLUDED` and keeps them in the file,
so the exclusion itself stays reviewable.

The closed exclusion list, with the measured cause of each:

| Code | Name | Why it is not type evidence |
|---|---|---|
| 184 | `ILLEGAL_AGGREGATION` | A state cannot be built in the SELECT that merges it. `sumMerge(sumState(i32))` is Code 184 because of the aggregation context, not because of a type. |
| 215 | `NOT_AN_AGGREGATE` | The expression mixes an aggregate with a bare column. A query-shape error. |
| 36 | `BAD_ARGUMENTS` | The argument must be a constant. The grid puts columns in value positions by design, so `map(c_n_i32, c_n_i32)` is refused for a reason the grid created. |
| 6 | `CANNOT_PARSE_TEXT` | A VALUE error. `toInt32('abc')` is Code 6 although the type rule is perfectly defined. |
| 131 | `TOO_LARGE_STRING_SIZE` | A VALUE error. `toFixedString(c_bare_s, 2)` fails because the seed is 8 bytes long; the same call with a size of 8 types correctly. |
| 62 | `SYNTAX_ERROR` | The enumerator cannot spell the call. `toStartOfInterval` takes an INTERVAL literal and the constant renderer writes a quoted unit. An artifact of the grid, not a finding. |

Code 455, `SUSPICIOUS_TYPE_FOR_LOW_CARDINALITY`, never appears, because
the grid runs with `allow_suspicious_low_cardinality_types=1`. Code 455 is
a POLICY guard: the server types the expression perfectly well and refuses
it on policy. Recording it would store a policy opinion as a type rule.
Only Codes 43, 44 and 48 are type evidence in this family.

A server REFUSAL that is not in the list above is DATA and is kept. The
error code derives the argument domain of an entry that declares none, and
it is the only evidence that a cell chgen types is refused when it runs:
the regression produced `AggregateFunction(quantile, Bool)`, which is Code
134 at execution.

## The verdicts

| Verdict | Meaning |
|---|---|
| `AGREE` | Both sides gave the same type, after the accepted `Bool`/`UInt8` equivalence. |
| `BOTH_REFUSE` | The server refused and chgen refused. The wanted answer for an illegal cell. |
| `CHGEN_REFUSES_SERVER_ACCEPTS` | chgen is more strict than the server. A GAP, not a defect: an explicit refusal never produces a wrong type. |
| `CHGEN_TYPES_SERVER_REFUSES` | chgen produced a type for a cell the server refuses. The generated Go code would name a type that no query can return. |
| `MISMATCH` | Both sides gave a type and the types differ. The worst class: a silently wrong type. |
| `EXCLUDED` | The cell carries no type evidence; see the table above. |

## The first run

Measured on ClickHouse 25.8.29.51.

| Verdict | Cells |
|---|---|
| `AGREE` | 2246 |
| `CHGEN_REFUSES_SERVER_ACCEPTS` | 510 |
| `CHGEN_TYPES_SERVER_REFUSES` | 180 |
| `MISMATCH` | 144 |
| `BOTH_REFUSE` | 102 |
| `EXCLUDED` | 44 |

### Finding 1: chgen does not look through `SimpleAggregateFunction`

This one cause explains **120 of the 144 mismatches** and most of the 510
one-sided refusals. Measured, and confirmed by hand against the server:

```
assumeNotNull(safn)   server Int32                  chgen SimpleAggregateFunction(anyLast, Nullable(Int32))
toDate(safn_i32)      server Nullable(Date)         chgen Date
safn_i32 AND safn_i32 server Nullable(UInt8)        chgen Bool
anySimpleState(safn)  server SimpleAggregateFunction(any, Nullable(Int32))
                      chgen  SimpleAggregateFunction(any, SimpleAggregateFunction(anyLast, Nullable(Int32)))
```

chgen carries the whole `SimpleAggregateFunction` wrapper into the result
where the server reads the INNER value type, and it drops the inner
`Nullable` where the server keeps it. The nesting in the
`anySimpleState` row is the clearest form: chgen wraps a wrapper.

Note that a `HasPrefix` guard is the wrong fix. The server accepts a
Nullable inner type under `SimpleAggregateFunction` although it refuses
one under `AggregateFunction`, so a guard on the outer name would give a
new wrong answer. The grid records both families, so the fix can be
measured rather than guessed.

#### Result of the regression

the regression corrected 100 of these 120 cells. The census after the fix,
on the same server:

| Verdict | Before | After |
|---|---|---|
| `AGREE` | 2246 | 2346 |
| `CHGEN_REFUSES_SERVER_ACCEPTS` | 510 | 510 |
| `CHGEN_TYPES_SERVER_REFUSES` | 180 | 180 |
| `MISMATCH` | 144 | 44 |
| `BOTH_REFUSE` | 102 | 102 |
| `EXCLUDED` | 44 | 44 |

No cell moved away from `AGREE`, and the 24 mismatches that are not
`SimpleAggregateFunction` cells are unchanged.

The rule is that the server READS THROUGH the marker and answers about
the value inside it. An inner `Nullable` is therefore a property of the
VALUE, exactly as a top-level `Nullable` is. The fix reports it through
`branchArgumentNullable` at the three sites that compute the result
nullability, and it does NOT change `splitCHWrappers`: that function
answers "which wrappers are on the type", which is the answer a
marker-KEEPING function needs. A blanket look-through in the split gives
a new wrong answer for the container constructors, which keep the marker
whole:

```
array(safn)   Array(SimpleAggregateFunction(anyLast, Nullable(Int32)))
tuple(safn,i32) Tuple(SimpleAggregateFunction(anyLast, Nullable(Int32)), Int32)
```

The 20 cells that remain are `assumeNotNull`, the `groupArray` family,
`nullIf` and the `-SimpleState` combinator. Each is a `wrapperOpaque`
rule that owns its own wrappers, thus the transport cannot correct it
and the fix belongs in the rule. Their measured answers are recorded in
`simple_aggregate_nullable_inner_scalar_test.go`.

### Finding 2: `greatest` and `least` read only one argument

Caught by the variadic mixed-type lane, and confirmed by hand:

```
greatest(i32, u64)  server Int128         chgen Int32
greatest(u64, i32)  server Int128         chgen UInt64
greatest(i32, f64)  server Float64        chgen Int32
greatest(i32, dec)  server Decimal(18, 4) chgen Int32
greatest(fs, s)     server String         chgen FixedString(8)
least(d, dt)        server DateTime       chgen Date
```

chgen answers the type of the FIRST argument instead of the supertype of
all of them. The two orders prove the mechanism: the answer follows the
argument that comes first, so the rule reads position one and stops. This
is the regression class, in a different function, and a same-type grid
could not have seen it.

### Finding 3: the `-State` family keeps `LowCardinality`

```
quantileState(lc_i32)     server AggregateFunction(quantile, Int32)
                          chgen  AggregateFunction(quantile, LowCardinality(Int32))
quantileStateIf(n_i32, …) server AggregateFunction(quantile, Int32)
                          chgen  AggregateFunction(quantile, Nullable(Int32))
```

Every aggregate REMOVES `LowCardinality` at every depth, and the `-If`
form also removes `Nullable` from the state value type. chgen keeps both
inside the `AggregateFunction` parameter.

### Finding 4: `assumeNotNull` does not enter `LowCardinality`

```
assumeNotNull(lcn_i32)  server LowCardinality(Int32)
                        chgen  LowCardinality(Nullable(Int32))
```

The server removes the `Nullable` from INSIDE the `LowCardinality`; chgen
leaves it in place, thus it reports a Nullable result for a call whose
whole purpose is to remove the Nullable.

### Finding 5: chgen types calls the server refuses on the argument type

180 cells, and ALL 180 are Code 43, `ILLEGAL_TYPE_OF_ARGUMENT`. That one
code is genuine type evidence, and it was confirmed by hand:

```
c_bare_arr LIKE 'a'         Code 43   chgen UInt8
substring(c_bare_arr, 2)    Code 43   chgen Array(Int32)
arrayDistinct(c_bare_map)   Code 43   chgen Map(String, Int64)
```

The `arrayDistinct` group is worth separating from the known
the regression: here the argument is not an array at all, and chgen passes
the non-array type through unchanged instead of refusing. These are the
81 entries with no declared domain, and the grid has now measured the
domain that each of them needs.

#### Result of the regression

The 180 cells are 16 function names, but only FIVE domain rules. The
census after the fix, on the same server:

| Verdict | Before | After |
|---|---|---|
| `AGREE` | 2354 | 2384 |
| `CHGEN_REFUSES_SERVER_ACCEPTS` | 514 | 514 |
| `CHGEN_TYPES_SERVER_REFUSES` | 180 | 28 |
| `MISMATCH` | 32 | 36 |
| `BOTH_REFUSE` | 102 | 162 |
| `EXCLUDED` | 44 | 44 |

No `AGREE` cell was lost, and the set of `CHGEN_REFUSES_SERVER_ACCEPTS`
cells is IDENTICAL, cell for cell. No domain is too narrow.

The rules, each measured on both sides:

| Domain | Names | Accepts |
|---|---|---|
| `arrayArgumentDomain` | `arrayDistinct`, `arraySort`, `arraySlice`, `arrayResize`, `arrayStringConcat` | Array only |
| `hasArgumentDomain` | `has` | Array or Map |
| `textArgumentDomain` | `substring` | String, FixedString, Enum |
| `dateArgumentDomain` | `toStartOfDay` | Date, Date32, DateTime, DateTime64 |
| `isLogicOperandType` | `AND`, `OR` | narrow integers, floats, Bool |
| `isRegexpOperandType` | the `LIKE` family, `REGEXP` | String, FixedString, Enum |

Three of the six show why the ACCEPTING side must be measured too. A
domain copied from a neighbour would have been too narrow in each case:

- `has` takes a **Map** as well as an Array (`has(mp, 'a')` is UInt8),
  thus it cannot share `arrayArgumentDomain`.
- `substring` takes an **Enum** (`substring(e8, 2)` returns a row),
  which the `stringArgumentDomain` of `lower` and `trim` refuses.
- `toStartOfDay` takes **Date32**, which the `temporalBaseType` of
  `quantile` leaves out.

And one shows why the REFUSING side must be measured at the exact width:
`AND` and `OR` refuse the **wide** integers (`i128 AND i128` is Code 43)
although `integerBaseType` accepts them for the arithmetic aggregates.

The grid lost 58 cells, all in the `fn` family (1844 to 1786). The loss
is the intended consequence of the domains: an entry WITHOUT a domain is
enumerated over every base type, precisely so that the server derives the
domain it never declared, and an entry WITH one is enumerated over the
types its domain accepts. Those 58 cells had already delivered the
evidence they existed to collect.

Two groups remain, and neither is a missing domain:

- 24 cells are `dateDiff` and `date_diff`. They need the same date set
  that `toStartOfDay` uses, but they take the unit as their FIRST
  argument, and all three call sites of `checkArgumentDomain` read
  `args[0]`. Attaching the domain would test the String unit against a
  date set and refuse every legal call. The fix needs a
  domain-argument-INDEX on `functionSpec`, which is a change in
  `infer_function.go`.
- 4 cells were `has` with the same column in both positions, for example
  `has(c_bare_arr, c_bare_arr)`. These were Code **386**, `NO_COMMON_TYPE`,
  not Code 43. The refusal is about the PAIR and not about the first
  argument, thus no argument domain can state it. **the regression cured
  these four in the enumerator**; see "The element argument" below.

The 4 new `MISMATCH` cells are `arrayDistinct`, `arraySort`,
`arraySlice` and `arrayResize` over
`SimpleAggregateFunction(anyLast, Array(Int32))`. The server reads
through the marker and answers `Array(Int32)`; chgen keeps the marker,
because the four are `wrapperOpaque` with the rule
`firstFunctionArgument`. That is the Finding 1 class above, and it is
OLDER than the domain: before this change no array column could reach
those entries, so the cells were never enumerated. Only their visibility
is new.

### Cells that agree on a refusal

`array(i32, u64)` is Code 386, `NO_COMMON_TYPE`, and chgen refuses it too.
The pair is recorded rather than dropped, because a shared refusal is the
correct answer for that cell and a later change that starts typing it must
be visible.

### Known open defects, and the boundary of this grid

The four already-open tickets do NOT appear in the first run, and the
reason is a boundary of the instrument rather than evidence that they are
fixed. It is recorded here so that a later reader does not read their
absence as a pass.

- the regression (`-Merge` ignores the state's aggregate name) and
  the regression (`groupUniqArrayOrNull` typed but refused) name COMBINATOR
  forms. `functionRegistry` holds no `-Merge` and no `-OrNull` key: those
  families are built by the combinator logic, thus a registry-driven
  enumeration never spells them. Extending the grid with a combinator
  alphabet is the natural next step.
- the regression (`arrayDistinct` keeps `Nullable`) needs an ARRAY argument.
  `arrayDistinct` declares no argument domain, so the grid gives it the
  first two representative base types, which are integers. The grid does
  report `arrayDistinct` at every wrapper, but as Finding 5 above: chgen
  types a non-array argument instead of refusing it.
- the regression (`compatibleNamedArgTypes` looks through `LowCardinality`)
  needs a NAMED tuple, which the closed base-type list does not carry.

The four are therefore neither reproduced nor contradicted by this run.

## the regression: the composite marker inner, and a boundary that was misread

the regression corrected two conditions in `wrapper_transport.go` and
added the seventh letter of the alphabet,
`SimpleAggregateFunction(f, LowCardinality(T))`, with the key `saflc`.

### The rule that was wrong

`aggregateKeepsSimpleAggregateCondition` and
`caseFoldingKeepsSimpleAggregateCondition` both answered
`!innerNullable`. The measured rule, which
`simpleAggregateMarkerSurvives` in `supertype.go` already stated,
is that the marker survives a value read only when its inner type is a
BARE SCALAR. Both conditions now call that one helper.

The two rules AGREE on every scalar inner and on every Nullable inner.
They disagree only over a composite inner that carries no Nullable of its
own, and there the old condition gave a silently wrong type. Measured on
ClickHouse 25.8.29.51 with real columns of a real `AggregatingMergeTree`
table, with a row inserted, never over literals:

```
max(safarrb)          Array(Int32)          chgen kept the marker
max(safmapb)          Map(String, Int32)    chgen kept the marker
max(saftup)           Tuple(Int32, Int32)   chgen kept the marker
max(saflc)            String                chgen kept the marker
first_value(safarrb)  Array(Int32)          chgen kept the marker
```

### The two transports differ, and the difference is measured

They are separate functions on purpose. The `saflc` letter is what
proves the separation is real:

```
max(saflc)    String                  the aggregate also strips the LC
lower(saflc)  LowCardinality(String)  case folding KEEPS the LC
```

Both drop the marker. Only the aggregate strips the `LowCardinality` that
the marker was hiding, because every true aggregate removes
`LowCardinality` at every depth, and `lower` and `upper` are scalar. The
two answers come from the ordinary `LowCardinality` disposition that each
transport already declared, so the applier still holds no name check.

### A second defect that the same letter uncovered

`splitWrapperStack` did not enter the marker past an inner `Nullable`,
thus a rule received `LowCardinality(FixedString(8))` as its "bare" base
and the file's own contract, that a rule reasons about bare base types
only, was broken for that one shape. `caseFoldingFunctionType` tests its
base for the name `FixedString`, the wrapped name did not match, and the
answer collapsed to `String`:

```
lower(saflcfs)  server LowCardinality(FixedString(8))   chgen String
```

The split now lifts an inner `LowCardinality` into the stack, exactly as
it already lifted an inner `Nullable`, and the applier puts it back
inside the marker when the marker survives. That path is unreachable
under the measured marker rule; it is kept correct so that a later rule
change cannot turn it into a silently wrong type.

### Why LowCardinality is the letter that was needed

The ticket said the alphabet "only ever gives the marker a SCALAR inner
type". That is NOT what the fixture does, and the difference matters. The
`saf` letter is enumerated over every base type, containers included, so
`c_saf_arr`, `c_saf_tup` and `c_saf_map` already existed. Those cells did
report this defect class: the four `arrayDistinct`, `arraySort`,
`arraySlice` and `arrayResize` mismatches of the previous golden are it.

The real boundary was narrower. An entry takes at most two
representative base types of its domain, and a container base is never
among the first two that `max`, `any`, `argMax`, `lower` or `upper`
accept, so those families could not reach a composite inner at all. A
`LowCardinality` inner is composite for the marker rule AND legal for
every scalar domain, thus it reaches those families over their own
representative bases. No other composite inner does. That is why one
letter was added and not three.

Its legality follows the measured `lowCardinality` flag of `gridBase`
exactly, so the flag is reused and not copied. Measured by `CREATE TABLE`
of a real column, with `allow_suspicious_low_cardinality_types=1`:
`Int32`, `String`, `UUID` and the rest of the flagged set are accepted;
`Decimal(18, 4)`, `DateTime64(3)`, `Enum8`, `Array`, `Tuple` and `Map`
are Code 43.

### The census

| Verdict | Before | After |
|---|---|---|
| `AGREE` | 2396 | 2528 |
| `CHGEN_REFUSES_SERVER_ACCEPTS` | 514 | 769 |
| `BOTH_REFUSE` | 162 | 186 |
| `MISMATCH` | 24 | 94 |
| `EXCLUDED` | 44 | 50 |
| `CHGEN_TYPES_SERVER_REFUSES` | 28 | 32 |
| **cells** | **3168** | **3659** |

Every one of the 491 new cells is a `saflc` cell, and the 24 mismatches
that existed before are unchanged, cell for cell. No `AGREE` cell was
lost.

The per-family counts after the letter, which `TestGridDoesNotShrink`
reads from the golden header:

| Family | Before | After |
|---|---|---|
| `fn` | 1786 | 2080 |
| `sized` | 708 | 826 |
| `op` | 398 | 461 |
| `var` | 126 | 126 |
| `simplestate` | 96 | 112 |
| `hof` | 54 | 54 |

No family lost a cell. `var` and `hof` are unchanged because the
variadic lane fixes its own argument types and the higher-order lane
takes an Array data argument, which the `saflc` wrapper never spells.

The 70 new mismatches are the point of the letter, not a regression: they
are a defect class that the instrument could not see. They fall into
two groups, and neither of them is owned by this ticket.

- 52 cells: the server keeps a `LowCardinality` that chgen drops. The
  marker hid the wrapper from the transparent and conversion families,
  which had never been measured over it. The names are the `to*`
  conversions, `trim` and its two sides, `substring`, `hex`, `length`,
  `empty`, `notEmpty`, `cityHash64`, `toStartOfDay` and the four `LIKE`
  operators.
- 18 cells: `greatest`, `least`, `lagInFrame`, `leadInFrame`, `nullIf`
  and the four `-SimpleState` bases keep the marker. Each is a
  `wrapperOpaque` or value-preserving rule that owns its own wrappers,
  thus the transport cannot correct it. This is the group that Finding 1
  above already recorded as belonging to the rules.

Every refusal among the new cells is classified by its CODE. The 24
`BOTH_REFUSE` and the 4 `CHGEN_TYPES_SERVER_REFUSES` are all Code 43,
which is type evidence; the 4 `CHGEN_TYPES_SERVER_REFUSES` are the known
`dateDiff` domain-argument-index gap. The 6 `EXCLUDED` are Codes 131 and
62, both on the closed exclusion list. No Code 386 pair refusal and no
Code 455 policy refusal is recorded as type evidence. Code 386 WAS
recorded as type evidence for four `has` cells, which is the trap that
the regression found and cured in the enumerator.

## The element argument

The enumerator puts the SAME fixture column in every value position. For
most entries that is right. For a function whose later argument is the
ELEMENT of the first argument it is wrong, and it builds an expression
that no user would write.

`has` is such a function. Measured on 25.8.29.51 over real columns:

| Expression | Server answer |
|---|---|
| `has(c_bare_arr, c_bare_arr)` | Code 386, no supertype `Int32`, `Array(Int32)` |
| `has(c_bare_map, c_bare_map)` | Code 386, no supertype `String`, `Map(String, Int64)` |
| `has(c_bare_arr, c_bare_i32)` | `UInt8` |
| `has(c_bare_map, c_bare_s)` | `UInt8` |

Code 386 is a refusal about the PAIR of arguments. It is not evidence
that chgen typed something the server refuses, yet the grid recorded
`CHGEN_TYPES_SERVER_REFUSES` for four cells. That verdict was wrong.

Two cures were possible. Adding 386 to the closed exclusion list would
stop the false verdict but would LOSE the cell. Correcting the enumerator
keeps a real measurement and gains coverage. The second cure was chosen,
so **Code 386 stays off the exclusion list**.

`gridElementArgumentEntries` names the entries of this shape, and
`gridElementBaseOf` gives the element base key of a container base:
`arr` gives `i32`, and `map` gives `s`, because `has(mp, k)` tests the
map KEY. The element column is always taken **bare**: the wrapper
alphabet applies to the container argument, whose wrapper rule the cell
measures, and a cell that varied two wrappers at once could not say
which one moved the answer.

A survey of the whole registry found exactly ONE entry of this shape.
Sixteen entries have two or more value positions; the other fifteen (the
comparison family, `argMax`, `argMin`, `concat`, `map`, `nullIf` and the
`dateDiff` family) want two values of the SAME kind. `hasAll`, `hasAny`,
`indexOf`, `countEqual` and `mapContains` have no registry entry at all
today. `arrayElement` already declares `argSortConstInt` in position 2,
thus it never sent a container.

The list belongs in the registry, not beside the enumerator. The right
home is a field on `genSpec` — an `argSortElement` member of the
`argSort` enum would let `has` declare
`argSorts: []argSort{argSortValue, argSortElement}` and would delete
`gridElementArgumentEntries` entirely. That change is in
`gen_spec.go` and `registry.go`.

The four cells kept their addresses and now read `AGREE`. No cell was
added and none was removed.

## Where the code is

| File | Role |
|---|---|
| `wrappergrid_fixture_test.go` | The closed wrapper alphabet, the representative base types and the derived fixture DDL. Untagged. |
| `wrappergrid_cells_test.go` | The cell enumeration over the registry tables. Untagged. |
| `wrappergrid_shape_test.go` | The gates that need no server: uniqueness, determinism, the no-shrink gate and the variadic rule. Untagged. |
| `wrappergrid_test.go` | The measurement, the classification and the golden. Behind the `fuzzoracle` tag. |
| `testdata/wrapper_grid.golden` | The measured golden. |

The fixture of the grid is its OWN table `g`, not the oracle table `t`.
The oracle fixture holds one column per type that the random walk needed;
the grid needs one column per (wrapper, base type) pair of a closed
alphabet, so that a cell can be addressed by name instead of found by
chance. The DDL is derived from the same cell list that addresses the
probes, thus a column cannot exist without a cell.
