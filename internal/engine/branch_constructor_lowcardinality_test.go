package engine

import "testing"

// The BRANCH family and the container CONSTRUCTOR need OPPOSITE
// LowCardinality rules, and both must be independent of the ORDER of the
// operands. This file pins the measured grid of both.
//
// The branch family (if, multiIf, ifNull, coalesce) removes the
// LowCardinality wrapper at EVERY depth. The constructor keeps it at a
// position when EVERY member had it at that position, and the rule
// applies again at each nesting depth.
//
// The grid was measured on ClickHouse 25.8.29.51 through the HTTP
// interface, against real columns of a real table, never over literals,
// because the server folds a constant branch and a constant array. The
// probe table was:
//
//	CREATE TABLE t (
//	    b UInt8,
//	    s String, lc LowCardinality(String),
//	    lcn LowCardinality(Nullable(String)), ns Nullable(String),
//	    i32 Int32,
//	    as_ Array(String), alc Array(LowCardinality(String)),
//	    alcn Array(LowCardinality(Nullable(String))),
//	    ans Array(Nullable(String)),
//	    ms Map(String, String), mlc Map(String, LowCardinality(String)),
//	    ts Tuple(Int32, String), tlc Tuple(Int32, LowCardinality(String))
//	) ENGINE = MergeTree ORDER BY tuple();
//
// The cells were executed as well as analysed, because toTypeName reports
// the analysis and can be blind to a refusal at execution time:
//
//	SELECT if(b, alc, as_), [alc, as_], [alc, alc], if(b, tlc, ts) FROM t
//	['a']  [['a'],['a']]  [['a'],['a']]  (1,'a')
//
// This is the regression test of the defect that the order dependence
// made visible: if(b, array(lc), array(s)) answered
// Array(LowCardinality(String)) while the swapped
// if(b, array(s), array(lc)) answered Array(String). The server answers
// Array(String) for both.
func TestBranchAndConstructorLowCardinalityGrid(t *testing.T) {
	schema := wrapperTestSchema(t)

	// The BRANCH family. Every cell appears in BOTH operand orders,
	// because the defect was an order dependence and a grid that tests
	// one order cannot see it.
	//
	// Raw server output for the if rows:
	//
	//	if(b, lc, s)      String        if(b, s, lc)      String
	//	if(b, lc, lc)     String
	//	if(b, lcn, s)     Nullable(String)
	//	if(b, s, lcn)     Nullable(String)
	//	if(b, arr_lc, arr_s)   Array(String)
	//	if(b, arr_s, arr_lc)   Array(String)
	//	if(b, arr_lc, arr_lc)  Array(String)
	//	if(b, tup_lc, tup_s)   Tuple(Int32, String)
	//	if(b, tup_s, tup_lc)   Tuple(Int32, String)
	//	if(b, tup_lc, tup_lc)  Tuple(Int32, String)
	//	if(b, map_lc, map_s)   Map(String, String)
	//	if(b, map_s, map_lc)   Map(String, String)
	//	if(b, map_lc, map_lc)  Map(String, String)
	branch := []struct{ expr, want string }{
		// The scalar case. The wrapper comes OFF, which is the rule
		// that TestSimpleAggregateBranchKeepsWrapper and its
		// neighbours already pin. It must stay true.
		{"if(b, lc, s)", "String"},
		{"if(b, s, lc)", "String"},
		{"if(b, lc, lc)", "String"},
		{"if(b, lcn, s)", "Nullable(String)"},
		{"if(b, s, lcn)", "Nullable(String)"},
		{"if(b, lcn, lc)", "Nullable(String)"},
		{"if(b, lc, lcn)", "Nullable(String)"},
		{"if(b, ns, s)", "Nullable(String)"},
		{"if(b, s, ns)", "Nullable(String)"},

		// The nested case, which is the defect. An all-LowCardinality
		// operand pair ALSO loses the wrapper here, unlike a
		// constructor.
		{"if(b, array(lc), array(s))", "Array(String)"},
		{"if(b, array(s), array(lc))", "Array(String)"},
		{"if(b, array(lc), array(lc))", "Array(String)"},
		{"if(b, (i32,lc), (i32,s))", "Tuple(Int32, String)"},
		{"if(b, (i32,s), (i32,lc))", "Tuple(Int32, String)"},
		{"if(b, (i32,lc), (i32,lc))", "Tuple(Int32, String)"},

		// The rule is the same for the whole branch family, thus one
		// entry point must give all of them the same answer.
		{"multiIf(b, array(lc), array(s))", "Array(String)"},
		{"multiIf(b, array(s), array(lc))", "Array(String)"},
		{"multiIf(b, array(lc), array(lc))", "Array(String)"},
		{"multiIf(b, lc, s)", "String"},
		{"multiIf(b, s, lc)", "String"},
		{"ifNull(array(lc), array(s))", "Array(String)"},
		{"ifNull(array(s), array(lc))", "Array(String)"},
		{"ifNull(array(lc), array(lc))", "Array(String)"},
		{"ifNull(lc, s)", "String"},
		{"ifNull(s, lc)", "String"},
		{"coalesce(array(lc), array(s))", "Array(String)"},
		{"coalesce(array(s), array(lc))", "Array(String)"},
		{"coalesce(array(lc), array(lc))", "Array(String)"},
		{"coalesce(lc, s)", "String"},
		{"coalesce(s, lc)", "String"},
	}
	for _, testCase := range branch {
		t.Run("branch "+testCase.expr, func(t *testing.T) {
			if got := inferCHTypeString(t, schema, testCase.expr); got != testCase.want {
				t.Errorf("inferExprType(%q) = %q, want %q", testCase.expr, got, testCase.want)
			}
		})
	}

	// The container CONSTRUCTOR. The wrapper stays when EVERY member had
	// it, at every depth, and the answer is order-independent as well.
	//
	// Raw server output:
	//
	//	[lc, lc]      Array(LowCardinality(String))
	//	[lc, s]       Array(String)
	//	[s, lc]       Array(String)
	//	[arr_lc, arr_lc]   Array(Array(LowCardinality(String)))
	//	[arr_lc, arr_s]    Array(Array(String))
	//	[arr_s, arr_lc]    Array(Array(String))
	//	[tup_lc, tup_lc]   Array(Tuple(Int32, LowCardinality(String)))
	//	[tup_lc, tup_s]    Array(Tuple(Int32, String))
	//	[map_lc, map_lc]   Array(Map(String, LowCardinality(String)))
	//	[map_lc, map_s]    Array(Map(String, String))
	//	[array(array(lc)), array(array(lc))]
	//	                   Array(Array(Array(LowCardinality(String))))
	//	[array(array(lc)), array(array(s))]
	//	                   Array(Array(Array(String)))
	constructor := []struct{ expr, want string }{
		{"[lc, lc]", "Array(LowCardinality(String))"},
		{"[lc, s]", "Array(String)"},
		{"[s, lc]", "Array(String)"},

		{"[array(lc), array(lc)]", "Array(Array(LowCardinality(String)))"},
		{"[array(lc), array(s)]", "Array(Array(String))"},
		{"[array(s), array(lc)]", "Array(Array(String))"},

		{"[(i32,lc), (i32,lc)]", "Array(Tuple(Int32, LowCardinality(String)))"},
		{"[(i32,lc), (i32,s)]", "Array(Tuple(Int32, String))"},
		{"[(i32,s), (i32,lc)]", "Array(Tuple(Int32, String))"},

		{"[map('k',lc), map('k',lc)]", "Array(Map(String, LowCardinality(String)))"},
		{"[map('k',lc), map('k',s)]", "Array(Map(String, String))"},
		{"[map('k',s), map('k',lc)]", "Array(Map(String, String))"},

		// The rule applies again at each depth, thus a third level of
		// nesting keeps or drops the wrapper on its own.
		{"[array(array(lc)), array(array(lc))]", "Array(Array(Array(LowCardinality(String))))"},
		{"[array(array(lc)), array(array(s))]", "Array(Array(Array(String)))"},
		{"[array(array(s)), array(array(lc))]", "Array(Array(Array(String)))"},
	}
	for _, testCase := range constructor {
		t.Run("constructor "+testCase.expr, func(t *testing.T) {
			if got := inferCHTypeString(t, schema, testCase.expr); got != testCase.want {
				t.Errorf("inferExprType(%q) = %q, want %q", testCase.expr, got, testCase.want)
			}
		})
	}

	// The two families must DISAGREE on the all-LowCardinality pair.
	// This is the cell that a single blanket rule would get wrong, and
	// the project has been burned by a blanket rule before, thus the
	// disagreement is pinned on its own.
	t.Run("families disagree on the all-LowCardinality pair", func(t *testing.T) {
		branchAnswer := inferCHTypeString(t, schema, "if(b, array(lc), array(lc))")
		constructorAnswer := inferCHTypeString(t, schema, "[array(lc), array(lc)]")
		if branchAnswer != "Array(String)" {
			t.Errorf("if(b, array(lc), array(lc)) = %q, want %q", branchAnswer, "Array(String)")
		}
		if constructorAnswer != "Array(Array(LowCardinality(String)))" {
			t.Errorf("[array(lc), array(lc)] = %q, want %q", constructorAnswer, "Array(Array(LowCardinality(String)))")
		}
	})
}
