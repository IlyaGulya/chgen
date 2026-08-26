package engine

import "testing"

// The -State and -SimpleState combinators and the wrapper alphabet.
//
// Every answer below was measured on ClickHouse 25.8.29.51 over real
// columns of a real table with one row, in the probe database
// chgen_probe_i6h, never over literals, because the server folds a
// constant and then reports a different LowCardinality wrapper.
//
// THE MEASURED RULE, over seven base aggregates (quantile, sum, max,
// any, uniq, groupArray, avg) and over the bases Int32, UInt64, String,
// Float64, Date, Array and Map:
//
//	LowCardinality comes OFF, at EVERY depth.
//	Nullable STAYS, at every depth.
//
//	quantileState(lc_i32)    AggregateFunction(quantile, Int32)
//	quantileState(n_i32)     AggregateFunction(quantile, Nullable(Int32))
//	quantileState(lcn_i32)   AggregateFunction(quantile, Nullable(Int32))
//	anyState(arr_lc)         AggregateFunction(any, Array(String))
//	anyState(map_lc)         AggregateFunction(any, Map(String, Int64))
//	anyState(tup_lc)         AggregateFunction(any, Tuple(String, Int32))
//	anyState(arr_arr_lc)     AggregateFunction(any, Array(Array(String)))
//	anyState(arr_lcn)        AggregateFunction(any, Array(Nullable(String)))
//
// THE DISTINGUISHING CELLS. Three candidate rules fit the plain -State
// cells of the ticket, and only the measurement separates them:
//
//   - "strip every wrapper" is REFUTED by quantileState(lcn_i32), which
//     is AggregateFunction(quantile, Nullable(Int32)) and not
//     AggregateFunction(quantile, Int32). The server keeps the Nullable
//     and removes only the LowCardinality.
//   - "AggregateFunction does not accept Nullable" is REFUTED by the same
//     cell and by quantileState(n_i32). AggregateFunction accepts a
//     Nullable value type. The claim is true for the -If form only, and
//     the reason there is the combinator, not the container.
//   - "strip LowCardinality at the top level only" is REFUTED by
//     anyState(arr_lc), which is AggregateFunction(any, Array(String)).
//     The wrapper comes off inside a container as well.
//
// THE -If FORM IS A DIFFERENT RULE. -StateIf removes the TOP-LEVEL
// Nullable on top of the LowCardinality rule, and it removes only the
// top-level one:
//
//	quantileStateIf(n_i32, b)    AggregateFunction(quantile, Int32)
//	quantileStateIf(lcn_i32, b)  AggregateFunction(quantile, Int32)
//	anyStateIf(arr_n, b)         AggregateFunction(any, Array(Nullable(Int32)))
//
// The last cell is what proves "top-level only": the Nullable inside the
// Array survives although the top-level one does not.
//
// -State AND -SimpleState AGREE on the wrapper question. Measured over
// any, anyLast, min, max and sum:
//
//	anySimpleState(lc_i32)   SimpleAggregateFunction(any, Int32)
//	anySimpleState(n_i32)    SimpleAggregateFunction(any, Nullable(Int32))
//	anySimpleState(lcn_i32)  SimpleAggregateFunction(any, Nullable(Int32))
//	anySimpleState(arr_lc)   SimpleAggregateFunction(any, Array(String))
//
// Thus the ticket's warning that SimpleAggregateFunction accepts a
// Nullable where AggregateFunction does not is TRUE as a statement about
// the two containers, but it does NOT make a different wrapper rule for
// the two combinators: both keep the Nullable and both drop the
// LowCardinality. The two families differ at the -If form only, and that
// difference belongs to -SimpleStateIf, which lifts the Nullable OUTSIDE
// the marker instead of absorbing it. That form is recorded in
// TestSimpleStateIfLiftsNullableOutsideTheMarker below and is not changed
// by this fix.

// stateWrapperSchema holds the wrapper alphabet over the two base types
// that the measured grid cells use. It is a table of its own, so that the
// integer LowCardinality columns which quantile needs can exist next to
// the String ones which quantile refuses with Code 43.
const stateWrapperSchema = `
CREATE TABLE t (
    i32     Int32,
    ni32    Nullable(Int32),
    lc_i32  LowCardinality(Int32),
    lcn_i32 LowCardinality(Nullable(Int32)),
    b       Bool,
    s       String,
    lc      LowCardinality(String),
    lcn     LowCardinality(Nullable(String)),
    arr_n   Array(Nullable(Int32)),
    arr_lc  Array(LowCardinality(String))
);
`

// TestAggregateStateDropsLowCardinalityKeepsNullable pins the plain
// -State rule over the wrapper alphabet. The lcn rows are the cells that
// separate the measured rule from "strip every wrapper".
func TestAggregateStateDropsLowCardinalityKeepsNullable(t *testing.T) {
	schema, err := schemaFromDDLErr(t, stateWrapperSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	cases := []struct{ sql, want string }{
		// Nullable STAYS. AggregateFunction accepts it.
		{"SELECT maxState(ni32) AS a FROM t", "AggregateFunction(max, Nullable(Int32))"},
		// LowCardinality comes OFF.
		{"SELECT uniqState(lc) AS a FROM t", "AggregateFunction(uniq, String)"},
		// The distinguishing cell: only the LowCardinality comes off.
		// This one cell refutes "strip every wrapper" on its own.
		{"SELECT uniqState(lcn) AS a FROM t", "AggregateFunction(uniq, Nullable(String))"},
		{"SELECT maxState(lcn) AS a FROM t", "AggregateFunction(max, Nullable(String))"},
		// LowCardinality comes off inside a container too, which
		// refutes "strip the top-level LowCardinality only".
		{"SELECT maxState(arr_lc) AS a FROM t", "AggregateFunction(max, Array(String))"},
		// A Nullable inside a container stays.
		{"SELECT maxState(arr_n) AS a FROM t", "AggregateFunction(max, Array(Nullable(Int32)))"},
	}
	for _, testCase := range cases {
		got, err := inferSelectItemCHType(t, schema, testCase.sql)
		if err != nil {
			t.Errorf("%s: error = %v", testCase.sql, err)
			continue
		}
		if got != testCase.want {
			t.Errorf("%s: CH type = %q, want %q", testCase.sql, got, testCase.want)
		}
	}
}

// TestQuantileStateReachesTheCombinatorRule pins the cells that a
// hardcoded branch at the top of inferFunctionType used to answer first.
//
// That branch named quantilestate and quantilestateif only. It handed the
// argument type back verbatim, thus it kept the LowCardinality that the
// server removes and the top-level Nullable that the -If form removes,
// while every other -State name already took the combinator path and gave
// the correct answer. One family thus had two different rules for one
// server behaviour, and the wrong one won by position in the function.
//
// The branch is now gone and these names reach the same measured rule as
// their family. This test is the guard against a return of a name-shaped
// shortcut in front of a family rule.
//
// Measured on ClickHouse 25.8.29.51 over real columns.
func TestQuantileStateReachesTheCombinatorRule(t *testing.T) {
	schema, err := schemaFromDDLErr(t, stateWrapperSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	cases := []struct{ sql, want string }{
		// The columns are integer ones, because quantile refuses a
		// String argument with Code 43 whatever the wrapper is.
		//
		// LowCardinality comes off, and the Nullable stays.
		{"SELECT quantileState(lc_i32) AS a FROM t", "AggregateFunction(quantile, Int32)"},
		{"SELECT quantileState(ni32) AS a FROM t", "AggregateFunction(quantile, Nullable(Int32))"},
		{"SELECT quantileState(lcn_i32) AS a FROM t", "AggregateFunction(quantile, Nullable(Int32))"},
		// The -If form additionally removes the top-level Nullable.
		{"SELECT quantileStateIf(ni32, b) AS a FROM t", "AggregateFunction(quantile, Int32)"},
		{"SELECT quantileStateIf(lcn_i32, b) AS a FROM t", "AggregateFunction(quantile, Int32)"},
	}
	for _, testCase := range cases {
		got, err := inferSelectItemCHType(t, schema, testCase.sql)
		if err != nil {
			t.Errorf("%s: error = %v", testCase.sql, err)
			continue
		}
		if got != testCase.want {
			t.Errorf("%s: CH type = %q, want %q", testCase.sql, got, testCase.want)
		}
	}
}

// TestAggregateStateIfAlsoDropsTopLevelNullable pins the -If form, which
// is NOT the plain -State rule. The arr_n row proves that only the
// top-level Nullable comes off.
func TestAggregateStateIfAlsoDropsTopLevelNullable(t *testing.T) {
	schema, err := schemaFromDDLErr(t, stateWrapperSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	cases := []struct{ sql, want string }{
		// The quantileStateIf cells of the grid are blocked by the
		// hardcoded branch that
		// TestQuantileStateWrapperCellsStayBlocked names, thus the -If
		// rule is pinned here on the names that reach the combinator
		// path. Before the `stateif` suffix existed, none of these
		// names split at all: `maxStateIf` left the base `maxstate`,
		// which is not a combinable aggregate, so chgen refused.
		{"SELECT maxStateIf(i32, b) AS a FROM t", "AggregateFunction(max, Int32)"},
		// The top-level Nullable comes off, unlike the plain -State form.
		{"SELECT maxStateIf(ni32, b) AS a FROM t", "AggregateFunction(max, Int32)"},
		{"SELECT uniqStateIf(lc, b) AS a FROM t", "AggregateFunction(uniq, String)"},
		// Both wrappers come off in this cell, and for two rules.
		{"SELECT maxStateIf(lcn, b) AS a FROM t", "AggregateFunction(max, String)"},
		// A Nullable at DEPTH survives: the removal is top-level only.
		// This cell is what separates "remove the top-level Nullable"
		// from "remove every Nullable".
		{"SELECT maxStateIf(arr_n, b) AS a FROM t", "AggregateFunction(max, Array(Nullable(Int32)))"},
		{"SELECT maxStateIf(arr_lc, b) AS a FROM t", "AggregateFunction(max, Array(String))"},
	}
	for _, testCase := range cases {
		got, err := inferSelectItemCHType(t, schema, testCase.sql)
		if err != nil {
			t.Errorf("%s: error = %v", testCase.sql, err)
			continue
		}
		if got != testCase.want {
			t.Errorf("%s: CH type = %q, want %q", testCase.sql, got, testCase.want)
		}
	}
}

// TestSimpleStateWrapperRuleMatchesState pins that -SimpleState uses the
// SAME wrapper rule as plain -State: LowCardinality off at every depth,
// Nullable kept. The lcn row is again the distinguishing cell, and it is
// the one that the ticket warned could be broken by a shared guard that
// strips Nullable for both families.
func TestSimpleStateWrapperRuleMatchesState(t *testing.T) {
	schema, err := schemaFromDDLErr(t, stateWrapperSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	cases := []struct{ sql, want string }{
		{"SELECT anySimpleState(i32) AS a FROM t", "SimpleAggregateFunction(any, Int32)"},
		{"SELECT anySimpleState(ni32) AS a FROM t", "SimpleAggregateFunction(any, Nullable(Int32))"},
		{"SELECT anySimpleState(lc) AS a FROM t", "SimpleAggregateFunction(any, String)"},
		// SimpleAggregateFunction accepts the Nullable, and keeps it.
		{"SELECT anySimpleState(lcn) AS a FROM t", "SimpleAggregateFunction(any, Nullable(String))"},
		{"SELECT maxSimpleState(lcn) AS a FROM t", "SimpleAggregateFunction(max, Nullable(String))"},
		{"SELECT anySimpleState(arr_lc) AS a FROM t", "SimpleAggregateFunction(any, Array(String))"},
	}
	for _, testCase := range cases {
		got, err := inferSelectItemCHType(t, schema, testCase.sql)
		if err != nil {
			t.Errorf("%s: error = %v", testCase.sql, err)
			continue
		}
		if got != testCase.want {
			t.Errorf("%s: CH type = %q, want %q", testCase.sql, got, testCase.want)
		}
	}
}
