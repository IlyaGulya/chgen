# ClickHouse type transport

This document records the measured Go transport boundary for the supported
type families. The measurements use ClickHouse 25.8.29.51 and
clickhouse-go v2.47.0. They use real table columns.

## Wide integers

chgen refuses `Int128`, `UInt128`, `Int256`, and `UInt256` in every shape.
The native driver preserves valid values in some read shapes, but the full
transport is not safe:

- A Map with a wide integer can panic during `Rows.Next`.
- A nil pointer in a non-Nullable wide integer can become zero.
- A value outside the signed or unsigned range can wrap to another value.
- A text parameter that contains `big.Int` Array elements can use the Go
  structure text instead of the decimal integer text.

These changes can occur without an error. The refusal propagates through
Nullable, Array, LowCardinality, Map, and SimpleAggregateFunction. The
execution oracle keeps scalar, wrapper, container, and nested-container
cases as mandatory generation guards. These cases prove the refusal. They do
not run the unsafe driver path.

## Explicit refusals

chgen refuses these families:

- `Tuple` and `Nested`: named and unnamed Tuple values use different driver
  shapes. A Tuple that contains a wide integer can scan as nil with no error.
- `Variant` and `Dynamic`: the driver keeps the runtime alternative in a
  wrapper. The current generated type model cannot declare that alternative.
- `JSON`: the driver representation depends on connection settings that
  generated code does not control.
- `Point`, `Ring`, `LineString`, `Polygon`, `MultiLineString`, and
  `MultiPolygon`: direct columns have exact `orb` values, but ClickHouse
  removes the Geo alias from some expression results. The server then returns
  the underlying Tuple or Array type. chgen must not generate an `orb` target
  until inference models this boundary.
- `AggregateFunction`: clickhouse-go cannot decode the raw state column.
  Use a `-Merge` combinator or `finalizeAggregation` before the value crosses
  the native protocol.

`SimpleAggregateFunction(f, T)` stays supported as `T` only when the schema
constructor has a measured rule. chgen rejects an unknown function. It also
rejects `sum` for a nonnumeric type and `groupArrayArray` for a non-Array
type. The execution oracle checks an exact read and insert round trip for
`SimpleAggregateFunction(sum, Int64)`.

## Evidence and status

The strict status source is
`testdata/clickhouse-support-overrides.json`. The generated manifest is
`testdata/clickhouse-support-manifest.json`. A supported or refused family has
an evidence identifier and a test witness. A missing verdict stays
`not_measured`.

`testdata/clickhouse-type-transport-evidence.json` records the measured native
and text driver hazards. `type_transport_evidence_test.go` requires every
hazard that supports a refusal. This evidence is separate from the execution
oracle because the oracle stops before it creates a table for a refused type.

The type oracle can find analysis errors. The execution oracle checks the
generated Go type and the native value. Both witnesses are necessary because
the server can report a type for an expression that it cannot execute, and a
driver can lose a value without a scan error.
