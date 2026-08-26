package engine

import "testing"

// TestAbsNegateTypeRule pins the measured result type of abs and negate.
//
// the regression: neither name had a registered type rule before this test.
// A call such as abs(i32) refused with "function abs has no registered
// type rule", although the server answers it. This is the fix, checked
// against ClickHouse 25.8.29.51 with real table columns, never over
// literals, because the server folds constants.
func TestAbsNegateTypeRule(t *testing.T) {
	schema := wrapperTestSchema(t)
	cases := []struct{ expr, want string }{
		// abs widens every signed width to the unsigned type of the SAME
		// width. An unsigned argument stays at its own width.
		{"abs(i8)", "UInt8"},
		{"abs(i16)", "UInt16"},
		{"abs(i32)", "UInt32"},
		{"abs(i64)", "UInt64"},
		{"abs(i128)", "UInt128"},
		{"abs(i256)", "UInt256"},
		{"abs(u8)", "UInt8"},
		{"abs(u16)", "UInt16"},
		{"abs(u32)", "UInt32"},
		{"abs(u64)", "UInt64"},
		{"abs(u128)", "UInt128"},
		{"abs(u256)", "UInt256"},
		{"abs(b)", "UInt8"},
		{"abs(f32)", "Float32"},
		{"abs(f64)", "Float64"},
		{"abs(dec)", "Decimal(18, 4)"},

		// negate keeps a signed width unchanged. An unsigned argument
		// widens to the signed type of DOUBLE its width, capped at 64
		// bits: UInt64 negates to Int64, not Int128.
		{"negate(i8)", "Int8"},
		{"negate(i16)", "Int16"},
		{"negate(i32)", "Int32"},
		{"negate(i64)", "Int64"},
		{"negate(i128)", "Int128"},
		{"negate(i256)", "Int256"},
		{"negate(u8)", "Int16"},
		{"negate(u16)", "Int32"},
		{"negate(u32)", "Int64"},
		{"negate(u64)", "Int64"},
		{"negate(u128)", "Int128"},
		{"negate(u256)", "Int256"},
		{"negate(b)", "Int16"},
		{"negate(f32)", "Float32"},
		{"negate(f64)", "Float64"},
		{"negate(dec)", "Decimal(18, 4)"},

		// Both functions are wrapperTransparent: Nullable moves through
		// from the argument, and LowCardinality moves through when it
		// is the only wrapper (measured: abs(lc-Int32) keeps the
		// wrapper, the same as length(lc) does).
		{"abs(ni32)", "Nullable(UInt32)"},
		{"negate(ni32)", "Nullable(Int32)"},
	}
	for _, testCase := range cases {
		t.Run(testCase.expr, func(t *testing.T) {
			got := inferCHTypeString(t, schema, testCase.expr)
			if got != testCase.want {
				t.Errorf("inferCHTypeString(%q) = %q, want %q", testCase.expr, got, testCase.want)
			}
		})
	}
}

// TestAbsNegateRefuseNonNumeric checks that abs and negate refuse the
// types that the server refuses, rather than answering a silently wrong
// type. Measured: abs(String) and negate(Array(Int32)) both give Code:
// 43, ILLEGAL_TYPE_OF_ARGUMENT.
func TestAbsNegateRefuseNonNumeric(t *testing.T) {
	schema := wrapperTestSchema(t)
	cases := []string{
		"abs(s)", "abs(arr_i)", "abs(m)",
		"negate(s)", "negate(arr_i)", "negate(m)",
	}
	for _, expr := range cases {
		t.Run(expr, func(t *testing.T) {
			if _, err := inferCHTypeErr(t, schema, expr); err == nil {
				t.Errorf("inferCHTypeErr(%q) succeeded, want a refusal", expr)
			}
		})
	}
}
