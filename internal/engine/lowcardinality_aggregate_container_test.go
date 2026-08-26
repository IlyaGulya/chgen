package engine

import "testing"

// An aggregate removes every LowCardinality wrapper from its result, at
// the top level and at every depth inside a container. A plain
// constructor keeps the wrapper at every depth. The rule belongs to the
// aggregate, not to the container.
//
// Every expected type below was measured on ClickHouse 25.8.29.51 with
// real columns in a real table, never on literals alone, because the
// server folds constants. The table was:
//
//	CREATE TABLE t (
//	    i32 Int32, ni32 Nullable(Int32),
//	    lc  LowCardinality(String),
//	    lcn LowCardinality(Nullable(String)),
//	    s   String,
//	    arr Array(LowCardinality(String)),
//	    m   Map(String, LowCardinality(String))
//	) ENGINE = MergeTree ORDER BY i32;
//
// The commands had this shape:
//
//	SELECT toTypeName(argMin((i32, lc), ni32)) FROM t
//	Tuple(Int32, String)
//
// Before the fix chgen answered Tuple(Int32, LowCardinality(String)) for
// that cell and Array(LowCardinality(String)) for argMin(array(lc), ni32).
// Neither type comes back from the server. clickhouse-go scans the two
// forms differently, thus the generated Go was silently wrong.
//
// The cells were run and not only analysed, because toTypeName reports
// the analysis and can be blind to an execution-time refusal:
//
//	SELECT argMin((i32, lc), ni32), argMin(arr, ni32), argMin(m, ni32),
//	       any(tuple(i32, lc)), groupArray(lc) FROM t
//	(1,'a')	['a']	{'k':'v'}	(1,'a')	['a']

// TestAggregateStripsLowCardinalityInsideContainer pins the aggregate
// side of the rule: the wrapper comes off at every depth.
func TestAggregateStripsLowCardinalityInsideContainer(t *testing.T) {
	schema := wrapperTestSchema(t)
	cases := []struct{ expr, want string }{
		// The measured cell of the defect report.
		{"argMin((i32, lc), ni32)", "Tuple(Int32, String)"},
		{"argMax((i32, lc), ni32)", "Tuple(Int32, String)"},
		// The same aggregate over a bare LowCardinality argument. The
		// wrapper comes off here as well, and the Nullable of the
		// ordering argument stays, because String can go inside
		// Nullable.
		{"argMin(lc, ni32)", "Nullable(String)"},
		{"argMax(lc, ni32)", "Nullable(String)"},
		{"argMin(lcn, ni32)", "Nullable(String)"},
		// An Array member loses the wrapper too. This is the proof
		// that the rule is not about Tuple.
		{"argMin(array(lc), ni32)", "Array(String)"},
		{"argMin([lc, lc], ni32)", "Array(String)"},
		// Nesting is not limited to one level.
		{"argMin(tuple(i32, array(lc)), ni32)", "Tuple(Int32, Array(String))"},
		{"argMin(tuple(tuple(tuple(lc))), ni32)", "Tuple(Tuple(Tuple(String)))"},
		{"argMin(array(tuple(lc)), ni32)", "Array(Tuple(String))"},
		// Only the LowCardinality wrapper comes off. An inner Nullable
		// stays, thus LowCardinality(Nullable(String)) becomes
		// Nullable(String) and not String.
		{"argMin(tuple(i32, lcn), ni32)", "Tuple(Int32, Nullable(String))"},
		{"any(tuple(lcn))", "Tuple(Nullable(String))"},
		// The rule is not special to argMin. Every aggregate has it.
		{"any((i32, lc))", "Tuple(Int32, String)"},
		{"anyLast((i32, lc))", "Tuple(Int32, String)"},
		{"max((i32, lc))", "Tuple(Int32, String)"},
		{"min((i32, lc))", "Tuple(Int32, String)"},
		{"any(lc)", "String"},
		{"min(lc)", "String"},
		{"max(lc)", "String"},
		{"groupArray(lc)", "Array(String)"},
		{"groupArray((i32, lc))", "Array(Tuple(Int32, String))"},
		// A combinator keeps the rule of its base aggregate.
		{"argMinIf((i32, lc), ni32, i32 > 0)", "Tuple(Int32, String)"},
	}
	for _, testCase := range cases {
		t.Run(testCase.expr, func(t *testing.T) {
			if got := inferCHTypeString(t, schema, testCase.expr); got != testCase.want {
				t.Errorf("inferExprType(%q) = %q, want %q", testCase.expr, got, testCase.want)
			}
		})
	}
}

// TestConstructorKeepsLowCardinalityInsideContainer pins the other side
// of the rule. A plain constructor is not an aggregate and keeps the
// wrapper at every depth. A fix that removed the wrapper everywhere
// would break these cells.
func TestConstructorKeepsLowCardinalityInsideContainer(t *testing.T) {
	schema := wrapperTestSchema(t)
	cases := []struct{ expr, want string }{
		{"tuple(i32, lc)", "Tuple(Int32, LowCardinality(String))"},
		{"(i32, lc)", "Tuple(Int32, LowCardinality(String))"},
		{"tuple(lc)", "Tuple(LowCardinality(String))"},
		{"tuple(lcn)", "Tuple(LowCardinality(Nullable(String)))"},
		{"array(lc)", "Array(LowCardinality(String))"},
		{"[lc, lc]", "Array(LowCardinality(String))"},
		// An array literal that mixes lc and s is left out on purpose.
		// The server answers Array(String) there, because the common
		// supertype of the two members is String, but chgen refuses
		// the mixed literal. That is a separate defect about the array
		// literal supertype, not about the aggregate rule of this
		// test.
		//
		// greatest and least are scalar although chgen classifies them
		// with the aggregates. At arity 1 they keep the wrapper.
		{"greatest(lc)", "LowCardinality(String)"},
		{"least(lc)", "LowCardinality(String)"},
		{"greatest(lc, s)", "String"},
	}
	for _, testCase := range cases {
		t.Run(testCase.expr, func(t *testing.T) {
			if got := inferCHTypeString(t, schema, testCase.expr); got != testCase.want {
				t.Errorf("inferExprType(%q) = %q, want %q", testCase.expr, got, testCase.want)
			}
		})
	}
}
