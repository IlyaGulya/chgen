package engine

import (
	"strings"
	"testing"
)

// These tests pin the ClickHouse rules for the wide integer types
// Int128, UInt128, Int256 and UInt256, for the binary arithmetic
// operators and for the common supertype that if() uses.
//
// Every expected value was measured on ClickHouse 26.7.3.19 (image
// clickhouse/clickhouse-server:latest, disposable container on port
// 18140) with real table columns, never with constant literals, because
// ClickHouse folds constants and a rule read from a literal lies. The
// type was read with DESCRIBE (SELECT <expr> AS x FROM t), and each
// accepted case was also executed on a real row to prove that the call
// runs and does not only type-check.
//
// The measurement used use_variant_as_common_type=0. ClickHouse 26.x
// turns that setting on by default, which makes if() return
// Variant(A, B) instead of refusing. chgen models the plain supertype
// lattice, which is what the setting=0 answer gives, and which also
// matches the ClickHouse 25.8 default that the other chgen supertype
// tests were measured against.
func wideIntegerTestSchema(t *testing.T) *Schema {
	t.Helper()
	schema, err := schemaFromDDLErr(t, `CREATE TABLE t (
    i8 Int8, i16 Int16, i32 Int32, i64 Int64, i128 Int128, i256 Int256,
    u8 UInt8, u16 UInt16, u32 UInt32, u64 UInt64, u128 UInt128, u256 UInt256,
    f32 Float32, f64 Float64
) ENGINE = MergeTree ORDER BY tuple();`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	return schema
}

func wideIntegerType(name string) CHType { return CHType{Name: name} }

// TestWideIntegerArithmeticResultType pins the arithmetic result type
// for the wide integer types. The measured rule is the same rule that
// chgen already applies to the 8..64-bit types, extended with the 128-
// and 256-bit sizes: the operand size doubles below 8 bytes and stays
// put at 8 bytes and above. All 720 integer-integer cells of the
// measured matrix follow it with no exception.
func TestWideIntegerArithmeticResultType(t *testing.T) {
	cases := []struct{ op, left, right, want string }{
		// "+" and "*": signed when either operand is signed.
		{"+", "Int64", "Int128", "Int128"},
		{"+", "Int8", "UInt128", "Int128"},
		{"+", "UInt64", "UInt128", "UInt128"},
		{"+", "UInt256", "Int64", "Int256"},
		{"+", "UInt256", "UInt64", "UInt256"},
		{"+", "Int128", "Int256", "Int256"},
		{"*", "UInt128", "UInt64", "UInt128"},
		{"*", "UInt64", "Int256", "Int256"},
		{"*", "Int128", "UInt128", "Int128"},

		// "-" is always signed.
		{"-", "UInt128", "UInt128", "Int128"},
		{"-", "UInt256", "UInt64", "Int256"},
		{"-", "UInt128", "UInt8", "Int128"},
		{"-", "Int256", "UInt128", "Int256"},

		// "/" is always Float64, including for the wide types.
		{"/", "Int256", "UInt64", "Float64"},
		{"/", "UInt128", "Float64", "Float64"},
		{"/", "Int128", "Int128", "Float64"},

		// "%" takes the sign from the dividend and the size from the
		// divisor. A signed dividend widens the divisor size, an
		// unsigned dividend keeps it.
		{"%", "UInt128", "Int64", "UInt64"},
		{"%", "UInt256", "UInt32", "UInt32"},
		{"%", "Int128", "Int32", "Int64"},
		{"%", "Int256", "UInt8", "Int16"},
		{"%", "UInt64", "UInt128", "UInt128"},
		{"%", "Int64", "UInt128", "Int128"},
		{"%", "UInt256", "Float64", "Float64"},
	}
	for _, testCase := range cases {
		got, err := inferArithmeticResultType(testCase.op,
			wideIntegerType(testCase.left), wideIntegerType(testCase.right))
		if err != nil {
			t.Errorf("inferArithmeticResultType(%q, %s, %s) error = %v, want %s",
				testCase.op, testCase.left, testCase.right, err, testCase.want)
			continue
		}
		if got.String() != testCase.want {
			t.Errorf("inferArithmeticResultType(%q, %s, %s) = %s, want %s",
				testCase.op, testCase.left, testCase.right, got.String(), testCase.want)
		}
	}
}

// TestWideIntegerArithmeticWithFloatRefused pins the refusals that the
// server also makes. ClickHouse rejects "+", "-" and "*" between a wide
// integer and a float with code 43 (ILLEGAL_TYPE_OF_ARGUMENT), thus a
// chgen refusal for these is correct and must stay.
//
// "/" and "%" are NOT in this list: the server accepts both and returns
// Float64, which TestWideIntegerArithmeticResultType pins.
func TestWideIntegerArithmeticWithFloatRefused(t *testing.T) {
	wide := []string{"Int128", "UInt128", "Int256", "UInt256"}
	floats := []string{"Float32", "Float64"}
	for _, op := range []string{"+", "-", "*"} {
		for _, wideName := range wide {
			for _, floatName := range floats {
				for _, pair := range [][2]string{{wideName, floatName}, {floatName, wideName}} {
					got, err := inferArithmeticResultType(op,
						wideIntegerType(pair[0]), wideIntegerType(pair[1]))
					if err == nil {
						t.Errorf("inferArithmeticResultType(%q, %s, %s) = %s, want a refusal",
							op, pair[0], pair[1], got.String())
					}
				}
			}
		}
	}
}

// TestWideIntegerCommonType pins the common supertype that if() uses.
//
// The measured rule for a signed operand with an unsigned operand is:
// ClickHouse needs a signed type that represents every value of the
// unsigned operand, that is a signed type at least twice as wide. It
// looks for that type among the ordinary 8..64-bit widths, and among
// the 128- and 256-bit widths only when a 128- or 256-bit operand is
// already present in the pair. This is why UInt128 with a signed peer
// gives Int256, while UInt64 with a signed peer has no supertype: the
// pair holds no wide operand that would put Int128 in reach.
func TestWideIntegerCommonType(t *testing.T) {
	cases := []struct{ left, right, want string }{
		// Same sign: the wider operand wins.
		{"UInt64", "UInt256", "UInt256"},
		{"UInt128", "UInt64", "UInt128"},
		{"UInt256", "UInt64", "UInt256"},
		{"UInt8", "UInt128", "UInt128"},
		{"Int64", "Int128", "Int128"},
		{"Int128", "Int256", "Int256"},
		{"Int256", "Int64", "Int256"},

		// Mixed sign, resolvable: a wide operand is present and a
		// signed type twice the unsigned width is in reach.
		{"Int8", "UInt128", "Int256"},
		{"Int64", "UInt128", "Int256"},
		{"Int128", "UInt128", "Int256"},
		{"UInt128", "Int64", "Int256"},
		{"UInt128", "Int128", "Int256"},

		// Mixed sign where the signed operand is already wide enough
		// to hold the whole unsigned range.
		{"Int128", "UInt64", "Int128"},
		{"Int256", "UInt64", "Int256"},
		{"UInt64", "Int128", "Int128"},
		{"UInt64", "Int256", "Int256"},
		{"Int128", "UInt32", "Int128"},
		{"Int256", "UInt128", "Int256"},
		{"UInt32", "Int256", "Int256"},
	}
	for _, testCase := range cases {
		got, err := commonCHType(wideIntegerType(testCase.left), wideIntegerType(testCase.right))
		if err != nil {
			t.Errorf("commonCHType(%s, %s) error = %v, want %s",
				testCase.left, testCase.right, err, testCase.want)
			continue
		}
		if got.String() != testCase.want {
			t.Errorf("commonCHType(%s, %s) = %s, want %s",
				testCase.left, testCase.right, got.String(), testCase.want)
		}
	}
}

// TestWideIntegerCommonTypeRefused pins the supertype refusals that the
// server also makes, with code 386 (NO_COMMON_TYPE). These chgen
// refusals are correct and must survive the wide integer fix.
//
// UInt64 and UInt256 with a signed peer of the same width or narrower
// have no supertype, because no reachable signed type represents the
// whole unsigned range. Every wide integer with a float also has no
// supertype.
func TestWideIntegerCommonTypeRefused(t *testing.T) {
	pairs := [][2]string{
		// UInt64 with a signed peer that is not wider.
		{"Int8", "UInt64"}, {"Int16", "UInt64"}, {"Int32", "UInt64"}, {"Int64", "UInt64"},
		{"UInt64", "Int8"}, {"UInt64", "Int64"},
		// UInt256 with any signed peer: Int512 does not exist.
		{"Int8", "UInt256"}, {"Int64", "UInt256"}, {"Int128", "UInt256"}, {"Int256", "UInt256"},
		{"UInt256", "Int64"}, {"UInt256", "Int256"},
		// A wide integer with a float.
		{"Int128", "Float64"}, {"UInt128", "Float32"}, {"Int256", "Float64"},
		{"UInt256", "Float64"}, {"Float64", "Int128"}, {"Float32", "UInt256"},
		// A 64-bit integer with a float, already refused today.
		{"Int64", "Float64"}, {"Float64", "Int64"}, {"UInt64", "Float64"},
	}
	for _, pair := range pairs {
		got, err := commonCHType(wideIntegerType(pair[0]), wideIntegerType(pair[1]))
		if err == nil {
			t.Errorf("commonCHType(%s, %s) = %s, want a refusal", pair[0], pair[1], got.String())
			continue
		}
		if !strings.Contains(err.Error(), "no common ClickHouse type") {
			t.Errorf("commonCHType(%s, %s) error = %v, want a no-common-type refusal",
				pair[0], pair[1], err)
		}
	}
}

// TestWideIntegerExpressionTypesEndToEnd resolves the wide integer rules
// through a real query against a real schema, so the fix is pinned at
// the level a chgen user meets, not only at the helper level.
func TestWideIntegerExpressionTypesEndToEnd(t *testing.T) {
	schema := wideIntegerTestSchema(t)
	cases := []struct{ expr, want string }{
		{"i64 + i128", "Int128"},
		{"u256 + u64", "UInt256"},
		{"u128 - u8", "Int128"},
		{"i256 / u64", "Float64"},
		{"u128 % i64", "UInt64"},
		{"i128 % i32", "Int64"},
		{"if(i8 > 0, u64, u256)", "UInt256"},
		{"if(i8 > 0, i64, u128)", "Int256"},
		{"if(i8 > 0, i128, u64)", "Int128"},
	}
	for _, testCase := range cases {
		if got := inferCHTypeString(t, schema, testCase.expr); got != testCase.want {
			t.Errorf("type of %q = %s, want %s", testCase.expr, got, testCase.want)
		}
	}
}
