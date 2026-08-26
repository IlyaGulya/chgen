package engine

import "testing"

// arithmeticTypeForTest parses a ClickHouse type name for the arithmetic
// tests. The names below are all well formed, thus a parse error is a
// fault in the test itself.
func arithmeticTypeForTest(t *testing.T, name string) CHType {
	t.Helper()
	parsed, err := parseCHTypeName(name)
	if err != nil {
		t.Fatalf("parseCHTypeName(%q) error = %v", name, err)
	}
	return parsed
}

// TestSizedDecimalArithmetic pins the arithmetic rules for the sized
// Decimal spellings. Decimal32(S), Decimal64(S), Decimal128(S) and
// Decimal256(S) name the same types as Decimal(P, S), but chgen used to
// read a precision and a scale from the bare spelling only. Every sized
// Decimal therefore fell through to "cannot infer result type", although
// the server answers.
//
// The server reports a sized result in the canonical Decimal(P, S) form,
// thus the expectations below use that form. Measured on ClickHouse
// 25.8.29.51 with real columns for both operand orders.
func TestSizedDecimalArithmetic(t *testing.T) {
	cases := []struct {
		op    string
		left  string
		right string
		want  string
	}{
		// The three false refusals from the bug report.
		{"*", "Float64", "Decimal128(4)", "Float64"},
		{"+", "Int16", "Decimal128(4)", "Decimal(38, 4)"},
		{"+", "UInt32", "Decimal128(4)", "Decimal(38, 4)"},

		// A sized Decimal with an integer keeps the Decimal type and
		// takes the precision that the size fixes: Decimal32 holds 9
		// digits, Decimal64 18, Decimal128 38 and Decimal256 76.
		{"+", "Decimal32(4)", "Int8", "Decimal(9, 4)"},
		{"-", "Decimal64(4)", "UInt64", "Decimal(18, 4)"},
		{"*", "Decimal128(4)", "Int256", "Decimal(38, 4)"},
		{"/", "Decimal256(4)", "Int32", "Decimal(76, 4)"},
		{"%", "Decimal32(4)", "UInt16", "Decimal(9, 4)"},

		// The reversed operand order gives the same result.
		{"%", "Int64", "Decimal64(4)", "Decimal(18, 4)"},
		{"/", "UInt128", "Decimal256(4)", "Decimal(76, 4)"},

		// A sized Decimal with a float gives Float64 for every operator.
		{"+", "Decimal32(4)", "Float32", "Float64"},
		{"%", "Decimal256(4)", "Float64", "Float64"},
		{"-", "Float32", "Decimal64(4)", "Float64"},

		// Two sized Decimals take the full precision of the widest
		// storage class. The scale is max(s1, s2) for "+" and "-",
		// s1 + s2 for "*" and the left scale for "/" and "%".
		{"+", "Decimal32(4)", "Decimal128(4)", "Decimal(38, 4)"},
		{"*", "Decimal32(4)", "Decimal64(4)", "Decimal(18, 8)"},
		{"/", "Decimal128(4)", "Decimal32(4)", "Decimal(38, 4)"},

		// The bare spelling keeps working exactly as before.
		{"+", "Decimal(18, 4)", "Int16", "Decimal(18, 4)"},
	}
	for _, testCase := range cases {
		left := arithmeticTypeForTest(t, testCase.left)
		right := arithmeticTypeForTest(t, testCase.right)
		got, err := inferArithmeticResultType(testCase.op, left, right)
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

// TestSizedDecimalWithDateRefused pins the refusals that the server also
// makes. ClickHouse rejects every operator between a Decimal and a Date
// or a DateTime with code 43 (ILLEGAL_TYPE_OF_ARGUMENT), thus the chgen
// refusal is correct and must survive the sized-spelling fix. Measured on
// ClickHouse 25.8.29.51 with real columns.
func TestSizedDecimalWithDateRefused(t *testing.T) {
	for _, decimalName := range []string{
		"Decimal32(4)", "Decimal64(4)", "Decimal128(4)", "Decimal256(4)", "Decimal(18, 4)",
	} {
		for _, dateName := range []string{"Date", "DateTime"} {
			for _, op := range []string{"+", "-", "*", "/", "%"} {
				decimalType := arithmeticTypeForTest(t, decimalName)
				dateType := arithmeticTypeForTest(t, dateName)
				if got, err := inferArithmeticResultType(op, decimalType, dateType); err == nil {
					t.Errorf("inferArithmeticResultType(%q, %s, %s) = %s, want a refusal",
						op, decimalName, dateName, got.String())
				}
				if got, err := inferArithmeticResultType(op, dateType, decimalType); err == nil {
					t.Errorf("inferArithmeticResultType(%q, %s, %s) = %s, want a refusal",
						op, dateName, decimalName, got.String())
				}
			}
		}
	}
}

// TestSizedDecimalCommonSupertype pins the CASE and if() branch rules for
// the sized Decimal spellings. commonDecimalSupertype reads the precision
// and the scale through decimalPrecisionScale, thus the same missing
// normalization made a CASE over a sized Decimal report the misleading
// message "Decimal128(4) and Int64 are not numeric", although both
// operands are numeric.
//
// Measured on ClickHouse 25.8.29.51 with real columns:
// if(cond, Decimal128(4), Int64) is Decimal(38, 4) and
// if(cond, Decimal32(4), Decimal128(4)) is Decimal(38, 4).
func TestSizedDecimalCommonSupertype(t *testing.T) {
	cases := []struct {
		left  string
		right string
		want  string
	}{
		{"Decimal128(4)", "Int64", "Decimal(38, 4)"},
		{"Decimal32(4)", "Int16", "Decimal(9, 4)"},
		{"Decimal256(4)", "UInt32", "Decimal(76, 4)"},
		{"Decimal64(4)", "Decimal128(4)", "Decimal(38, 4)"},
		{"Decimal(18, 4)", "Int64", "Decimal(38, 4)"},
	}
	for _, testCase := range cases {
		left := arithmeticTypeForTest(t, testCase.left)
		right := arithmeticTypeForTest(t, testCase.right)
		got, err := commonCHType(left, right)
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

// TestSizedDecimalWithFloatBranchRefused pins a refusal that stays. A
// CASE or if() with a Decimal branch and a Float branch has no common
// type on ClickHouse either: the server answers code 386, NO_COMMON_TYPE.
// The sized spellings must not become an exception to that rule.
// Measured on ClickHouse 25.8.29.51 with real columns.
func TestSizedDecimalWithFloatBranchRefused(t *testing.T) {
	for _, decimalName := range []string{
		"Decimal32(4)", "Decimal64(4)", "Decimal128(4)", "Decimal256(4)", "Decimal(18, 4)",
	} {
		for _, floatName := range []string{"Float32", "Float64"} {
			decimalType := arithmeticTypeForTest(t, decimalName)
			floatType := arithmeticTypeForTest(t, floatName)
			if got, err := commonCHType(decimalType, floatType); err == nil {
				t.Errorf("commonCHType(%s, %s) = %s, want a refusal",
					decimalName, floatName, got.String())
			}
		}
	}
}

// TestDateArithmetic pins the Date and DateTime rules. Two defects lived
// here: chgen refused a float offset and the modulo form, and it accepted
// a wide integer offset that the server rejects with code 43. Measured on
// ClickHouse 25.8.29.51 with real columns for both operand orders.
func TestDateArithmetic(t *testing.T) {
	cases := []struct {
		op    string
		left  string
		right string
		want  string
	}{
		// A date moves by a narrow integer offset and keeps its type.
		{"+", "Date", "Int16", "Date"},
		{"-", "DateTime", "UInt32", "DateTime"},
		{"+", "Int8", "Date", "Date"},

		// A float offset works too and still keeps the date type.
		{"+", "Date", "Float32", "Date"},
		{"-", "DateTime", "Float64", "DateTime"},
		{"+", "Float64", "Date", "Date"},

		// Two dates of the same type subtract to Int32.
		{"-", "Date", "Date", "Int32"},
		{"-", "DateTime", "DateTime", "Int32"},

		// Modulo takes the date as the dividend; the result follows the
		// divisor.
		{"%", "Date", "Int16", "Int16"},
		{"%", "DateTime", "UInt64", "UInt64"},
		{"%", "Date", "Float64", "Float64"},
		{"%", "DateTime", "Float32", "Float64"},
	}
	for _, testCase := range cases {
		left := arithmeticTypeForTest(t, testCase.left)
		right := arithmeticTypeForTest(t, testCase.right)
		got, err := inferArithmeticResultType(testCase.op, left, right)
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

// TestDateArithmeticRefused pins the date combinations that the server
// rejects with code 43. The wide integer offsets are the important group:
// chgen used to answer Date for them, which is a silently wrong type.
// Measured on ClickHouse 25.8.29.51 with real columns.
func TestDateArithmeticRefused(t *testing.T) {
	cases := []struct {
		op    string
		left  string
		right string
	}{
		// A wide integer offset is rejected by the server.
		{"+", "Date", "Int128"},
		{"-", "Date", "UInt256"},
		{"+", "Int256", "DateTime"},
		{"-", "DateTime", "UInt128"},
		{"%", "Date", "Int128"},
		{"%", "DateTime", "UInt128"},

		// "-" is not commutative: a date on the right needs a date on
		// the left.
		{"-", "Float64", "Date"},
		{"-", "Int32", "DateTime"},

		// Mixing Date with DateTime has no rule.
		{"-", "Date", "DateTime"},
		{"-", "DateTime", "Date"},
		{"+", "Date", "DateTime"},
		{"+", "Date", "Date"},

		// Multiply and divide have no date rule at all.
		{"*", "Date", "Int8"},
		{"/", "Date", "Int8"},
		{"*", "DateTime", "Float64"},
		{"/", "DateTime", "UInt16"},

		// The date must be the dividend of a modulo.
		{"%", "Int16", "Date"},
		{"%", "Float64", "DateTime"},
	}
	for _, testCase := range cases {
		left := arithmeticTypeForTest(t, testCase.left)
		right := arithmeticTypeForTest(t, testCase.right)
		if got, err := inferArithmeticResultType(testCase.op, left, right); err == nil {
			t.Errorf("inferArithmeticResultType(%q, %s, %s) = %s, want a refusal",
				testCase.op, testCase.left, testCase.right, got.String())
		}
	}
}
