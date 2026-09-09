# v0.1.8: explicit result type contracts

Status: implemented. The user-facing contract and exact release boundary are
documented in [Query source](query-source.md#explicit-clickhouse-result-contracts).
The sections below retain the design rationale.

## Goal

Support the common numeric date-key functions without annotations, and give
clients a narrow escape hatch for result expressions whose types chgen cannot
infer. An asserted type must not be presented as a statically proven type.

## Findings in v0.1.7

- `toYYYYMMDD` already has a UInt32 registry rule, wrapper/domain tests, and a
  live execution witness. `toYYYYMM` and `toYYYYMMDDhhmmss` are missing.
- `-- result: GoName SQLAlias [GoType]` customizes names and Go representation.
  `ensureTopLevelQueryResults` still infers the ClickHouse type, and the resolver
  stores that inferred type even when GoType was explicitly supplied.
- `pinTypeHint` consequently advertises a workaround that does not work for a
  missing type rule. Correct the hint and its comment independently of the new
  annotation. Do not recommend CAST universally: analyzing its input may still
  require the missing rule.
- Existing generated read-side checks concern temporal values. They are not a
  general runtime comparison of server result types with a client assertion.

## 1. Numeric date-key family

| Function | Base result type |
| --- | --- |
| toYYYYMM | UInt32 |
| toYYYYMMDD | UInt32 |
| toYYYYMMDDhhmmss | UInt64 |

Reuse the existing registry machinery; avoid another name-specific resolver
branch. Share the argument domain where measurements support it, but declare
each function's result type explicitly.

The base result type is fixed only for valid inputs. Cover Date, Date32,
DateTime, DateTime64, Nullable, LowCardinality, combined wrappers, literal NULL,
arity errors, and invalid input types. Measure the optional constant timezone
argument for each temporal input family rather than widening the current
single-argument toYYYYMMDD rule by analogy. Run type and execution witnesses
on the pinned ClickHouse server, including month/day boundaries and null rows.

Primary references: [toYYYYMM implementation](https://github.com/ClickHouse/ClickHouse/blob/master/src/Functions/toYYYYMM.cpp)
and [toYYYYMMDDhhmmss implementation](https://github.com/ClickHouse/ClickHouse/blob/master/src/Functions/toYYYYMMDDhhmmss.cpp).
These establish the intended base types and timezone signatures; release
support is gated on measurements against the pinned server, not master alone.

## 2. A separate ClickHouse result contract

Syntax, in the query header:

```sql
-- name: ReadMonths :many
-- result-chtype: month UInt32
-- result: Month month uint32
SELECT clientMonthFunction(occurred_at) AS month
FROM events;
```

`-- result:` stays backward-compatible and is optional. `-- result-chtype:`
binds a ClickHouse type to a unique, explicit SQL output alias. Derive the Go
type from that contract when no Go override is supplied. Use the existing CH
type parser, consuming the whole remainder of the annotation, including spaces
inside Tuple or timezone types. Reject types the generator cannot represent.

This is a client assertion, not a cast, a SQL rewrite, or registration of the
function as supported. No global allow-all switch or extra opt-in flag is
needed: the local annotation is the opt-in.

### Resolution rules

1. Parse SQL and validate schema references, scopes, identifiers, placeholders,
   and known argument constraints independently of output-type inference.
2. Attempt ordinary inference. If it succeeds, require the asserted canonical
   CH type to match; a pin cannot contradict a known type or remove Nullable.
3. If inference fails with a classified unsupported-inference error, use the
   assertion for that output only. Never catch every error and replace it with
   the declared type. Unknown columns, malformed SQL, invalid known calls,
   ambiguous aliases, and unresolved parameter contracts remain errors.
4. Store both the resolved type and its provenance: inferred or asserted, with
   annotation location. Derive Go representation and temporal check plans from
   the resolved CH type. Do not overwrite an asserted type later in resolution.
5. Keep unsupported expressions out of the measured support roster. Generated
   code must identify asserted outputs for reviewers.

The implementation classifies missing registry rules and validates every
argument independently before using a contract. It deliberately accepts direct
unregistered calls rather than inferring through known operations with unknown
operands. There is no string match against error messages or unconditional skip
inside ensureTopLevelQueryResults. Scope construction and parameter inference
can fail before that function is reached.

### Runtime enforcement

For queries with asserted outputs, compare result metadata from
`Rows.ColumnTypes()` / `DatabaseTypeName()` against the expected output positions,
names, and canonical CH types before returning data. Cover both :one and :many.
Validate before Scan and do not silently skip the check on an empty result.
If usable metadata is unavailable, fail explicitly. Close rows on mismatch.

Measure metadata availability and wrapper/type spelling on both supported
driver versions. Normalize type syntax structurally, preserving Nullable,
LowCardinality, tuple shape, decimal scale, and timezone; do not silently accept
different types merely because they share a Go representation. This adds no
separate query or server connection at generation time.

A runtime type match proves the output contract, not the semantics or validity
of the unknown function. The server still validates and executes the SQL.

### Deliberate first-version boundary

Support outermost named outputs of ordinary SELECT queries (:one / :many).
Reject duplicate, unused, misplaced, or ambiguous pins and pins on :exec.
Reject pins on star expansion and set-operation outputs in this version.
Nested subqueries may be used when they resolve normally, but a top-level pin
does not supply a type to an unresolved CTE/subquery or an alias used elsewhere
in the query. Give an explicit boundary diagnostic for those cases.

This does not unblock arbitrary SQL: upstream syntax, scope support, parameter
typing, and generator-supported CH types remain necessary. Later nested or
set-operation contracts need explicit scope/branch identity, not a global map
of alias names. Preserve that possibility in the internal contract model.

## 3. Diagnostics

For a missing result type rule, explain that -- result controls Go mapping,
not CH inference. Suggest a local -- result-chtype contract only where the
above boundary permits it. Other errors should report their actual cause
without appending an inapplicable pin suggestion.

Conflicts name the query, alias, annotation file/line, asserted type and inferred
type. Runtime mismatches name the query, output position/alias, expected type
and actual server type. Never silently downgrade to unchecked scanning.

## Acceptance tests

- All three date-key functions generate without pins over the measured domain.
- An intentionally unregistered expression fails without a contract and succeeds
  with one; do not use toYYYYMM as the permanent negative test once it is supported.
- Existing -- result alone still does not bypass inference, with an honest error.
- Unknown columns, bad known calls, missing parameter types and malformed SQL
  remain failures even with a contract.
- Conflicting inferred types, nullable mismatches, malformed CH types, duplicate
  and unused pins, and unsupported scopes fail with useful locations.
- Generated code compiles with both supported Go/driver combinations.
- Live matching and mismatching metadata, empty results, nullable outputs and
  temporal results exercise :one and :many. A false assertion fails before data
  is returned, and rows are closed.
- Unannotated queries retain their SQL and generated behavior.

## Implementation order

1. Correct misleading diagnostics and add the date-key family measurements.
2. Add classified inference failures and independent structural validation.
3. Add parsed result contracts and provenance-aware resolution.
4. Add generated runtime metadata enforcement and driver compatibility tests.
5. Document boundaries, run full verification and live CI, then release v0.1.8.

Do not ship the annotation without runtime enforcement or with a broad error
catch. If that work needs a separate release, ship only the family and honest
diagnostics first; do not advertise an incomplete escape hatch.
