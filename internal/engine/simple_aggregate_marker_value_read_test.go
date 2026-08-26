package engine

import "testing"

// This file pins WHEN a SimpleAggregateFunction(f, X) marker survives a
// function that READS the argument as a value, and when it does not.
//
// The related regressions both named a rule that the
// measurement does NOT support, thus the rule below is derived from the
// server and not from the ticket text.
//
// the regression said the rule is "look through the marker by one level",
// unconditionally, and it pointed at assumeNotNull(safarr), which gives
// Array(Nullable(Int32)) although the value is not Nullable at the
// marker's own level. That cell is real. It is not the whole rule: the
// neighbour cell assumeNotNull(saf) gives
// SimpleAggregateFunction(anyLast, Int32) and KEEPS the marker, and saf
// has a non-Nullable inner type exactly as safarr does. An
// unconditional look-through therefore contradicts the measurement.
//
// THE MEASURED RULE. A function that reads the marker as a value keeps
// the marker only when the inner type X is a BARE SCALAR. When X is
// Nullable, LowCardinality, Array, Map or Tuple, the server answers X
// itself with no marker at all.
//
// The cells that DISTINGUISH this rule from the two candidates:
//
//	                    inner X            server answer          keeps?
//	saf      Int32                  SimpleAggregateFunction(...)   yes
//	safarrb  Array(Int32)           Array(Int32)                   no
//
// Both inner types are non-Nullable. A rule that reads nullability alone
// predicts "keeps" for both, and is wrong on safarrb. A rule that looks
// through unconditionally predicts "drops" for both, and is wrong on
// saf. Only the scalar/composite split predicts both.
//
// THE RULE IS NOT A PROPERTY OF assumeNotNull OR groupArray. identity
// applies no type rule of its own and shows the same split, thus the
// strip happens when the argument is READ, before the rule runs:
//
//	identity(saf)      SimpleAggregateFunction(anyLast, Int32)
//	identity(safarrb)  Array(Int32)
//
// Four independent witnesses agree on the same split: identity, any,
// assumeNotNull and groupArray. The container CONSTRUCTORS array and
// tuple are a different class and keep the marker for EVERY inner shape,
// which is why the split must not go into splitCHWrappers.
//
// Every expected value below was measured on ClickHouse 25.8.29.51
// through the HTTP interface, against real columns of a real
// AggregatingMergeTree table, never over literals, because the server
// folds constants. The shapes were SELECTed as well as read with
// toTypeName, because toTypeName is analysis and not execution.
const simpleAggregateMarkerValueReadSchema = `
CREATE TABLE t (
    saf     SimpleAggregateFunction(anyLast, Int32),
    safs    SimpleAggregateFunction(anyLast, String),
    safd    SimpleAggregateFunction(anyLast, Decimal(18,4)),
    safdt   SimpleAggregateFunction(anyLast, DateTime),
    saffs   SimpleAggregateFunction(anyLast, FixedString(4)),
    safuuid SimpleAggregateFunction(anyLast, UUID),
    safn    SimpleAggregateFunction(anyLast, Nullable(Int32)),
    safns   SimpleAggregateFunction(anyLast, Nullable(String)),
    safarr  SimpleAggregateFunction(anyLast, Array(Nullable(Int32))),
    safarrb SimpleAggregateFunction(anyLast, Array(Int32)),
    safarrs SimpleAggregateFunction(anyLast, Array(String)),
    safmap  SimpleAggregateFunction(anyLast, Map(String, Nullable(Int32))),
    safmapb SimpleAggregateFunction(anyLast, Map(String, Int32)),
    saftup  SimpleAggregateFunction(anyLast, Tuple(Int32, Int32)),
    saflc   SimpleAggregateFunction(anyLast, LowCardinality(String)),
    saflcn  SimpleAggregateFunction(anyLast, LowCardinality(Nullable(String))),
    i32     Int32,
    ni32    Nullable(Int32),
    lc      LowCardinality(String),
    lcn     LowCardinality(Nullable(String)),
    lcn_i32 LowCardinality(Nullable(Int32)),
    s       String
);
`

// TestSimpleAggregateMarkerSurvivesOnlyAScalarInner holds the measured
// grid for the regression. Each block names the witness that measured it.
func TestSimpleAggregateMarkerSurvivesOnlyAScalarInner(t *testing.T) {
	schema, err := schemaFromDDLErr(t, simpleAggregateMarkerValueReadSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	cases := []struct{ expr, want string }{
		// assumeNotNull. A BARE SCALAR inner type keeps the marker,
		// because assumeNotNull finds no Nullable to remove.
		{"assumeNotNull(saf)", "SimpleAggregateFunction(anyLast, Int32)"},
		{"assumeNotNull(safs)", "SimpleAggregateFunction(anyLast, String)"},
		{"assumeNotNull(safd)", "SimpleAggregateFunction(anyLast, Decimal(18, 4))"},
		{"assumeNotNull(safdt)", "SimpleAggregateFunction(anyLast, DateTime)"},
		{"assumeNotNull(saffs)", "SimpleAggregateFunction(anyLast, FixedString(4))"},
		{"assumeNotNull(safuuid)", "SimpleAggregateFunction(anyLast, UUID)"},

		// assumeNotNull. A NULLABLE inner type loses the marker AND the
		// Nullable. These are the two cells the grid reports as
		// MISMATCH today.
		{"assumeNotNull(safn)", "Int32"},
		{"assumeNotNull(safns)", "String"},

		// assumeNotNull. A COMPOSITE inner type loses the marker even
		// though there is no Nullable at the marker's own level. These
		// are the DISTINGUISHING cells: a nullability rule keeps the
		// marker here and is wrong.
		{"assumeNotNull(safarr)", "Array(Nullable(Int32))"},
		{"assumeNotNull(safarrb)", "Array(Int32)"},
		{"assumeNotNull(safarrs)", "Array(String)"},
		{"assumeNotNull(safmap)", "Map(String, Nullable(Int32))"},
		{"assumeNotNull(safmapb)", "Map(String, Int32)"},
		{"assumeNotNull(saftup)", "Tuple(Int32, Int32)"},
		{"assumeNotNull(saflc)", "LowCardinality(String)"},
		// The inner LowCardinality(Nullable(String)) loses the marker
		// and then assumeNotNull removes the Nullable from INSIDE the
		// LowCardinality. This one cell needs both rules at once.
		{"assumeNotNull(saflcn)", "LowCardinality(String)"},

		// the regression. assumeNotNull enters LowCardinality and removes the
		// Nullable that sits inside it. The wrapper itself stays.
		{"assumeNotNull(lcn)", "LowCardinality(String)"},
		{"assumeNotNull(lcn_i32)", "LowCardinality(Int32)"},
		// NEIGHBOUR CELLS for the regression. Nothing to remove, no change.
		{"assumeNotNull(lc)", "LowCardinality(String)"},
		{"assumeNotNull(i32)", "Int32"},
		{"assumeNotNull(ni32)", "Int32"},

		// groupArray. The same split, one level down inside the Array.
		{"groupArray(saf)", "Array(SimpleAggregateFunction(anyLast, Int32))"},
		{"groupArray(safs)", "Array(SimpleAggregateFunction(anyLast, String))"},
		{"groupArray(safn)", "Array(Int32)"},
		{"groupArray(safns)", "Array(String)"},
		{"groupArray(safarr)", "Array(Array(Nullable(Int32)))"},
		{"groupArray(safarrb)", "Array(Array(Int32))"},
		{"groupArray(safmapb)", "Array(Map(String, Int32))"},
		{"groupArray(saftup)", "Array(Tuple(Int32, Int32))"},
		// groupArray is an aggregate, thus it also removes every
		// LowCardinality wrapper. The marker goes first, then the
		// LowCardinality.
		{"groupArray(saflc)", "Array(String)"},
		{"groupArray(saflcn)", "Array(String)"},
		{"groupArray(lc)", "Array(String)"},
		{"groupArray(lcn_i32)", "Array(Int32)"},
		{"groupArray(ni32)", "Array(Int32)"},

		// The whole groupArray family shares the rule.
		{"groupUniqArray(safn)", "Array(Int32)"},
		{"groupUniqArray(saf)", "Array(SimpleAggregateFunction(anyLast, Int32))"},
		{"groupUniqArray(safarrb)", "Array(Array(Int32))"},

		// NEIGHBOUR CELLS. The container CONSTRUCTORS are a different
		// class: they keep the marker for EVERY inner shape. A fix that
		// puts the split into the shared wrapper code breaks these.
		{"array(saf)", "Array(SimpleAggregateFunction(anyLast, Int32))"},
		{"array(safn)", "Array(SimpleAggregateFunction(anyLast, Nullable(Int32)))"},
		{"array(safarrb)", "Array(SimpleAggregateFunction(anyLast, Array(Int32)))"},
		{"array(saftup)", "Array(SimpleAggregateFunction(anyLast, Tuple(Int32, Int32)))"},
		{"array(saflc)", "Array(SimpleAggregateFunction(anyLast, LowCardinality(String)))"},
		{"tuple(saf, i32)", "Tuple(SimpleAggregateFunction(anyLast, Int32), Int32)"},
		{"tuple(safarrb, i32)", "Tuple(SimpleAggregateFunction(anyLast, Array(Int32)), Int32)"},
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

// THE DEFECT THAT THIS NOTE ONCE RECORDED IS FIXED. The note stays to
// keep the history readable, and it now states the CURRENT code.
//
// The two conditions in wrapper_transport.go
//
//	aggregateKeepsSimpleAggregateCondition
//	caseFoldingKeepsSimpleAggregateCondition
//
// returned !innerNullable until the regression. That test keeps the marker
// over a COMPOSITE inner type, where the server drops it, thus it gave a
// silently wrong type. Both conditions now call
// callKeepsSimpleAggregateMarker, which applies the measured rule
// simpleAggregateMarkerSurvives from supertype.go. The rule therefore
// has ONE statement in the package.
//
// the regression re-measured the cells on ClickHouse 25.8.29.51, with real
// columns of a real AggregatingMergeTree table that holds one row:
//
//	max(safarr)          Array(Int32)
//	max(saftup)          Tuple(Int32, String)
//	max(safmap)          Map(String, Int32)
//	max(saflcs)          String
//	anyLast(safarr)      Array(Int32)
//	argMax(saftup, i32)  Tuple(Int32, String)
//	max(safn)            Nullable(Int32)
//	lower(safsn)         Nullable(String)
//
// The last two cells decide WHICH of the two measured marker rules these
// conditions obey. Both routes DROP the marker over a Nullable inner,
// thus both obey the value-READ rule simpleAggregateMarkerSurvives and
// NOT simpleAggregateMarkerSurvivesValuePreserving, which keeps the
// marker there. The value-preserving rule belongs to greatest, least,
// lagInFrame, leadInFrame and nullIf only.
//
// TestValuePreservingAggregateDropsMarkerOverCompositeInner and
// TestCaseFoldingDropsMarkerOverCompositeInner in
// simple_aggregate_marker_composite_inner_test.go hold these cells. Both
// FAIL if a condition goes back to the nullability test, thus they are
// defect reproducers and not only guards.
//
// The wrapper grid still cannot see these cells, because its closed
// alphabet gives the marker a SCALAR inner type. That is a boundary of
// the instrument and not evidence of health.
