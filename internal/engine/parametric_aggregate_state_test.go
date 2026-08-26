package engine

import "testing"

// A parametric aggregate has TWO argument lists. In quantileState(0.5)(x)
// the level 0.5 is a PARAMETER and x is the DATA argument. The
// AggregateFunction type carries the parameters only. The data argument
// belongs in the type list that follows the name, never in the parentheses
// after the name.
//
// chgen put the text of the data argument where the parameters belong, thus
// quantileState(f64) gave AggregateFunction(quantile(f64), Float64). That
// spelling is not merely a wrong type, it is not a type at all: the server
// refuses it with Code: 134,
// PARAMETERS_TO_AGGREGATE_FUNCTIONS_MUST_BE_LITERALS.
//
// Measured on ClickHouse 25.8.29.51 through HTTP, over the columns of a
// real table, because the server folds constant expressions:
//
//	quantileState(f64)                  AggregateFunction(quantile, Float64)
//	quantileState(0.5)(f64)             AggregateFunction(quantile(0.5), Float64)
//	quantileStateIf(f64, b)             AggregateFunction(quantile, Float64)
//	quantileStateIf(0.5)(f64, b)        AggregateFunction(quantile(0.5), Float64)
//	quantileExactState(f64)             AggregateFunction(quantileExact, Float64)
//	quantileExactState(0.9)(f64)        AggregateFunction(quantileExact(0.9), Float64)
//	quantileTimingState(f64)            AggregateFunction(quantileTiming, Float64)
//	quantileTimingState(0.99)(f64)      AggregateFunction(quantileTiming(0.99), Float64)
//	quantileTDigestState(f64)           AggregateFunction(quantileTDigest, Float64)
//	quantileTDigestState(0.5)(f64)      AggregateFunction(quantileTDigest(0.5), Float64)
//	quantilesExactState(0.5, 0.9)(f64)  AggregateFunction(quantilesExact(0.5, 0.9), Float64)
//	topKState(s)                        AggregateFunction(topK, String)
//	topKState(3)(s)                     AggregateFunction(topK(3), String)
//	groupArrayState(i32)                AggregateFunction(groupArray, Int32)
//	groupArrayState(4)(i32)             AggregateFunction(groupArray(4), Int32)
//	uniqCombinedState(i32)              AggregateFunction(uniqCombined, Int32)
//	uniqCombinedState(12)(i32)          AggregateFunction(uniqCombined(12), Int32)
//	maxIntersectionsState(i32, i32)     AggregateFunction(maxIntersections, Int32, Int32)
//	histogramState(5)(f64)              AggregateFunction(histogram(5), Float64)
//
// The sweep found no aggregate in this family where the server puts a data
// argument in the parameter list. The rule is uniform: the parentheses
// after the name hold the parameters, and they are empty when the call
// gives none.
//
// The plain spelling is the one that broke. The parser puts the data
// argument of quantileState(x) into the same node that holds the parameters
// of quantileState(0.5)(x), thus a reader of that node must first make sure
// that a separate data argument list is present.

const parametricStateSchema = `
CREATE TABLE t (
    i32 Int32,
    f64 Float64,
    s   String,
    b   Bool
);
`

// TestParametricAggregateStateParameterList pins the measured table. The two
// spellings must give different answers: the bare call has no parameters and
// the parametric call keeps its literal levels.
func TestParametricAggregateStateParameterList(t *testing.T) {
	schema, err := schemaFromDDLErr(t, parametricStateSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	cases := []struct{ expr, want string }{
		// No parameter present. The parameter list must stay empty,
		// and the data argument must NOT appear inside it.
		{"quantileState(f64)", "AggregateFunction(quantile, Float64)"},
		{"quantileState(i32)", "AggregateFunction(quantile, Int32)"},
		{"quantileState(b)", "AggregateFunction(quantile, Bool)"},

		// A parameter present. The literal level belongs in the
		// parameter list, and the data argument stays out of it.
		{"quantileState(0.5)(f64)", "AggregateFunction(quantile(0.5), Float64)"},
		{"quantileState(0.9)(i32)", "AggregateFunction(quantile(0.9), Int32)"},

		// The -If form. Its condition argument is not a parameter
		// either, thus the bare spelling keeps an empty list.
		{"quantileStateIf(f64, b)", "AggregateFunction(quantile, Float64)"},
		{"quantileStateIf(0.5)(f64, b)", "AggregateFunction(quantile(0.5), Float64)"},

		// The family members that the combinator path types. They
		// take the same rule, thus they belong in the same pin.
		{"groupArrayState(i32)", "AggregateFunction(groupArray, Int32)"},
		{"groupArrayState(s)", "AggregateFunction(groupArray, String)"},
		{"uniqCombinedState(i32)", "AggregateFunction(uniqCombined, Int32)"},
	}
	for _, testCase := range cases {
		got, err := combinatorCHType(t, schema, "t", testCase.expr)
		if err != nil {
			t.Errorf("%s: error = %v, want %q", testCase.expr, err, testCase.want)
			continue
		}
		if got != testCase.want {
			t.Errorf("%s: got %q, want %q", testCase.expr, got, testCase.want)
		}
	}
}

// TestParametricAggregateStateSpellingsDiffer states the distinction on its
// own. If the two spellings ever give the same answer again, one of them is
// wrong, because the server tells them apart.
func TestParametricAggregateStateSpellingsDiffer(t *testing.T) {
	schema, err := schemaFromDDLErr(t, parametricStateSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	bare, err := combinatorCHType(t, schema, "t", "quantileState(f64)")
	if err != nil {
		t.Fatalf("quantileState(f64): error = %v", err)
	}
	parametric, err := combinatorCHType(t, schema, "t", "quantileState(0.5)(f64)")
	if err != nil {
		t.Fatalf("quantileState(0.5)(f64): error = %v", err)
	}
	if bare == parametric {
		t.Fatalf("the two spellings give the same type %q; the server tells them apart", bare)
	}
	// The data argument must never appear in the parameter list. The
	// column name f64 inside the parentheses is the exact defect.
	if bare != "AggregateFunction(quantile, Float64)" {
		t.Errorf("bare spelling got %q, want AggregateFunction(quantile, Float64)", bare)
	}
}
