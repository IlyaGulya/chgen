# Local expression type contracts

Use `chgen.assumeType(expression, 'ClickHouseType')` to supply a locally
verified type for a function chgen cannot infer. The contract belongs to that
expression in its actual lexical scope, not to an alias shared across CTEs.

```sql
-- name: ReadPage :many
WITH halves AS (
  SELECT chgen.assumeType(intDiv(id, toUInt64(2)), 'UInt64') AS half
  FROM events
)
SELECT toInt64(half) AS value FROM halves
WHERE half > chgen.arg('After');
```

The generated result is `int64`; `After` is `uint64`. The server receives the
original `intDiv` expression in parentheses, without the macro. chgen does
not insert a CAST, change nullability, or rewrite query semantics.

Each unknown call needs its own contract. Arguments must have independently
resolvable types: use an inner contract for another unknown call and a SQL
CAST for a parameter whose type cannot otherwise be inferred. Known surrounding
functions and operators still check their argument domains. A contract cannot
override a known result type, illegal arity, missing column, or invalid operation.
Nullable and LowCardinality wrappers must be specified exactly. Escape quotes
inside a parameterized type, for example
`chgen.assumeType(clientTime(id), 'DateTime64(3, \'UTC\')')`.

Contracts are supported in `:one` and `:many` SELECTs, including CTEs, derived
tables, predicates and typed lambda bodies. They are not supported in `:exec`. The older
`-- result-chtype:` remains an outer-result contract; `-- result:` remains a
Go-representation annotation.

## Trust, not a proof

If chgen can infer the expression and it matches the contract, `check` reports
no assumption. If the contract supplies an unknown type, `check` reports
`expression-type-asserted` with status `unknown`, while permitting generation.
`check -require-confirmed` rejects that project.

Generated methods check final result metadata before scanning, even on empty
results. That detects a wrong output type; it **cannot prove an intermediate
type or a predicate's semantics**. A wrong intermediate assumption can produce
a correctly typed but incorrect answer. Verify the specific expression on your
ClickHouse version, including NULLs and boundary values. `chgen describe` can
discover server-analyzed types; it does not execute a regression or prove values.

The public runtime regression executes an unregistered function through a CTE,
checks nullable values, and rejects a wrong final type both with and without rows
on the pinned ClickHouse server and both supported drivers.
