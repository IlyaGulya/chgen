package engine

import "testing"

// TestUniqFamilyAdditions pins the measured result type of the three
// uniq family members that the regression adds to the registry: uniqCombined64,
// uniqHLL12 and uniqTheta. All three were previously unknown names.
//
// Measured on ClickHouse 25.8.29.51 with real columns.
func TestUniqFamilyAdditions(t *testing.T) {
	schema := wrapperTestSchema(t)
	cases := []struct{ expr, want string }{
		// uniqCombined64 follows uniqCombined: Nullable moves through
		// from the argument, LowCardinality does not survive.
		{"uniqCombined64(s)", "UInt64"},
		{"uniqCombined64(ns)", "Nullable(UInt64)"},
		{"uniqCombined64(lc)", "UInt64"},
		{"uniqCombined64(lcn)", "Nullable(UInt64)"},

		// uniqHLL12 and uniqTheta are opaque: they always give a bare
		// UInt64.
		{"uniqHLL12(s)", "UInt64"},
		{"uniqHLL12(ns)", "UInt64"},
		{"uniqTheta(s)", "UInt64"},
		{"uniqTheta(ns)", "UInt64"},
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
