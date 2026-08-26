package engine

import "testing"

// This file pins the SimpleAggregateFunction transport of the String
// function family.
//
// The server does NOT treat the family as one group. Some String
// functions keep the SimpleAggregateFunction wrapper of their argument
// and some drop it, and the two groups both hold unary
// String-to-String functions. lower and trim are the clearest pair:
// both compute a new String, and only lower keeps the wrapper.
//
// A guess about the group is therefore worth nothing. The split is a
// property of the server implementation of each function, thus each
// name is measured on its own. Before this pin chgen answered the bare
// String for lower and upper, which is a silently wrong type.
//
// Both sides of the split are pinned together, so that a later blanket
// change cannot pass: a change that keeps the wrapper everywhere fails
// the drop cases, and a change that drops it everywhere fails the keep
// cases.
//
// Every expected value was measured on ClickHouse 25.8.29.51 through
// the HTTP interface, against real columns of a real table, never over
// literals, because the server folds constants. The keep group was
// measured by analysis (toTypeName) AND by execution
// (TSVWithNamesAndTypes over a real row), because toTypeName alone is
// analysis and not execution.
const simpleAggregateStringFamilySchema = `
CREATE TABLE t (
    saggs    SimpleAggregateFunction(min, String),
    saggmax  SimpleAggregateFunction(max, String),
    saggfs   SimpleAggregateFunction(min, FixedString(8)),
    saggns   SimpleAggregateFunction(min, Nullable(String)),
    s        String,
    lc       LowCardinality(String),
    ns       Nullable(String)
);
`

// TestStringFamilySimpleAggregateWrapperSplit pins which String
// functions keep the SimpleAggregateFunction wrapper and which drop it.
func TestStringFamilySimpleAggregateWrapperSplit(t *testing.T) {
	schema, err := schemaFromDDLErr(t, simpleAggregateStringFamilySchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	cases := []struct{ expr, want string }{
		// The KEEP group. The server gives the wrapper back with its
		// own aggregate-function parameter and its own inner type.
		{"lower(saggs)", "SimpleAggregateFunction(min, String)"},
		{"upper(saggs)", "SimpleAggregateFunction(min, String)"},

		// The aggregate-function parameter of the wrapper survives. It
		// is part of the type, thus a rebuild that lost it would give a
		// wrong type.
		{"lower(saggmax)", "SimpleAggregateFunction(max, String)"},
		{"upper(saggmax)", "SimpleAggregateFunction(max, String)"},

		// The inner type survives at its own width. lower and upper
		// keep a FixedString(N) argument at N.
		{"lower(saggfs)", "SimpleAggregateFunction(min, FixedString(8))"},
		{"upper(saggfs)", "SimpleAggregateFunction(min, FixedString(8))"},

		// NEIGHBOUR CELLS of the keep group. A Nullable INNER type
		// drops the wrapper, thus the keep is conditional and not
		// blanket. SimpleAggregateFunction ACCEPTS a Nullable inner
		// type, unlike AggregateFunction, so a HasPrefix guard on the
		// type name would answer this wrongly.
		{"lower(saggns)", "Nullable(String)"},
		{"upper(saggns)", "Nullable(String)"},

		// NEIGHBOUR CELLS of the keep group. With no wrapper to keep,
		// lower and upper are unchanged by this rule.
		{"lower(s)", "String"},
		{"upper(s)", "String"},
		{"lower(ns)", "Nullable(String)"},
		{"lower(lc)", "LowCardinality(String)"},

		// The DROP group. Each of these computes over the value and
		// gives the bare type back. trim against lower is the pair
		// that shows the split is not "value-preserving against
		// value-computing": both give a new String.
		{"trim(saggs)", "String"},
		{"trimLeft(saggs)", "String"},
		{"trimRight(saggs)", "String"},
		{"toString(saggs)", "String"},
		{"hex(saggs)", "String"},
		{"substring(saggs, 1, 2)", "String"},
		{"concat(saggs, s)", "String"},
		{"length(saggs)", "UInt64"},
		{"cityHash64(saggs)", "UInt64"},
	}
	for _, testCase := range cases {
		got, err := inferSelectItemCHType(t, schema, "SELECT "+testCase.expr+" AS a FROM t")
		if err != nil {
			t.Errorf("%s: error = %v, want %q", testCase.expr, err, testCase.want)
			continue
		}
		if got != testCase.want {
			t.Errorf("%s = %q, want %q", testCase.expr, got, testCase.want)
		}
	}
}
