package engine

import "testing"

// These tests pin the result type of the wide and the sized type
// constructors, together with the *OrNull and the *OrZero variants of the
// cast family.
//
// Each expected type was measured on ClickHouse 25.8.29.51 (image
// clickhouse/clickhouse-server:25.8, disposable container) with
// DESCRIBE (SELECT <expression> FROM t), always over real table columns
// and never over literals only, because ClickHouse folds constants and a
// folded constant reports a different LowCardinality wrapper.
//
// The measured rules:
//
//   - The wide integer constructors give their own fixed type.
//   - toFixedString(x, N) gives FixedString(N). N comes from the second
//     argument, which must be a constant.
//   - A Decimal constructor takes its precision from its name (Decimal32
//     is 9 digits, Decimal64 18, Decimal128 38 and Decimal256 76) and its
//     scale from the constant second argument. The type of the first
//     argument does not change the result.
//   - LowCardinality survives a constructor of this family only when the
//     RESULT type family can go inside LowCardinality. Decimal and
//     DateTime64 cannot, thus they drop the wrapper. See
//     TestLowCardinalityResultTypeAcceptance for the measured table.
//   - *OrNull adds Nullable to the base result.
//   - *OrZero does NOT remove Nullable. A Nullable argument still gives a
//     Nullable result, thus *OrZero moves the wrappers like the plain
//     constructor does.
func TestWideConstructorType(t *testing.T) {
	schema := wrapperTestSchema(t)
	cases := []struct{ expr, want string }{
		{"toInt128(i32)", "Int128"},
		{"toInt256(i32)", "Int256"},
		{"toUInt128(u32)", "UInt128"},
		{"toUInt256(u32)", "UInt256"},
		// The wrappers move like a plain cast.
		{"toInt128(ni32)", "Nullable(Int128)"},
		{"toInt128(lc)", "LowCardinality(Int128)"},
		{"toUInt256(lcn)", "LowCardinality(Nullable(UInt256))"},
	}
	for _, testCase := range cases {
		if got := inferCHTypeString(t, schema, testCase.expr); got != testCase.want {
			t.Errorf("type of %q = %s, want %s", testCase.expr, got, testCase.want)
		}
	}
}

func TestFixedStringConstructorType(t *testing.T) {
	schema := wrapperTestSchema(t)
	cases := []struct{ expr, want string }{
		{"toFixedString(s, 5)", "FixedString(5)"},
		{"toFixedString(fs, 10)", "FixedString(10)"},
		{"toFixedString(ns, 5)", "Nullable(FixedString(5))"},
		{"toFixedString(lc, 5)", "LowCardinality(FixedString(5))"},
	}
	for _, testCase := range cases {
		if got := inferCHTypeString(t, schema, testCase.expr); got != testCase.want {
			t.Errorf("type of %q = %s, want %s", testCase.expr, got, testCase.want)
		}
	}
}

func TestDecimalConstructorType(t *testing.T) {
	schema := wrapperTestSchema(t)
	cases := []struct{ expr, want string }{
		// The precision comes from the name, the scale from the
		// constant second argument.
		{"toDecimal32(f64, 2)", "Decimal(9, 2)"},
		{"toDecimal32(f64, 0)", "Decimal(9, 0)"},
		{"toDecimal32(f64, 9)", "Decimal(9, 9)"},
		{"toDecimal64(f64, 2)", "Decimal(18, 2)"},
		{"toDecimal64(f64, 18)", "Decimal(18, 18)"},
		{"toDecimal128(f64, 10)", "Decimal(38, 10)"},
		{"toDecimal256(f64, 5)", "Decimal(76, 5)"},
		// The scale of a Decimal argument does not reach the result.
		{"toDecimal64(d32, 2)", "Decimal(18, 2)"},
		{"toDecimal64(d128, 7)", "Decimal(18, 7)"},
		// Nullable moves through, LowCardinality does not, because
		// ClickHouse has no LowCardinality(Decimal).
		{"toDecimal64(ni32, 3)", "Nullable(Decimal(18, 3))"},
		{"toDecimal64(lc, 2)", "Decimal(18, 2)"},
		{"toDecimal32(lcn, 2)", "Nullable(Decimal(9, 2))"},
	}
	for _, testCase := range cases {
		if got := inferCHTypeString(t, schema, testCase.expr); got != testCase.want {
			t.Errorf("type of %q = %s, want %s", testCase.expr, got, testCase.want)
		}
	}
}

func TestOrNullAndOrZeroConstructorType(t *testing.T) {
	schema := wrapperTestSchema(t)
	cases := []struct{ expr, want string }{
		// *OrNull adds Nullable, *OrZero does not.
		{"toInt32OrNull(s)", "Nullable(Int32)"},
		{"toInt32OrZero(s)", "Int32"},
		{"toFloat64OrNull(s)", "Nullable(Float64)"},
		{"toFloat64OrZero(s)", "Float64"},
		{"toInt128OrNull(s)", "Nullable(Int128)"},
		{"toUInt256OrZero(s)", "UInt256"},
		{"toDateOrNull(s)", "Nullable(Date)"},
		{"toDateTimeOrZero(s)", "DateTime"},
		{"toDate32OrNull(s)", "Nullable(Date32)"},
		{"toDate32OrZero(s)", "Date32"},
		{"toUUIDOrNull(s)", "Nullable(UUID)"},
		{"toUUIDOrZero(s)", "UUID"},
		{"toDecimal64OrNull(s, 3)", "Nullable(Decimal(18, 3))"},
		{"toDecimal64OrZero(s, 3)", "Decimal(18, 3)"},
		{"toDateTime64OrNull(s, 6)", "Nullable(DateTime64(6))"},
		{"toDateTime64OrNull(s, 6, 'UTC')", "Nullable(DateTime64(6, 'UTC'))"},
		{"toDateTime64OrZero(s, 6)", "DateTime64(6)"},
		// A Nullable argument stays Nullable through *OrZero: the
		// suffix does not remove the wrapper.
		{"toInt32OrNull(ns)", "Nullable(Int32)"},
		{"toInt32OrZero(ns)", "Nullable(Int32)"},
		{"toDecimal64OrZero(ns, 3)", "Nullable(Decimal(18, 3))"},
		{"toDecimal64OrNull(ns, 3)", "Nullable(Decimal(18, 3))"},
		// LowCardinality survives, and Nullable sits inside it.
		{"toInt32OrNull(lc)", "LowCardinality(Nullable(Int32))"},
		{"toInt32OrZero(lc)", "LowCardinality(Int32)"},
		{"toInt32OrZero(lcn)", "LowCardinality(Nullable(Int32))"},
		// A Decimal result drops LowCardinality.
		{"toDecimal32OrNull(lc, 2)", "Nullable(Decimal(9, 2))"},
	}
	for _, testCase := range cases {
		if got := inferCHTypeString(t, schema, testCase.expr); got != testCase.want {
			t.Errorf("type of %q = %s, want %s", testCase.expr, got, testCase.want)
		}
	}
}

// TestSizedConstructorAppliesItsDomain pins the measured argument domain
// of this family.
//
// inferSizedConstructorType answers BEFORE the generic rule lookup, thus
// the generic domain check never runs for these names and this route has
// to apply the domain itself. Without that check the family gives a type
// to a call that the server refuses, which is the silent wrong answer
// that this project must not produce.
//
// Every case below was measured by EXECUTION on ClickHouse 25.8.29.51,
// not by toTypeName: toTypeName reports a type for calls that the server
// then refuses while it runs them, thus a domain read from the analysis
// alone would be far too wide.
//
// The refusals name only the codes that name a TYPE: 43
// ILLEGAL_TYPE_OF_ARGUMENT, 44 ILLEGAL_COLUMN and 48 NOT_IMPLEMENTED. A
// Code 6 CANNOT_PARSE_TEXT is about the VALUE in the row and is
// deliberately NOT a refusal here: toInt128(s) is Code: 6 when the column
// holds 'abc' and it is Int128 when the same column holds '12', thus
// String is inside the domain of toInt128.
func TestSizedConstructorAppliesItsDomain(t *testing.T) {
	schema := wrapperTestSchema(t)

	// Accepted. A String reaches the number-taking constructors,
	// because they parse the text.
	for _, testCase := range []struct{ expr, want string }{
		{"toInt128(s)", "Int128"},
		{"toDecimal64(s, 2)", "Decimal(18, 2)"},
		{"toInt128(i32)", "Int128"},
		{"toInt32OrZero(s)", "Int32"},
	} {
		if got := inferCHTypeString(t, schema, testCase.expr); got != testCase.want {
			t.Errorf("type of %q = %s, want %s", testCase.expr, got, testCase.want)
		}
	}

	// Refused. Each of these is a measured Code 43, 44 or 48.
	for _, expr := range []string{
		// The OrZero and the OrNull family parses a text and refuses
		// every number (Code: 43).
		"toInt32OrZero(i32)",
		"toInt32OrNull(f64)",
		"toDateOrZero(d)",
		"toUUIDOrZero(uu)",
		"toDecimal64OrZero(f64, 2)",
		// The Decimal constructors refuse the temporal types
		// (Code: 44).
		"toDecimal64(d, 2)",
		"toDecimal64(dt, 2)",
		// The wide integers refuse the containers (Code: 43) and the
		// UUID and the IP types (Code: 48).
		"toInt128(arr_i)",
		"toInt128(m)",
		"toInt128(uu)",
		// toFixedString takes a text only (Code: 48).
		"toFixedString(i32, 5)",
	} {
		inferCHTypeError(t, schema, expr)
	}
}

// A non-constant scale is not typeable, because ClickHouse itself refuses
// it. A missing or non-constant sizing argument must stay a refusal, never
// a guess.
func TestSizedConstructorRefusesNonConstantSize(t *testing.T) {
	schema := wrapperTestSchema(t)
	for _, expr := range []string{
		"toDecimal64(f64, u8)",
		"toFixedString(s, u8)",
		"toDecimal64(f64)",
		"toFixedString(s)",
	} {
		inferCHTypeError(t, schema, expr)
	}
}

// ClickHouse has no LowCardinality(Decimal), thus an arithmetic result
// that is a Decimal drops the LowCardinality wrapper, while the same
// arithmetic over an integer keeps it. Measured on ClickHouse 25.8.29.51
// with real columns:
//
//	1 + length(lower(lc))                  -> LowCardinality(UInt64)
//	toDecimal32(u8, 2) + length(lower(lc)) -> Decimal(9, 2)
//	length(lower(lc)) + toDecimal64(u8, 3) -> Decimal(18, 3)
func TestDecimalArithmeticDropsLowCardinality(t *testing.T) {
	schema := wrapperTestSchema(t)
	cases := []struct{ expr, want string }{
		{"1 + length(lower(lc))", "LowCardinality(UInt64)"},
		{"toDecimal32(u8, 2) + length(lower(lc))", "Decimal(9, 2)"},
		{"toDecimal32(u8, 2) - length(lower(lc))", "Decimal(9, 2)"},
		{"toDecimal32(u8, 2) * length(lower(lc))", "Decimal(9, 2)"},
		{"toDecimal32(u8, 2) / length(lower(lc))", "Decimal(9, 2)"},
		{"length(lower(lc)) + toDecimal64(u8, 3)", "Decimal(18, 3)"},
		// A constant Decimal operand is the shape that the constant
		// rule would otherwise keep LowCardinality for.
		{"toDecimal32(toUInt8(255), 2) / length(lower(lc))", "Decimal(9, 2)"},
		{"toDecimal32(toUInt8(255), 2) + length(lower(lc))", "Decimal(9, 2)"},
		{"length(lower(lc)) * toDecimal64(toUInt8(255), 3)", "Decimal(18, 3)"},
	}
	for _, testCase := range cases {
		if got := inferCHTypeString(t, schema, testCase.expr); got != testCase.want {
			t.Errorf("type of %q = %s, want %s", testCase.expr, got, testCase.want)
		}
	}
}

// TestLowCardinalityResultTypeAcceptance pins the measured rule that
// decides whether a constructor keeps or drops the LowCardinality
// wrapper of its argument.
//
// The rule is about the RESULT type family, not about Decimal alone. It
// was measured on the probe ClickHouse server in two ways that agree:
//
//   - CREATE TABLE t (c LowCardinality(T)) with
//     allow_suspicious_low_cardinality_types=1. Decimal of every width,
//     DateTime64 of every precision, Enum8, Enum16, Array, Tuple and Map
//     give Code: 43. Every Int and UInt width, Float32, Float64,
//     BFloat16, String, FixedString, Date, Date32, DateTime, UUID, IPv4,
//     IPv6, Bool and Nullable of each are accepted.
//   - toTypeName of the expression over a LowCardinality(String) column,
//     and the same query executed, which agree with the list above.
//
// The setting must be on for this oracle. With the setting off a Date or
// a numeric column gives Code: 455, which is a policy guard against a
// wasteful column and not a statement that the type cannot exist. An
// expression result is not subject to that guard, thus Code 455 is not
// evidence and only Code 43 is.
//
// The server message "DataTypeLowCardinality is supported only for
// numbers, strings, Date or DateTime" is not correct, because UUID, IPv4,
// IPv6 and Bool are accepted as well. Thus this test pins the measured
// table and not the message.
func TestLowCardinalityResultTypeAcceptance(t *testing.T) {
	schema := wrapperTestSchema(t)
	cases := []struct{ expr, want string }{
		// Result families that REJECT LowCardinality: the wrapper
		// drops. DateTime64 is the case that this test was added
		// for; before the fix these gave LowCardinality(DateTime64).
		{"toDateTime64OrZero(lc, 1)", "DateTime64(1)"},
		{"toDateTime64OrZero(lc, 3)", "DateTime64(3)"},
		{"toDateTime64OrNull(lc, 3)", "Nullable(DateTime64(3))"},
		{"toDateTime64OrZero(lcn, 3)", "Nullable(DateTime64(3))"},
		// Decimal was the one family that the old rule knew.
		{"toDecimal32(lc, 2)", "Decimal(9, 2)"},
		{"toDecimal64OrZero(lc, 2)", "Decimal(18, 2)"},
		{"toDecimal128OrNull(lc, 2)", "Nullable(Decimal(38, 2))"},

		// Result families that ACCEPT LowCardinality: the wrapper
		// stays. These pin the other side of the table, so that a
		// wider rule cannot drop the wrapper where the server keeps
		// it.
		{"toDate(lc)", "LowCardinality(Date)"},
		{"toDateOrZero(lc)", "LowCardinality(Date)"},
		{"toDateOrNull(lc)", "LowCardinality(Nullable(Date))"},
		{"toDateOrZero(lcn)", "LowCardinality(Nullable(Date))"},
		{"toDate32OrZero(lc)", "LowCardinality(Date32)"},
		{"toDateTimeOrZero(lc)", "LowCardinality(DateTime)"},
		{"toDateTimeOrNull(lc)", "LowCardinality(Nullable(DateTime))"},
		{"toUUIDOrZero(lc)", "LowCardinality(UUID)"},
		{"toUUIDOrNull(lc)", "LowCardinality(Nullable(UUID))"},
		{"toIPv4OrZero(lc)", "LowCardinality(IPv4)"},
		{"toIPv6OrZero(lc)", "LowCardinality(IPv6)"},
		{"toInt128OrZero(lc)", "LowCardinality(Int128)"},
		{"toFloat64OrZero(lc)", "LowCardinality(Float64)"},
		{"toFixedString(lc, 5)", "LowCardinality(FixedString(5))"},
	}
	for _, testCase := range cases {
		if got := inferCHTypeString(t, schema, testCase.expr); got != testCase.want {
			t.Errorf("type of %q = %s, want %s", testCase.expr, got, testCase.want)
		}
	}
}
