package engine

import "testing"

// uniqCombined is the one member of the uniq family that moves the
// Nullable wrapper of its argument into its result. The other members
// give a bare UInt64 for the same argument.
//
// Before this test existed, chgen typed uniqCombined(lcn) as UInt64
// while the server gives Nullable(UInt64). chgen writes Go code, thus a
// bare UInt64 becomes a Go field that is not a pointer, and a NULL from
// the server cannot survive the scan. That is a silently wrong type.
//
// Every expected answer below was measured on ClickHouse 25.8.29.51.
// The columns are real columns of a real table, because the server
// folds a literal and a rule from a literal is not true for a column.
// The command was:
//
//	curl -s "http://localhost:18123/" --data-binary \
//	  "SELECT toTypeName(<expr>) FROM chgen_probe_7sn.t"
//
// The measured grid:
//
//	function        String  LowCardinality(String)  Nullable(String)  LowCardinality(Nullable(String))  Nullable(Int64)
//	uniq            UInt64  UInt64                  UInt64            UInt64                            UInt64
//	uniqExact       UInt64  UInt64                  UInt64            UInt64                            UInt64
//	uniqHLL12       UInt64  UInt64                  UInt64            UInt64                            UInt64
//	uniqTheta       UInt64  UInt64                  UInt64            UInt64                            UInt64
//	count           UInt64  UInt64                  UInt64            UInt64                            UInt64
//	uniqCombined    UInt64  UInt64                  Nullable(UInt64)  Nullable(UInt64)                  Nullable(UInt64)
//	uniqCombined64  UInt64  UInt64                  Nullable(UInt64)  Nullable(UInt64)                  Nullable(UInt64)
//
// The rule: the result Nullable follows the Nullable of the argument,
// and only for the Combined members. LowCardinality is not the trigger.
// LowCardinality never stays on the result.
//
// All measured names are now in the registry. The cases below pin the
// Combined pair and their variadic argument positions.

// TestUniqCombinedFollowsArgumentNullable pins the defect that this
// test was written for. Each case fails without the wrapperAggregate
// class on the uniqcombined registry entry.
func TestUniqCombinedFollowsArgumentNullable(t *testing.T) {
	schema := wrapperTestSchema(t)
	for _, testCase := range []struct {
		expr string
		want string
	}{
		// The reported cell: LowCardinality(Nullable(String)).
		{"uniqCombined(lcn)", "Nullable(UInt64)"},
		// The real trigger is the plain Nullable, with no
		// LowCardinality anywhere.
		{"uniqCombined(ns)", "Nullable(UInt64)"},
		{"uniqCombined(ni32)", "Nullable(UInt64)"},
		{"uniqCombined(nf64)", "Nullable(UInt64)"},
		// A Nullable in any argument position is enough.
		{"uniqCombined(s, ns)", "Nullable(UInt64)"},
		{"uniqCombined(ns, s)", "Nullable(UInt64)"},
		{"uniqCombined64(s, ns)", "Nullable(UInt64)"},
		{"uniqCombined64(ns, s)", "Nullable(UInt64)"},
		// An argument that is not Nullable keeps the bare UInt64.
		// LowCardinality alone does not make the result Nullable,
		// and it never stays on the result.
		{"uniqCombined(s)", "UInt64"},
		{"uniqCombined(lc)", "UInt64"},
		{"uniqCombined(i64)", "UInt64"},
		{"uniqCombined(s, i64)", "UInt64"},
		{"uniqCombined64(s, i64)", "UInt64"},
		// The -If form reads its last argument as the condition.
		// The condition does not carry data, thus a Nullable
		// condition does not make the result Nullable.
		// Measured: uniqCombinedIf(s, ni32 > 0) is UInt64 and
		// uniqCombinedIf(ns, i64 > 0) is Nullable(UInt64).
		{"uniqCombinedIf(ns, i64 > 0)", "Nullable(UInt64)"},
		{"uniqCombinedIf(s, ni32 > 0)", "UInt64"},
		{"uniqCombinedIf(s, i64 > 0)", "UInt64"},
	} {
		t.Run(testCase.expr, func(t *testing.T) {
			got := inferCHTypeString(t, schema, testCase.expr)
			if got != testCase.want {
				t.Errorf("inferExprType(%q) = %q, want %q", testCase.expr, got, testCase.want)
			}
		})
	}
}

// TestUniqFamilyNeighboursStayOpaque is the guard half. The other
// members of the uniq family were measured on the same Nullable
// columns and they keep the bare UInt64. A later author who reads the
// uniqCombined fix must not apply it to the whole family: that would
// put a wrong Nullable on five names at once.
func TestUniqFamilyNeighboursStayOpaque(t *testing.T) {
	schema := wrapperTestSchema(t)
	for _, expr := range []string{
		"uniq(ns)", "uniq(lcn)", "uniq(ni32)", "uniq(s)", "uniq(lc)",
		"uniqExact(ns)", "uniqExact(lcn)", "uniqExact(ni32)", "uniqExact(s)",
		"uniqExactIf(ns, i64 > 0)", "uniqExactIf(s, ni32 > 0)",
		"count(ns)", "count(lcn)", "count(ni32)", "count(s)", "count(*)",
		"countIf(ni32 > 0)",
	} {
		t.Run(expr, func(t *testing.T) {
			got := inferCHTypeString(t, schema, expr)
			if got != "UInt64" {
				t.Errorf("inferExprType(%q) = %q, want %q", expr, got, "UInt64")
			}
		})
	}
}
