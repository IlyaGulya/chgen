package engine

import "testing"

// This file pins the transport of a SimpleAggregateFunction wrapper
// whose INNER type is Nullable.
//
// The rule has two opposite halves, thus a blanket rule is wrong in
// both directions:
//
//   - A value-preserving AGGREGATE over SimpleAggregateFunction(f, T)
//     KEEPS the wrapper.
//   - The same aggregate over SimpleAggregateFunction(f, Nullable(T))
//     LOSES the whole wrapper and gives the bare Nullable(T) back.
//
// Before this rule chgen answered
// SimpleAggregateFunction(sum, Nullable(Int64)) for max(saggn), which is
// a silently wrong type: the server answers Nullable(Int64).
//
// The split is on the inner type of the wrapper, NOT on the function
// name, and NOT on whether the RESULT is Nullable. A Nullable ordering
// argument makes the result Nullable while the wrapper stays, which the
// argMax cells below hold.
//
// Two neighbour families do NOT follow the aggregate rule and are
// pinned here so a later blanket change cannot pass:
//
//   - greatest and least are scalar, and they keep the wrapper with a
//     Nullable inner type.
//   - lagInFrame and leadInFrame keep it as well, although the
//     neighbour window functions first_value and last_value do not.
//
// Every expected value was measured on ClickHouse 25.8.29.51 through
// the HTTP interface, against real columns of a real table, never over
// literals, because the server folds constants. The types were read
// with toTypeName and the rows were also executed, because toTypeName
// is analysis and not execution.
const simpleAggregateNullableInnerSchema = `
CREATE TABLE t (
    sagg   SimpleAggregateFunction(sum, Int64),
    saggn  SimpleAggregateFunction(sum, Nullable(Int64)),
    saggs  SimpleAggregateFunction(min, String),
    saggsn SimpleAggregateFunction(min, Nullable(String)),
    i64    Int64,
    ni64   Nullable(Int64)
);
`

// TestSimpleAggregateNullableInnerUnwrapsForAggregates holds the whole
// measured grid: the Nullable-inner cells that unwrap AND the
// non-Nullable-inner cells that keep the wrapper. The two halves sit in
// one table on purpose, so a change that unwraps everywhere and a change
// that keeps everywhere both fail.
func TestSimpleAggregateNullableInnerUnwrapsForAggregates(t *testing.T) {
	schema, err := schemaFromDDLErr(t, simpleAggregateNullableInnerSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	cases := []struct{ expr, want string }{
		// A NON-Nullable inner type keeps the wrapper. These are the
		// cells that the fix must not disturb.
		{"max(sagg)", "SimpleAggregateFunction(sum, Int64)"},
		{"min(sagg)", "SimpleAggregateFunction(sum, Int64)"},
		{"any(sagg)", "SimpleAggregateFunction(sum, Int64)"},
		{"anyLast(sagg)", "SimpleAggregateFunction(sum, Int64)"},
		{"argMax(sagg, i64)", "SimpleAggregateFunction(sum, Int64)"},
		{"argMin(sagg, i64)", "SimpleAggregateFunction(sum, Int64)"},
		{"maxIf(sagg, i64 > 0)", "SimpleAggregateFunction(sum, Int64)"},
		{"minIf(sagg, i64 > 0)", "SimpleAggregateFunction(sum, Int64)"},
		{"argMaxIf(sagg, i64, i64 > 0)", "SimpleAggregateFunction(sum, Int64)"},
		{"argMinIf(sagg, i64, i64 > 0)", "SimpleAggregateFunction(sum, Int64)"},
		{"max(saggs)", "SimpleAggregateFunction(min, String)"},
		{"any(saggs)", "SimpleAggregateFunction(min, String)"},
		{"argMax(saggs, i64)", "SimpleAggregateFunction(min, String)"},

		// A Nullable inner type LOSES the whole wrapper. These are the
		// cells that the ticket reported.
		{"max(saggn)", "Nullable(Int64)"},
		{"min(saggn)", "Nullable(Int64)"},
		{"any(saggn)", "Nullable(Int64)"},
		{"anyLast(saggn)", "Nullable(Int64)"},
		{"argMax(saggn, i64)", "Nullable(Int64)"},
		{"argMin(saggn, i64)", "Nullable(Int64)"},
		{"maxIf(saggn, i64 > 0)", "Nullable(Int64)"},
		{"minIf(saggn, i64 > 0)", "Nullable(Int64)"},
		{"argMaxIf(saggn, i64, i64 > 0)", "Nullable(Int64)"},
		{"argMinIf(saggn, i64, i64 > 0)", "Nullable(Int64)"},
		{"max(saggsn)", "Nullable(String)"},
		{"any(saggsn)", "Nullable(String)"},
		{"argMax(saggsn, i64)", "Nullable(String)"},

		// NEIGHBOUR CELLS. A Nullable ORDERING argument puts a Nullable
		// OUTSIDE a wrapper that survives. The split is therefore on the
		// inner type of the wrapper and not on the nullability of the
		// result: these two cells differ only in the first argument.
		{"argMax(sagg, ni64)", "Nullable(SimpleAggregateFunction(sum, Int64))"},
		{"argMax(saggn, ni64)", "Nullable(Int64)"},
		{"argMin(saggs, ni64)", "Nullable(SimpleAggregateFunction(min, String))"},
		{"argMax(saggsn, ni64)", "Nullable(String)"},
		{"argMaxIf(sagg, ni64, i64 > 0)", "Nullable(SimpleAggregateFunction(sum, Int64))"},

		// NEIGHBOUR CELLS. A value-COMPUTING aggregate removes the
		// wrapper whatever the inner type is, thus these cells are the
		// contrast that shows the rule above is about the wrapper and
		// not about aggregates in general.
		{"sum(saggn)", "Nullable(Int64)"},
		{"sum(sagg)", "Int64"},
		{"avg(saggn)", "Nullable(Float64)"},
		{"avg(sagg)", "Float64"},
		{"count(saggn)", "UInt64"},
		{"uniq(saggn)", "UInt64"},

		// NEIGHBOUR CELLS. greatest and least are SCALAR, thus they keep
		// the wrapper although their class is the aggregate class.
		{"greatest(sagg)", "SimpleAggregateFunction(sum, Int64)"},
		{"greatest(saggn)", "SimpleAggregateFunction(sum, Nullable(Int64))"},
		{"least(saggn)", "SimpleAggregateFunction(sum, Nullable(Int64))"},
		{"least(saggsn)", "SimpleAggregateFunction(min, Nullable(String))"},
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

// TestSimpleAggregateNullableInnerWindowSplit holds the window half of
// the grid. The two window families disagree, thus one rule for all
// window functions would be wrong.
//
//	first_value and last_value  unwrap a Nullable inner type
//	lagInFrame and leadInFrame  keep the wrapper
func TestSimpleAggregateNullableInnerWindowSplit(t *testing.T) {
	schema, err := schemaFromDDLErr(t, simpleAggregateNullableInnerSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	cases := []struct{ expr, want string }{
		{"first_value(sagg)", "SimpleAggregateFunction(sum, Int64)"},
		{"first_value(saggs)", "SimpleAggregateFunction(min, String)"},
		{"first_value(saggn)", "Nullable(Int64)"},
		{"last_value(saggn)", "Nullable(Int64)"},
		{"first_value(saggsn)", "Nullable(String)"},
		{"last_value(saggsn)", "Nullable(String)"},

		{"lagInFrame(sagg, 1)", "SimpleAggregateFunction(sum, Int64)"},
		{"lagInFrame(saggn, 1)", "SimpleAggregateFunction(sum, Nullable(Int64))"},
		{"leadInFrame(saggn, 1)", "SimpleAggregateFunction(sum, Nullable(Int64))"},
		{"lagInFrame(saggsn, 1)", "SimpleAggregateFunction(min, Nullable(String))"},
	}
	for _, testCase := range cases {
		query := "SELECT " + testCase.expr + " OVER (ORDER BY i64) AS a FROM t"
		got, err := inferSelectItemCHType(t, schema, query)
		if err != nil {
			t.Errorf("%s: error = %v, want %q", testCase.expr, err, testCase.want)
			continue
		}
		if got != testCase.want {
			t.Errorf("%s = %q, want %q", testCase.expr, got, testCase.want)
		}
	}
}
