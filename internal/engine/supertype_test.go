package engine

import (
	"testing"
)

// These tests pin the common-supertype model and the Decimal arithmetic
// rules. Each expected type was measured on ClickHouse 25.8.29.51 (image
// clickhouse/clickhouse-server:25.8, disposable container) with
// SELECT toTypeName(<expression>) FROM t on real columns, not on
// constant expressions.
func supertypeTestSchema(t *testing.T) *Schema {
	t.Helper()
	schema, err := schemaFromDDLErr(t, `CREATE TABLE t (
    i8   Int8, i16 Int16, i32 Int32, i64 Int64,
    u8   UInt8, u16 UInt16, u32 UInt32, u64 UInt64,
    f32  Float32, f64 Float64,
    dec  Decimal(18, 4), dec2 Decimal(10, 2), b Bool,
    s    String, fs FixedString(8),
    d    Date, dt DateTime, dt64 DateTime64(3),
    ni32 Nullable(Int32), nf64 Nullable(Float64), ns Nullable(String),
    ndec Nullable(Decimal(18, 4)),
    arr_i Array(Int32), arr_s Array(String),
    m    Map(String, Int64),
    lc   LowCardinality(String), lcn LowCardinality(Nullable(String))
) ENGINE = MergeTree ORDER BY tuple();`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	return schema
}

func TestLikeOperatorType(t *testing.T) {
	schema := supertypeTestSchema(t)
	cases := []struct{ expr, want string }{
		{"s LIKE '%a%'", "UInt8"},
		{"fs LIKE '%a%'", "UInt8"},
		{"s NOT LIKE '%a%'", "UInt8"},
		{"s ILIKE '%a%'", "UInt8"},
		{"ns LIKE '%a%'", "Nullable(UInt8)"},
		{"ns NOT LIKE s", "Nullable(UInt8)"},
		{"lc LIKE '%a%'", "LowCardinality(UInt8)"},
		{"lc LIKE s", "UInt8"},
		{"lcn ILIKE '%a%'", "LowCardinality(Nullable(UInt8))"},
		// NOT over a LIKE result keeps the wrappers.
		{"NOT (lcn ILIKE '%a%')", "LowCardinality(Nullable(UInt8))"},
	}
	for _, testCase := range cases {
		if got := inferCHTypeString(t, schema, testCase.expr); got != testCase.want {
			t.Errorf("type of %q = %s, want %s", testCase.expr, got, testCase.want)
		}
	}
}

func TestDecimalArithmeticType(t *testing.T) {
	schema := supertypeTestSchema(t)
	cases := []struct{ expr, want string }{
		// Decimal with an integer keeps the Decimal type unchanged,
		// for every operator and on both sides.
		{"dec + u8", "Decimal(18, 4)"},
		{"dec + u64", "Decimal(18, 4)"},
		{"dec - u32", "Decimal(18, 4)"},
		{"u32 - dec", "Decimal(18, 4)"},
		{"dec * i32", "Decimal(18, 4)"},
		{"dec / u64", "Decimal(18, 4)"},
		{"i32 / dec", "Decimal(18, 4)"},
		{"dec % u16", "Decimal(18, 4)"},
		{"u32 % dec", "Decimal(18, 4)"},
		{"dec2 + i64", "Decimal(10, 2)"},
		{"dec2 % u64", "Decimal(10, 2)"},
		{"dec % b", "Decimal(18, 4)"},
		{"dec + 10000000000", "Decimal(18, 4)"},
		// Decimal with a float gives Float64 on both sides.
		{"dec + f64", "Float64"},
		{"dec % f32", "Float64"},
		{"f64 / dec", "Float64"},
		{"dec * 1.5", "Float64"},
		// Decimal with Decimal takes the full precision of the widest
		// storage class. The scale depends on the operator.
		{"dec + dec", "Decimal(18, 4)"},
		{"dec * dec", "Decimal(18, 8)"},
		{"dec / dec", "Decimal(18, 4)"},
		{"dec + dec2", "Decimal(18, 4)"},
		{"dec2 + dec2", "Decimal(18, 2)"},
		{"dec2 * dec", "Decimal(18, 6)"},
		{"dec2 / dec", "Decimal(18, 2)"},
		{"dec2 % dec", "Decimal(18, 2)"},
		{"dec % dec2", "Decimal(18, 4)"},
		// The Nullable wrapper of an operand stays on the result.
		{"ndec + u8", "Nullable(Decimal(18, 4))"},
		{"ndec * f64", "Nullable(Float64)"},
		{"nullIf(dec, u32) % u16", "Nullable(Decimal(18, 4))"},
	}
	for _, testCase := range cases {
		if got := inferCHTypeString(t, schema, testCase.expr); got != testCase.want {
			t.Errorf("type of %q = %s, want %s", testCase.expr, got, testCase.want)
		}
	}
}

func TestConditionalSupertype(t *testing.T) {
	schema := supertypeTestSchema(t)
	cases := []struct{ expr, want string }{
		// Bool acts as UInt8 in the lattice; a UInt8 peer keeps Bool.
		{"if(b, b, u8)", "Bool"},
		{"if(b, b, u16)", "UInt16"},
		{"if(b, b, i32)", "Int32"},
		{"if(b, b, f64)", "Float64"},
		{"if(b, i16, b)", "Int16"},
		{"multiIf(b, i16, b, b, ni32)", "Nullable(Int32)"},
		// Decimal against an integer widens the precision to the
		// storage class that holds the integer.
		{"if(b, dec, u16)", "Decimal(18, 4)"},
		{"if(b, dec, i32)", "Decimal(18, 4)"},
		{"if(b, dec, i64)", "Decimal(38, 4)"},
		{"if(b, dec, u64)", "Decimal(38, 4)"},
		{"if(b, dec2, u8)", "Decimal(18, 2)"},
		{"if(b, dec2, i64)", "Decimal(38, 2)"},
		{"if(b, dec, dec2)", "Decimal(18, 4)"},
		{"if(b, dec, b)", "Decimal(18, 4)"},
		{"if(b, dec, ni32)", "Nullable(Decimal(18, 4))"},
		{"if(b, dec, 10000000000)", "Decimal(38, 4)"},
		// String and FixedString join as String.
		{"if(b, s, fs)", "String"},
		{"if(b, fs, s)", "String"},
		// A big unsigned integer literal narrows to Int64 when a
		// signed peer needs it and the value fits.
		{"if(b, i8, 10000000000)", "Int64"},
		{"if(b, i64, 9223372036854775807)", "Int64"},
		{"multiIf(b, i16, b, b, u8)", "Int16"},
		// The condition wrappers do not reach the result.
		{"if(ni32 = 1, u8, u16)", "UInt16"},
	}
	for _, testCase := range cases {
		if got := inferCHTypeString(t, schema, testCase.expr); got != testCase.want {
			t.Errorf("type of %q = %s, want %s", testCase.expr, got, testCase.want)
		}
	}
}

func TestConditionalSupertypeRefusals(t *testing.T) {
	schema := supertypeTestSchema(t)
	// ClickHouse itself rejects these combinations (NO_COMMON_TYPE),
	// so chgen must refuse instead of guessing a branch type.
	cases := []string{
		"if(b, i64, u64)",
		"if(b, i32, u64)",
		"if(b, dec, f64)",
		"if(b, u64, -1)",
	}
	for _, expr := range cases {
		if err := inferCHTypeError(t, schema, expr); err == nil {
			t.Errorf("inferExprType(%q) did not refuse", expr)
		}
	}
}

func TestCoalesceIfNullSupertype(t *testing.T) {
	schema := supertypeTestSchema(t)
	cases := []struct{ expr, want string }{
		// coalesce takes the common supertype. The result is Nullable
		// only when every argument is Nullable.
		{"coalesce(nf64, 255)", "Float64"},
		{"coalesce(nullIf(nf64, 255), 255)", "Float64"},
		{"coalesce(ni32, nf64)", "Nullable(Float64)"},
		{"coalesce(ni32)", "Nullable(Int32)"},
		{"coalesce(ni32, ni32)", "Nullable(Int32)"},
		{"coalesce(nullIf(dec, u32), i16)", "Decimal(18, 4)"},
		// ifNull is Nullable only when the second argument is.
		{"ifNull(ni32, u8)", "Int32"},
		{"ifNull(i32, u8)", "Int32"},
		{"ifNull(ni32, nf64)", "Nullable(Float64)"},
		{"ifNull(ni32, ni32)", "Nullable(Int32)"},
		{"ifNull(nullIf(dec2, u32), u64)", "Decimal(38, 2)"},
	}
	for _, testCase := range cases {
		if got := inferCHTypeString(t, schema, testCase.expr); got != testCase.want {
			t.Errorf("type of %q = %s, want %s", testCase.expr, got, testCase.want)
		}
	}
}

// TestHarmfulNullableFindings pins the 14 run-3 findings where chgen
// reported a non-Nullable type and ClickHouse reported a Nullable type.
// A non-pointer Go field for such a column fails at scan time on NULL
// data. The expected types follow the ClickHouse answer; a predicate
// result is Nullable(UInt8), while an AND/OR result over a Bool operand
// stays Nullable(Bool).
func TestHarmfulNullableFindings(t *testing.T) {
	schema := supertypeTestSchema(t)
	cases := []struct{ expr, want string }{
		{"(NOT (lcn ILIKE '%a%')) OR true", "Nullable(Bool)"},
		{"(NOT (lcn NOT LIKE '%a%')) OR (CAST(f64 AS Int32) <= length(lc))", "Nullable(UInt8)"},
		{"CAST(1.7976931348623157e308 AS Int32) < multiIf(b, dec, (ns NOT LIKE '%a%'), -(ni32), m['k'])", "Nullable(UInt8)"},
		{"argMax(d, (nullIf(dec, u32) % u16))", "Nullable(Date)"},
		{"argMax(toString(arr_i[1]), multiIf(has(arr_i, 1), (i8 * i8), (NOT true), multiIf(b, ni32, b, 256, dec), CAST(u8 AS Int8)))", "Nullable(String)"},
		{"avg(if((lc <= fs), (dec * nf64), i16)) * uniq(substring((ns || '%a%'), 1, 2))", "Nullable(Float64)"},
		{"i16 > nullIf((u8 + dec), u64)", "Nullable(UInt8)"},
		{"toInt8(((dec / 9223372036854775807) % nf64))", "Nullable(Int8)"},
		{"toInt8(if((lc != lcn), nullIf(ni32, 4000000000), (f64 / dec)))", "Nullable(Int8)"},
		{"toString((ni32 + (1e10 % dec)))", "Nullable(String)"},
		{"toString(multiIf((false OR true), (dec / dec), true, if(true, ni32, i8), m['k']))", "Nullable(String)"},
		{"toString(nullIf(multiIf(b, i64, b, 9223372036854775807, u8), 256))", "Nullable(String)"},
		{"toUInt64((nullIf(i16, -1) - if(true, i8, 10000000000)))", "Nullable(UInt64)"},
	}
	for _, testCase := range cases {
		if got := inferCHTypeString(t, schema, testCase.expr); got != testCase.want {
			t.Errorf("type of %q = %s, want %s", testCase.expr, got, testCase.want)
		}
	}
}

// The fourteenth finding mixes Int64 with a large integer literal. This
// test asserted a refusal while chgen still pinned such a literal to
// UInt64. That expectation was wrong: the literal narrows to Int64,
// exactly as it does in the simple branch pair, so the server both types
// AND executes this expression. Measured on ClickHouse 25.8.29.51 against
// the real columns:
//
//	toTypeName(multiIf((f64 != i32), (i8 % 70000), b, 10000000000,
//	                   nullIf(10000000000, i8)))         -> Nullable(Int64)
//	toTypeName(avg(...same...))                          -> Nullable(Float64)
//	SELECT avg(...same...) FROM t                        -> 3
//
// The branch types are Int64, UInt64 and Nullable(UInt64). Both UInt64
// values come from the same narrowable literal, so the join is Int64, not
// a refusal.
func TestHarmfulNullableFindingNarrowsTheLiteral(t *testing.T) {
	schema := supertypeTestSchema(t)
	cases := []struct{ expr, want string }{
		{"multiIf((f64 != i32), (i8 % 70000), b, 10000000000, nullIf(10000000000, i8))", "Nullable(Int64)"},
		{"avg(multiIf((f64 != i32), (i8 % 70000), b, 10000000000, nullIf(10000000000, i8)))", "Nullable(Float64)"},
	}
	for _, testCase := range cases {
		if got := inferCHTypeString(t, schema, testCase.expr); got != testCase.want {
			t.Errorf("type of %q = %s, want %s", testCase.expr, got, testCase.want)
		}
	}
}

// TestFloatIntegerSupertype pins the measured float rules of the
// supertype lattice. ClickHouse keeps Float32 for integers that fit its
// mantissa, widens to Float64 for 32-bit integers, and refuses a 64-bit
// integer for both float widths (measured on ClickHouse 25.8.29.51).
func TestFloatIntegerSupertype(t *testing.T) {
	schema := supertypeTestSchema(t)
	cases := []struct{ expr, want string }{
		{"if(b, f32, i8)", "Float32"},
		{"if(b, f32, i16)", "Float32"},
		{"if(b, f32, u16)", "Float32"},
		{"if(b, f32, b)", "Float32"},
		{"if(b, f32, i32)", "Float64"},
		{"if(b, f32, u32)", "Float64"},
		{"if(b, f32, f64)", "Float64"},
		{"multiIf(b, f32, b, -129, i16)", "Float32"},
	}
	for _, testCase := range cases {
		if got := inferCHTypeString(t, schema, testCase.expr); got != testCase.want {
			t.Errorf("type of %q = %s, want %s", testCase.expr, got, testCase.want)
		}
	}
	refusals := []string{
		"if(b, f32, i64)",
		"if(b, f64, i64)",
		"if(b, f64, u64)",
	}
	for _, expr := range refusals {
		if err := inferCHTypeError(t, schema, expr); err == nil {
			t.Errorf("inferExprType(%q) did not refuse", expr)
		}
	}
}

// TestDecimalSupertypeIsNotPairwise pins the n-ary rule: ClickHouse
// computes the supertype over all branches at once, so the integer
// branches join the Decimal directly and never widen each other first
// (measured on ClickHouse 25.8.29.51).
func TestDecimalSupertypeIsNotPairwise(t *testing.T) {
	schema := supertypeTestSchema(t)
	cases := []struct{ expr, want string }{
		{"multiIf(b, i32, false, 70000, dec)", "Decimal(18, 4)"},
		{"multiIf(b, i32, b, u32, dec)", "Decimal(18, 4)"},
		{"multiIf(b, i64, b, u32, dec)", "Decimal(38, 4)"},
	}
	for _, testCase := range cases {
		if got := inferCHTypeString(t, schema, testCase.expr); got != testCase.want {
			t.Errorf("type of %q = %s, want %s", testCase.expr, got, testCase.want)
		}
	}
}
