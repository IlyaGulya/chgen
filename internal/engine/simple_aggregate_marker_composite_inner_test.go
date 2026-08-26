package engine

import "testing"

// This file pins the SimpleAggregateFunction marker rule for the two
// transports of wrapper_transport.go that read the marker as a
// VALUE: the value-preserving aggregate transport and the case-folding
// transport of lower and upper.
//
// the regression found the defect while it measured a different function
// family, and it recorded the cells in
// simple_aggregate_marker_value_read_test.go. Both conditions
// tested the inner NULLABILITY only, thus both kept the marker over a
// COMPOSITE inner type and gave a silently wrong type.
//
// THE CELLS THAT DISTINGUISH THE CANDIDATE RULES. Two rules are on
// offer. Rule N says the marker survives when the inner type is not
// Nullable. Rule S says the marker survives only when the inner type is
// a BARE SCALAR. They agree on every scalar inner and on every Nullable
// inner, thus a measurement taken there decides nothing. They disagree
// on a NON-Nullable COMPOSITE inner, and that is where the cells below
// are taken:
//
//	                inner type            rule N   rule S   server
//	max(safarrb)    Array(Int32)          keeps    drops    drops
//	max(safmapb)    Map(String, Int32)    keeps    drops    drops
//	max(saftup)     Tuple(Int32, Int32)   keeps    drops    drops
//	max(saflc)      LowCardinality(String) keeps   drops    drops
//
// The server answers the inner type with no marker, thus rule S is the
// measured rule and rule N is wrong.
//
// THE TWO TRANSPORTS DIFFER, AND THE DIFFERENCE IS MEASURED. They are
// separate functions on purpose, and the LowCardinality cell proves the
// separation is real:
//
//	max(saflc)      String                  aggregate: also strips LC
//	lower(saflc)    LowCardinality(String)  case folding: KEEPS the LC
//
// Both drop the marker. Only the aggregate strips the LowCardinality
// that the marker was hiding, because every true aggregate removes
// LowCardinality at every depth. lower and upper are scalar and keep it.
// A single shared condition would be right about the marker and wrong
// about the LowCardinality, thus the two transports keep their own
// dispositions and share only the marker helper.
//
// THE CASE-FOLDING FAMILY HAS ONLY ONE ACCEPTED COMPOSITE INNER. lower
// and upper take text, thus the server REFUSES an Array, Map or Tuple
// inner with Code 43, ILLEGAL_TYPE_OF_ARGUMENT. Code 43 is type
// evidence, but it is evidence about the ARGUMENT DOMAIN and not about
// the marker, thus those cells cannot decide the marker rule and they
// are not used here. The LowCardinality inner is the one composite that
// the family accepts, and it decides the rule on its own.
//
// Every expected value below was measured on ClickHouse 25.8.29.51
// through the HTTP interface, against real columns of a real
// AggregatingMergeTree table with a row inserted, never over literals,
// because the server folds constants. The shapes were SELECTed as well
// as read with toTypeName, because toTypeName is analysis and not
// execution:
//
//	SELECT max(safarrb) FROM t   =>  [1]
//	SELECT max(saftup)  FROM t   =>  (1,2)
//	SELECT max(saflc)   FROM t   =>  a
//	SELECT lower(saflc) FROM t   =>  a
const simpleAggregateMarkerCompositeInnerSchema = `
CREATE TABLE t (
    saf     SimpleAggregateFunction(anyLast, Int32),
    safs    SimpleAggregateFunction(anyLast, String),
    saffs   SimpleAggregateFunction(anyLast, FixedString(4)),
    safn    SimpleAggregateFunction(anyLast, Nullable(Int32)),
    safns   SimpleAggregateFunction(anyLast, Nullable(String)),
    safarr  SimpleAggregateFunction(anyLast, Array(Nullable(Int32))),
    safarrb SimpleAggregateFunction(anyLast, Array(Int32)),
    safarrs SimpleAggregateFunction(anyLast, Array(String)),
    safmap  SimpleAggregateFunction(anyLast, Map(String, Nullable(Int32))),
    safmapb SimpleAggregateFunction(anyLast, Map(String, Int32)),
    saftup  SimpleAggregateFunction(anyLast, Tuple(Int32, Int32)),
    saflc   SimpleAggregateFunction(anyLast, LowCardinality(String)),
    saflcfs SimpleAggregateFunction(anyLast, LowCardinality(FixedString(8))),
    saflcn  SimpleAggregateFunction(anyLast, LowCardinality(Nullable(String))),
    i32     Int32,
    s       String
);
`

// TestValuePreservingAggregateDropsMarkerOverCompositeInner holds the
// measured grid for the aggregate transport.
func TestValuePreservingAggregateDropsMarkerOverCompositeInner(t *testing.T) {
	schema, err := schemaFromDDLErr(t, simpleAggregateMarkerCompositeInnerSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	cases := []struct{ expr, want string }{
		// A BARE SCALAR inner type KEEPS the marker. These cells are
		// the control: they are already correct today, and the fix must
		// not move them.
		{"max(saf)", "SimpleAggregateFunction(anyLast, Int32)"},
		{"max(safs)", "SimpleAggregateFunction(anyLast, String)"},
		{"max(saffs)", "SimpleAggregateFunction(anyLast, FixedString(4))"},
		{"min(saf)", "SimpleAggregateFunction(anyLast, Int32)"},
		{"any(saf)", "SimpleAggregateFunction(anyLast, Int32)"},
		{"anyLast(safs)", "SimpleAggregateFunction(anyLast, String)"},
		{"first_value(saf)", "SimpleAggregateFunction(anyLast, Int32)"},
		{"last_value(saf)", "SimpleAggregateFunction(anyLast, Int32)"},
		{"argMax(saf, i32)", "SimpleAggregateFunction(anyLast, Int32)"},
		{"argMax(safs, i32)", "SimpleAggregateFunction(anyLast, String)"},

		// A NULLABLE inner type loses the marker. These cells are also
		// already correct today: the old condition and the measured
		// rule AGREE here, which is exactly why a measurement taken
		// here proves nothing.
		{"max(safn)", "Nullable(Int32)"},
		{"max(safns)", "Nullable(String)"},
		{"argMax(safn, i32)", "Nullable(Int32)"},

		// THE DISTINGUISHING CELLS. A NON-Nullable COMPOSITE inner type
		// loses the marker although nothing at the marker's own level
		// is Nullable. The old condition keeps the marker here.
		{"max(safarrb)", "Array(Int32)"},
		{"max(safarrs)", "Array(String)"},
		{"max(safmapb)", "Map(String, Int32)"},
		{"max(saftup)", "Tuple(Int32, Int32)"},
		{"min(safarrb)", "Array(Int32)"},
		{"any(safarrb)", "Array(Int32)"},
		{"anyLast(safarrb)", "Array(Int32)"},
		{"first_value(safarrb)", "Array(Int32)"},
		{"last_value(safarrb)", "Array(Int32)"},
		{"argMax(safarrb, i32)", "Array(Int32)"},
		{"argMax(safmapb, i32)", "Map(String, Int32)"},
		{"argMax(saftup, i32)", "Tuple(Int32, Int32)"},

		// A composite inner that ALSO holds a Nullable one level down.
		// The Nullable is a property of the member and it stays.
		{"max(safarr)", "Array(Nullable(Int32))"},
		{"max(safmap)", "Map(String, Nullable(Int32))"},

		// A LowCardinality inner. The marker drops, and then the
		// aggregate strips the LowCardinality that the marker hid,
		// because every true aggregate removes LowCardinality at every
		// depth. This cell needs BOTH rules at once.
		{"max(saflc)", "String"},
		{"min(saflc)", "String"},
		{"argMax(saflc, i32)", "String"},
		// The same, with a Nullable under the LowCardinality. The
		// LowCardinality goes and the Nullable stays.
		{"max(saflcn)", "Nullable(String)"},
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

// TestCaseFoldingDropsMarkerOverCompositeInner holds the measured grid
// for the case-folding transport of lower and upper.
//
// The family accepts exactly one composite inner type, LowCardinality,
// thus that cell alone separates the two candidate rules for this
// transport. It separates them cleanly: the inner type is NOT Nullable,
// so rule N predicts a kept marker, and the server answers
// LowCardinality(String) with no marker.
func TestCaseFoldingDropsMarkerOverCompositeInner(t *testing.T) {
	schema, err := schemaFromDDLErr(t, simpleAggregateMarkerCompositeInnerSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	cases := []struct{ expr, want string }{
		// Control cells. A bare scalar inner keeps the marker with its
		// exact aggregate-function parameter and its exact inner type.
		{"lower(safs)", "SimpleAggregateFunction(anyLast, String)"},
		{"upper(safs)", "SimpleAggregateFunction(anyLast, String)"},
		{"lower(saffs)", "SimpleAggregateFunction(anyLast, FixedString(4))"},
		{"upper(saffs)", "SimpleAggregateFunction(anyLast, FixedString(4))"},

		// Control cells. A Nullable inner drops the marker. The old
		// condition already answers these.
		{"lower(safns)", "Nullable(String)"},
		{"upper(safns)", "Nullable(String)"},

		// THE DISTINGUISHING CELLS. The marker drops over a
		// LowCardinality inner, and unlike the aggregate transport the
		// LowCardinality itself is KEPT, because lower and upper are
		// scalar functions on the argsFirstOnly path.
		{"lower(saflc)", "LowCardinality(String)"},
		{"upper(saflc)", "LowCardinality(String)"},
		{"lower(saflcn)", "LowCardinality(Nullable(String))"},
		{"upper(saflcn)", "LowCardinality(Nullable(String))"},

		// The FixedString member of the same cell. It is separate,
		// because the rule of lower and upper reads the NAME of its
		// base type to decide between FixedString and String. While the
		// split left the inner LowCardinality on the base, that name
		// test could not match and the answer collapsed to String. This
		// cell is the witness that the split now lifts an inner
		// LowCardinality into the stack. Measured:
		//
		//	lower(saflcfs)  LowCardinality(FixedString(8))
		{"lower(saflcfs)", "LowCardinality(FixedString(8))"},
		{"upper(saflcfs)", "LowCardinality(FixedString(8))"},
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
