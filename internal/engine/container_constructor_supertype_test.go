package engine

import (
	"strings"
	"testing"
)

// A container CONSTRUCTOR gives its member type the common supertype of
// its arguments. The array constructor, the array literal and the map
// constructor all share that one rule.
//
// Every expected value below was measured on ClickHouse 25.8.29.51 with
// real columns in a real table, never on literals alone, because the
// server folds a constant array and would then answer for the folded
// constant instead of the expression. The probe table was:
//
//	CREATE TABLE t (
//	    i8 Int8, i16 Int16, i32 Int32, i64 Int64,
//	    u8 UInt8, u32 UInt32,
//	    f64 Float64, dec Decimal(18, 4),
//	    s String, fs FixedString(4),
//	    lc LowCardinality(String), lcn LowCardinality(Nullable(String)),
//	    ni32 Nullable(Int32), nlc Nullable(String),
//	    d Date, dt DateTime, uu UUID
//	) ENGINE = MergeTree ORDER BY tuple();
//
// The commands had this shape:
//
//	SELECT toTypeName([lc, s]) FROM t
//	Array(String)
//
// The cells were also run and not only analysed, because toTypeName
// reports the analysis and can be blind to a refusal at execution time:
//
//	SELECT [lc, s], [dec, i32], [d, dt] FROM t
//	['a','a']  [1,1]  ['2020-01-01 00:00:00','2020-01-01 00:00:00']
//
//	SELECT map('k', lc), map('a', lc, 'b', s), map(lc, s, s, s) FROM t
//	{'k':'a'}  {'a':'a','b':'a'}  {'a':'a','a':'a'}
//
// The LowCardinality cell is the interesting one. A constructor keeps
// LowCardinality when EVERY member has it, and drops it as soon as one
// member is bare. That is not the same as the plain supertype lattice,
// which always unwraps LowCardinality: if(b, lc, lc) is String on the
// same server, while [lc, lc] is Array(LowCardinality(String)).
// TestConstructorKeepsLowCardinalityInsideContainer pins the other half
// of that rule.

// TestContainerConstructorMemberSupertype pins the measured grid of the
// array constructor, the array literal and the map constructor,
// including the cells that the server refuses.
func TestContainerConstructorMemberSupertype(t *testing.T) {
	schema := wrapperTestSchema(t)

	accepted := []struct{ expr, want string }{
		// One member type, the case that already worked.
		{"[s, s]", "Array(String)"},
		{"[lc]", "Array(LowCardinality(String))"},
		{"[fs, fs]", "Array(FixedString(8))"},

		// LowCardinality. Every member wrapped keeps the wrapper; one
		// bare member removes it from all of them.
		{"[lc, lc]", "Array(LowCardinality(String))"},
		{"[lc, lc, lc]", "Array(LowCardinality(String))"},
		{"[lc, s]", "Array(String)"},
		{"[s, lc]", "Array(String)"},
		{"[lc, s, lc]", "Array(String)"},
		{"[lcn, lc]", "Array(LowCardinality(Nullable(String)))"},
		{"[lcn, lcn]", "Array(LowCardinality(Nullable(String)))"},
		{"[ns, lc]", "Array(Nullable(String))"},
		{"[lcn, s]", "Array(Nullable(String))"},

		// Nullable mixed with non-Nullable.
		{"[ni32, i32]", "Array(Nullable(Int32))"},
		{"[i32, ni32]", "Array(Nullable(Int32))"},

		// Integer widths, and signed mixed with unsigned.
		{"[i8, i32]", "Array(Int32)"},
		{"[i8, i16, i32]", "Array(Int32)"},
		// Two literals of different width. The server folds this one,
		// and the folded answer is the same rule:
		// SELECT toTypeName([1, 300]) is Array(UInt16).
		{"[1, 300]", "Array(UInt16)"},
		{"[u8, u32]", "Array(UInt32)"},
		{"[i32, u32]", "Array(Int64)"},

		// Integer with Float, and Decimal with Integer.
		{"[i32, f64]", "Array(Float64)"},
		{"[dec, i32]", "Array(Decimal(18, 4))"},
		{"[i32, u32, dec]", "Array(Decimal(18, 4))"},

		// String with FixedString, and the temporal pair.
		{"[s, fs]", "Array(String)"},
		{"[d, dt]", "Array(DateTime)"},

		// The array constructor is the same function as the literal.
		{"array(lc, s)", "Array(String)"},
		{"array(lc, lc)", "Array(LowCardinality(String))"},
		{"array(i8, i32)", "Array(Int32)"},

		// The map constructor. The key type joins the odd positions
		// and the value type joins the even positions.
		{"map('k', s)", "Map(String, String)"},
		{"map('k', lc)", "Map(String, LowCardinality(String))"},
		{"map(lc, s)", "Map(LowCardinality(String), String)"},
		{"map(s, i32)", "Map(String, Int32)"},
		{"map(d, s)", "Map(Date, String)"},
		{"map(uu, s)", "Map(UUID, String)"},
		{"map(fs, s)", "Map(FixedString(8), String)"},
		{"map('a', i32, 'b', i64)", "Map(String, Int64)"},
		{"map(i32, s, i64, s)", "Map(Int64, String)"},
		{"map(lc, s, lc, s)", "Map(LowCardinality(String), String)"},
		{"map(lc, s, s, s)", "Map(String, String)"},
		{"map('a', lc, 'b', s)", "Map(String, String)"},
		{"map(lc, lc, lc, lc)", "Map(LowCardinality(String), LowCardinality(String))"},
		// A Nullable VALUE is legal; only a Nullable key is not.
		{"map('a', ni32)", "Map(String, Nullable(Int32))"},
		// A constructor inside a map value keeps LowCardinality at
		// every depth, which is the rule of
		// TestConstructorKeepsLowCardinalityInsideContainer.
		{"map('k', array(lc))", "Map(String, Array(LowCardinality(String)))"},
		{"map('k', (i32, lc))", "Map(String, Tuple(Int32, LowCardinality(String)))"},
	}
	for _, testCase := range accepted {
		t.Run(testCase.expr, func(t *testing.T) {
			if got := inferCHTypeString(t, schema, testCase.expr); got != testCase.want {
				t.Errorf("inferExprType(%q) = %q, want %q", testCase.expr, got, testCase.want)
			}
		})
	}

	// The server refuses these. A refusal from chgen is the correct
	// answer, thus the cells are pinned as refusals. The server code is
	// quoted for each one.
	refused := []struct{ expr, contains string }{
		// Code 386, NO_COMMON_TYPE: "some of them are
		// String/FixedString/Enum and some of them are not".
		{"[s, i32]", "no common ClickHouse type"},
		{"array(s, i32)", "no common ClickHouse type"},
		{"[uu, s]", "no common ClickHouse type"},
		// Code 386, NO_COMMON_TYPE: "some of them have no lossless
		// conversion to Decimal".
		{"[dec, f64]", "no common ClickHouse type"},
		{"map('a', dec, 'b', f64)", "no common ClickHouse type"},
		// Code 386 through the map value position.
		{"map('a', i32, 'b', s)", "no common ClickHouse type"},
		// Code 42, NUMBER_OF_ARGUMENTS_DOESNT_MATCH:
		// "Function map requires even number of arguments".
		{"map('a', s, 'b')", "even number of arguments"},
		// Code 36, BAD_ARGUMENTS: "Map cannot have a key of type
		// Nullable(Int32)". The LowCardinality(Nullable(String)) key
		// is refused with the same code.
		{"map(ni32, s)", "cannot have a key of type"},
		{"map(ns, s)", "cannot have a key of type"},
		{"map(lcn, s)", "cannot have a key of type"},
	}
	for _, testCase := range refused {
		t.Run("refused "+testCase.expr, func(t *testing.T) {
			got, err := inferCHTypeErr(t, schema, testCase.expr)
			if err == nil {
				t.Fatalf("inferExprType(%q) = %q, want a refusal", testCase.expr, got)
			}
			if !strings.Contains(err.Error(), testCase.contains) {
				t.Errorf("inferExprType(%q) error = %v, want it to contain %q", testCase.expr, err, testCase.contains)
			}
		})
	}

	// These two cells were a KNOWN DIVERGENCE. They now agree with the
	// server, thus they are pinned as ordinary accepted cells.
	//
	// The cause was in the supertype entry point, not in the constructor
	// rule. commonCHType removed the LowCardinality wrapper at the TOP
	// level only, and then the compatibleNamedArgTypes shortcut, which
	// looks through LowCardinality at EVERY depth, found the two members
	// "compatible" and returned the LEFT one unchanged. A nested wrapper
	// therefore came from whichever member the caller wrote first.
	// commonCHType now removes the wrapper at every depth, thus the
	// answer no longer depends on the order.
	//
	//	SELECT toTypeName([array(lc), array(s)])   Array(Array(String))
	//	SELECT toTypeName([array(s), array(lc)])   Array(Array(String))
	//	SELECT toTypeName([(i32,lc), (i32,s)])     Array(Tuple(Int32, String))
	//	SELECT toTypeName([(i32,s), (i32,lc)])     Array(Tuple(Int32, String))
	//
	// An all-LowCardinality member set keeps the wrapper at the nested
	// position, because the constructor rule applies again at that
	// depth. TestBranchAndConstructorLowCardinalityGrid holds the full
	// measured grid of both families, in both operand orders.
	nestedWrapper := []struct{ expr, want string }{
		{"[array(lc), array(s)]", "Array(Array(String))"},
		{"[array(s), array(lc)]", "Array(Array(String))"},
		{"[array(lc), array(lc)]", "Array(Array(LowCardinality(String)))"},
		{"[(i32,lc), (i32,s)]", "Array(Tuple(Int32, String))"},
		{"[(i32,s), (i32,lc)]", "Array(Tuple(Int32, String))"},
		{"[(i32,lc), (i32,lc)]", "Array(Tuple(Int32, LowCardinality(String)))"},
	}
	for _, testCase := range nestedWrapper {
		t.Run("nested wrapper "+testCase.expr, func(t *testing.T) {
			if got := inferCHTypeString(t, schema, testCase.expr); got != testCase.want {
				t.Errorf("inferExprType(%q) = %q, want %q", testCase.expr, got, testCase.want)
			}
		})
	}

	// chgen refuses two argument sets that the server accepts, because
	// the server answers with the Nothing type, which has no Go type to
	// generate. SELECT toTypeName([]) is Array(Nothing) and
	// SELECT toTypeName(map()) is Map(Nothing, Nothing).
	for _, expr := range []string{"[]", "map()"} {
		t.Run("nothing "+expr, func(t *testing.T) {
			if got, err := inferCHTypeErr(t, schema, expr); err == nil {
				t.Fatalf("inferExprType(%q) = %q, want a refusal", expr, got)
			}
		})
	}
}
