# Type mappings and generated API

`chgen` derives result and parameter types from the configured DDL. It refuses
a type when it cannot generate a Go value that the ClickHouse driver can
transport correctly.

The current type rules use ClickHouse 25.8.29.51 as their measured server
boundary. The generated file states this ClickHouse boundary. Another
ClickHouse major version can have different type rules.

The driver notes in this reference use measured clickhouse-go v2.47.0
behavior. The generated header does not state a driver version. Check this
reference when you change the driver version in an application.

## Generated declarations

For each configured package, `chgen` generates:

- `Queries`, which holds a `driver.Conn`
- `New`, which constructs `*Queries` and panics on a nil connection
- `Querier`, which contains all generated query methods
- `MockQuerier`, which has one `NameFunc` field for each query
- a SQL constant for each query
- a `NameParams` struct for each query
- a `NameRow` struct for a `:one` or `:many` query

The query annotation controls the method result:

| Annotation | Method result |
| --- | --- |
| `-- name: Name :many` | `([]NameRow, error)` |
| `-- name: Name :one` | `(NameRow, error)` |
| `-- name: Name :exec` | `error` |

A method always takes `context.Context` and a `NameParams` value. A query with
no parameters gets an empty `NameParams` struct. `MockQuerier` implements the
same interface. An unset mock function returns an error.

`chgen.arg('GoName')` creates one parameter field. Repeated uses of the same
name create one field and bind its value at each position. A parameter used
in incompatible type contexts is an error. Numeric coercions and
`Nullable(T)` versus `T` are compatible contexts.

These annotations can change the generated API when inference is not the
desired API:

- `-- param: GoName [GoType]` sets one parameter type.
- `-- result: GoName SQLAlias [GoType]` sets one result field. Other fields
  stay inferred.
- `-- result-capacity: SliceParameter` pre-allocates a `:many` result from a
  slice parameter.

A supported pointer parameter can define an optional value when its SQL
context accepts NULL. A nil pointer binds SQL NULL. A non-nil pointer binds
its value.

### Override type boundary

An explicit `GoType` must use this supported scalar set:

- `string` and `bool`
- `int8`, `int16`, `int32`, and `int64`
- `uint8`, `uint16`, `uint32`, and `uint64`
- `float32` and `float64`
- `time.Time`, `decimal.Decimal`, `net.IP`, and `uuid.UUID`
- `[]byte`
- `json.RawMessage` for results only

Pointers to supported scalars, slices of supported value types, and maps with
supported key and value types are also allowed. A map key must be a supported
comparable scalar. It cannot be a pointer or `net.IP`.

The whitelist checks that `chgen` can emit the Go type. An override
intentionally bypasses compatibility between that Go type and the inferred
ClickHouse type. The user is responsible for transport and scan correctness.
The ClickHouse driver can reject an incompatible choice. It can also return a
value that does not have the intended meaning.

The public `Generate` function also accepts a `Query` value that application
code constructs without `ParseQueryFiles`. A `time.Time` field with no
inferred `CHType` gets no temporal guard. Supply the inferred ClickHouse type
when generated code must apply a ClickHouse temporal boundary.

## Direct type mappings

| ClickHouse type | Generated Go type |
| --- | --- |
| `Bool` | `bool` |
| `Int8`, `Int16`, `Int32`, `Int64` | `int8`, `int16`, `int32`, `int64` |
| `UInt8`, `UInt16`, `UInt32`, `UInt64` | `uint8`, `uint16`, `uint32`, `uint64` |
| `Float32`, `Float64` | `float32`, `float64` |
| `String`, `FixedString(N)` | `string` |
| `Decimal(P, S)` | `decimal.Decimal` |
| `Decimal32/64/128/256` | `decimal.Decimal` |
| `UUID` | `uuid.UUID` from `github.com/google/uuid` |
| `IPv4`, `IPv6` | `net.IP` |
| `Enum8`, `Enum16` | `string` |
| `Date`, `Date32`, `DateTime`, `DateTime64` | `time.Time` |

`decimal.Decimal` comes from `github.com/shopspring/decimal`. `uuid.UUID`
comes from `github.com/google/uuid`. The clickhouse-go driver already uses
these packages. The generated imports do not add separate implementations.

## Wrapper and container mappings

Wrappers and containers apply recursively:

| ClickHouse shape | Generated Go shape |
| --- | --- |
| `LowCardinality(T)` | the Go type for `T` |
| `Nullable(T)` | `*T` |
| `Array(T)` | `[]T` |
| `Array(Nullable(T))` | `[]*T` |
| `Map(K, V)` | `map[K]V` |
| `Map(K, Nullable(V))` | `map[K]*V` |
| `SimpleAggregateFunction(f, T)` | the Go type for `T` |

A `Nullable` map key is an error. An IPv4 or IPv6 map key is also an error:
`net.IP` is a byte slice and is not a valid Go map key. The driver cannot
decode this ClickHouse shape. A UUID map key is supported because `uuid.UUID`
is a 16-byte array and is comparable.

`SimpleAggregateFunction(f, T)` is supported only for a measured `f` and `T`.

## Decimal values

All Decimal forms use `decimal.Decimal`, including Nullable, Array, and Map
values. A float target is not safe: the driver does not scan a Decimal column
into `*float64`, and a float can lose decimal precision.

`median` and `quantile` over a Decimal argument keep the input Decimal type
when ClickHouse reports that type.

## IP values

All IPv4 and IPv6 forms use `net.IP`. A string target is not correct for all
shapes. The driver can return raw binary for an IP value inside a container.
A string target can also hide the family of an IPv4-mapped IPv6 address.

`net.IP` keeps all 16 bytes of an address such as `::ffff:1.2.3.4`. Its
`String` method prints `1.2.3.4`, but the stored value still keeps the IPv6
family.

## UUID values

All UUID forms use `uuid.UUID`, including Nullable, Array, Map values, and Map
keys. A string target works for some scalar reads, but the driver refuses it
for container shapes. `uuid.UUID` is the driver's scan and bind type for all
supported shapes.

## Enum values

`Enum8` and `Enum16` use `string`. The value is the Enum name, not its number.
The driver refuses a numeric target and rejects a name that is not in the
declared value set.

The ClickHouse type in the catalog keeps the complete value set. When a
declaration omits numbers, `chgen` uses the same numbering rule as ClickHouse.
For example, `Enum8('a', 'b')` becomes
`Enum8('a' = 1, 'b' = 2)` in canonical type text.

## Temporal values

A generated parameter gets a temporal range check only when it has an inferred
temporal `CHType`. A scanned result with an inferred `DateTime64` type also has
a wrap check. An out-of-range value returns an error. It is not sent as a
different instant.

| Family | Guarded UTC range |
| --- | --- |
| `Date` | 1970-01-01 through 2149-06-06 23:59:59 |
| `Date32` | 1900-01-01 through 2299-12-31 23:59:59 |
| `DateTime` | 1970-01-01 through 2106-02-07 06:28:15 |
| `DateTime64(0..9)` | See the exact range below. |

The exact guarded `DateTime64` range is
1900-01-01 00:00:00.000000000 through
2262-04-11 23:47:16.854775807 UTC.

The `DateTime64` guard is narrower than the ClickHouse column range for some
precisions. clickhouse-go carries these values through signed 64-bit
nanoseconds. A later date can wrap without an error. Use SQL text instead of a
bound Go parameter when a `DateTime64` column must store a date after the
guarded limit.

The checks walk Array elements, Map keys, and Map values. A Nullable value is
checked only when it is not nil.

A fixed one-row `INSERT ... VALUES` with bare placeholders uses the native
batch path. This path keeps fractional seconds and supports temporal values in
Map shapes. A tuple that applies an expression to a placeholder uses
`conn.Exec`. When a parameter has an inferred temporal `CHType`, both paths
use the same range check.

An explicit `time.Time` annotation with no inferred `CHType` gets no temporal
guard. This case includes a `DROP PARTITION` parameter, because a partition
value has no column type from which to infer a temporal family.

## Nullable parameters and typed nil values

On the `conn.Exec` path, generated methods convert a nil pointer parameter to
an untyped nil. They dereference a non-nil pointer and pass its value. This
conversion stores SQL NULL and avoids a driver panic for types such as
`*uuid.UUID` and `*decimal.Decimal`.

A native one-row batch has a different rule. The generated method passes the
pointer directly to `batch.Append`. Measured clickhouse-go v2.47.0 handles
both nil and non-nil pointers safely on this path.

This protection does not apply to a call that application code writes directly
against `driver.Conn`. Do not pass a typed nil pointer to `conn.Exec`. Pass an
untyped `nil`, or use a generated method. In the measured driver version, typed
nil pointers for UUID and Decimal can panic because these types declare a
value-receiver `Value` method.

## Explicit type refusals

`chgen` refuses a type when the generated type model or the driver cannot keep
its value safely:

| ClickHouse family | Reason for refusal |
| --- | --- |
| `Int128`, `UInt128`, `Int256`, `UInt256` | Unsafe driver transport. |
| `Tuple`, `Nested` | Unsafe or different driver shapes. |
| `Variant`, `Dynamic` | The generated type cannot be dynamic. |
| `JSON` | The shape depends on connection settings. |
| Geo families | An expression can lose the Geo alias. |
| raw `AggregateFunction` state | The driver cannot decode it. |

Wide integers can wrap, change nil to zero, use a wrong text form, or cause a
driver panic in some container shapes. Tuple and Nested driver shapes can
differ, and some nested values can become nil without an error. Variant and
Dynamic values have a run-time alternative that a generated static type cannot
declare. JSON depends on connection settings that generated code does not
control. ClickHouse can remove a Geo alias from an expression result and
return a different underlying type.

Geo families include `Point`, `Ring`, `LineString`, `Polygon`,
`MultiLineString`, and `MultiPolygon`.

To read an `AggregateFunction` state, use a `-Merge` combinator or
`finalizeAggregation` so that an ordinary value crosses the protocol.
`SimpleAggregateFunction(f, T)` is supported as `T` only when `f` and `T` have
a measured rule.

An unknown type or a function without a type rule is also an explicit
generation error. `chgen` does not generate `any` as a fallback.

## Related reference

- [Configuration reference](configuration.md)
- [Measured transport boundary](clickhouse-type-transport.md)
- [ClickHouse support manifest](clickhouse-support-manifest.md)
- [ClickHouse type specimen catalog](clickhouse-type-specimens.md)
