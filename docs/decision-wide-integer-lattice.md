# The wide integer rules

Measured on ClickHouse 25.8.29.51 with real table columns. ClickHouse folds
constants, thus a rule measured over literals lies. Every number below comes
from `DESCRIBE (SELECT <expr> AS x FROM t)` over a column, never a literal.

## Why the version matters

ClickHouse 26.7 turns `use_variant_as_common_type` ON by default. With that
default `if(cond, i64, u64)` gives `Variant(Int64, UInt64)` instead of an
error, which is not the lattice that chgen models. A measurement taken on
26.7 with the default settings does not describe the rules in this file.

## Arithmetic

The existing promotion algorithm was already correct; only the 128 bit and
256 bit sizes were missing from the lookup tables. `promotedArithmeticSize`
keeps the size at and above 64 bits, which is the right behaviour at 128 and
256 bits as well.

## Mixed sign supertype

A mixed sign pair needs a signed type that holds the whole unsigned range,
thus at least twice the unsigned width and at least the width of the signed
operand. The server reaches `Int128` and `Int256` only when the pair already
holds a wide operand:

| pair | result | why |
|---|---|---|
| `UInt128` with a signed peer | `Int256` | a wide operand is present |
| `UInt64` with a signed peer | refused | nothing puts `Int128` in reach |
| `UInt256` with a signed peer | refused | `Int512` does not exist |
| `Int128` with `UInt64` | `Int128` | the signed side already holds the range |

The refusal for `UInt64` is correct and must survive. Removing it makes
`commonCHType(Int8, UInt64)` answer `Int128`, which the server refuses.

## A wide integer with a float

The operator decides:

| operator | result |
|---|---|
| `+` `-` `*` | refused by the server, code 43, ILLEGAL_TYPE_OF_ARGUMENT |
| `/` `%` | `Float64` |

A 64 bit integer with a float gives `Float64` for every operator, and a
Decimal with a float gives `Float64` as well. Only the 128 bit and 256 bit
integers carry the split.

## Negation

The whole wide family negates. An unsigned wide operand gives the signed
type of the same width: `-UInt128` is `Int128` and `-UInt256` is `Int256`.
A sized Decimal keeps its own type, thus the switch needs every spelling
(`Decimal32`, `Decimal64`, `Decimal128`, `Decimal256`), not the bare
`Decimal` alias.

`Decimal32(4)` and `Decimal(9, 4)` map to the same Go type,
`decimal.Decimal`, thus the spelling that chgen keeps is unobservable in the
generated code. The type oracle compares the names as text and reports the
pair as a mismatch. That report is cosmetic.

## The gate

`numericCHType` guards the numeric branch of `commonCHType`. A type that it
does not accept never reaches the rules above, so the wide names belong in
that function as well. Adding the rules without the gate leaves them
unreachable, and the tests stay red with no sign of why.
