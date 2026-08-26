package engine

import "testing"

// An aggregate combinator that READS its argument as a value and a
// SimpleAggregateFunction marker on that argument.
//
// Every answer below was measured on ClickHouse 25.8.29.51 over real
// columns of a real AggregatingMergeTree table with one row, in the probe
// database chgen_probe_lod, never over literals, because the server folds
// a constant and then reports a different wrapper.
//
// THE MEASURED RULE. A combinator that gives the ORDINARY result type of
// the base aggregate reads the argument as a VALUE. Thus a
// SimpleAggregateFunction(f, T) argument is first replaced by T, unless
// the marker survives the read. The marker survives a BARE SCALAR inner
// type only. That rule already exists in this repository as
// simpleAggregateMarkerSurvives and readSimpleAggregateValue, and this
// file adds no second copy of it.
//
// The columns:
//
//	saf       SimpleAggregateFunction(anyLast, Int32)
//	saflc     SimpleAggregateFunction(anyLast, LowCardinality(Int32))
//	safn      SimpleAggregateFunction(anyLast, Nullable(Int32))
//	saf_arr   SimpleAggregateFunction(anyLast, Array(Int32))
//	saf_tup   SimpleAggregateFunction(anyLast, Tuple(Int32, String))
//	saf_str   SimpleAggregateFunction(anyLast, String)
//
// The measured -SimpleState cells:
//
//	anySimpleState(saf)      SimpleAggregateFunction(any, SimpleAggregateFunction(anyLast, Int32))
//	anySimpleState(saflc)    SimpleAggregateFunction(any, Int32)
//	anySimpleState(safn)     SimpleAggregateFunction(any, Nullable(Int32))
//	anySimpleState(saf_str)  SimpleAggregateFunction(any, SimpleAggregateFunction(anyLast, String))
//	anySimpleState(saf_arr)  SimpleAggregateFunction(any, Array(Int32))
//	anySimpleState(saf_tup)  SimpleAggregateFunction(any, Tuple(Int32, String))
//
// THE DISTINGUISHING CELLS. Three candidate rules fit the two cells that
// the ticket names, and only these further cells separate them:
//
//   - "the server removes the LowCardinality inside the marker and keeps
//     the marker" is REFUTED by anySimpleState(saflc). The answer is a
//     bare Int32. The marker is GONE, not repaired.
//
//   - "the marker is always read through" is REFUTED by
//     anySimpleState(saf), which keeps the whole marker, and by
//     anySimpleState(saf_str), which keeps it over a String inner type.
//
//   - "the marker is dropped when the inner type carries a wrapper" is
//     REFUTED by anySimpleState(saf_arr) and anySimpleState(saf_tup).
//     Array(Int32) and Tuple(Int32, String) carry no Nullable and no
//     LowCardinality, and the marker is dropped for both. The test is on
//     the SHAPE of the inner type and not on its wrappers.
//
// The surviving rule is exactly simpleAggregateMarkerSurvives: the marker
// stays for a bare scalar inner type and goes for Nullable,
// LowCardinality, Array, Map and Tuple.
//
// -State IS A DIFFERENT CLASS AND MUST NOT CHANGE. A -State result stores
// the argument type verbatim; it does not read a value. Measured on the
// same server and the same table:
//
//	anyState(saf)    AggregateFunction(any, SimpleAggregateFunction(anyLast, Int32))
//	anyState(safn)   AggregateFunction(any, SimpleAggregateFunction(anyLast, Nullable(Int32)))
//	anyState(saflc)  AggregateFunction(any, Int32)
//
// anyState(safn) is the cell that proves the split. It KEEPS the marker
// around a Nullable inner type, where anySimpleState(safn) drops it. Thus
// the value read belongs to the ordinary-result path only, and a fix
// placed in the shared -State branch would be wrong here.

// combinatorMarkerSchema holds the marker alphabet. The inner types cover
// a bare scalar, a String, the two wrappers and two containers, so that
// the distinguishing cells above all have a column.
const combinatorMarkerSchema = `
CREATE TABLE t (
    k       UInt8,
    saf     SimpleAggregateFunction(anyLast, Int32),
    saflc   SimpleAggregateFunction(anyLast, LowCardinality(Int32)),
    safn    SimpleAggregateFunction(anyLast, Nullable(Int32)),
    saf_str SimpleAggregateFunction(anyLast, String),
    saf_arr SimpleAggregateFunction(anyLast, Array(Int32)),
    saf_tup SimpleAggregateFunction(anyLast, Tuple(Int32, String))
);
`

// TestSimpleStateReadsTheMarkerArgumentAsAValue pins the -SimpleState
// cells over the marker alphabet. The saflc and safn rows are the 16
// grid cells of the ticket. The saf_arr and saf_tup rows are the cells
// that refute a wrapper-shaped rule, and the saf and saf_str rows are
// the cells that must NOT move.
func TestSimpleStateReadsTheMarkerArgumentAsAValue(t *testing.T) {
	schema, err := schemaFromDDLErr(t, combinatorMarkerSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	cases := []struct{ sql, want string }{
		// The marker SURVIVES a bare scalar inner type. These cells
		// already agree with the server and must stay.
		{"SELECT anySimpleState(saf) AS a FROM t", "SimpleAggregateFunction(any, SimpleAggregateFunction(anyLast, Int32))"},
		{"SELECT maxSimpleState(saf) AS a FROM t", "SimpleAggregateFunction(max, SimpleAggregateFunction(anyLast, Int32))"},
		{"SELECT anySimpleState(saf_str) AS a FROM t", "SimpleAggregateFunction(any, SimpleAggregateFunction(anyLast, String))"},
		// The marker GOES for a LowCardinality inner type, and the
		// wrapper goes with it. The answer is a bare Int32.
		{"SELECT anySimpleState(saflc) AS a FROM t", "SimpleAggregateFunction(any, Int32)"},
		{"SELECT anyLastSimpleState(saflc) AS a FROM t", "SimpleAggregateFunction(anyLast, Int32)"},
		{"SELECT minSimpleState(saflc) AS a FROM t", "SimpleAggregateFunction(min, Int32)"},
		{"SELECT maxSimpleState(saflc) AS a FROM t", "SimpleAggregateFunction(max, Int32)"},
		// The marker GOES for a Nullable inner type, and the Nullable
		// STAYS. It moves outside the marker that is now absent.
		{"SELECT anySimpleState(safn) AS a FROM t", "SimpleAggregateFunction(any, Nullable(Int32))"},
		{"SELECT anyLastSimpleState(safn) AS a FROM t", "SimpleAggregateFunction(anyLast, Nullable(Int32))"},
		{"SELECT minSimpleState(safn) AS a FROM t", "SimpleAggregateFunction(min, Nullable(Int32))"},
		{"SELECT maxSimpleState(safn) AS a FROM t", "SimpleAggregateFunction(max, Nullable(Int32))"},
		// The distinguishing cells. Neither inner type carries a
		// wrapper, and the marker goes for both.
		{"SELECT anySimpleState(saf_arr) AS a FROM t", "SimpleAggregateFunction(any, Array(Int32))"},
		{"SELECT anySimpleState(saf_tup) AS a FROM t", "SimpleAggregateFunction(any, Tuple(Int32, String))"},
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

// TestPlainCombinatorsReadTheMarkerArgumentAsAValue pins the same value
// read for the other combinators that give the ordinary result type of
// the base aggregate. One rule serves all of them, thus a cell that moves
// in one form must move in every form.
//
// Measured on ClickHouse 25.8.29.51 over the same real columns:
//
//	any(saf)          SimpleAggregateFunction(anyLast, Int32)
//	any(saflc)        Int32
//	any(safn)         Nullable(Int32)
//	anyIf(saf, b)     SimpleAggregateFunction(anyLast, Int32)
//	anyIf(saflc, b)   Int32
//	anyIf(safn, b)    Nullable(Int32)
//	anyOrNull(saflc)  Nullable(Int32)
//	anyOrNull(saf)    Nullable(SimpleAggregateFunction(anyLast, Int32))
func TestPlainCombinatorsReadTheMarkerArgumentAsAValue(t *testing.T) {
	schema, err := schemaFromDDLErr(t, combinatorMarkerSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	cases := []struct{ sql, want string }{
		{"SELECT anyIf(saf, k > 0) AS a FROM t", "SimpleAggregateFunction(anyLast, Int32)"},
		{"SELECT anyIf(saflc, k > 0) AS a FROM t", "Int32"},
		{"SELECT anyIf(safn, k > 0) AS a FROM t", "Nullable(Int32)"},
		{"SELECT anyOrNull(saflc) AS a FROM t", "Nullable(Int32)"},
		{"SELECT anyOrNull(safn) AS a FROM t", "Nullable(Int32)"},
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

// TestAggregateStateKeepsTheMarkerArgument is the guard that the fix for
// the ordinary-result path did NOT reach the -State path.
//
// anyState(safn) keeps the marker around a Nullable inner type, where
// anySimpleState(safn) drops it. The two forms therefore need two
// different treatments of the same argument, and this test fails if the
// value read is ever moved into the shared -State branch.
//
// Measured on ClickHouse 25.8.29.51 over the same real columns.
func TestAggregateStateKeepsTheMarkerArgument(t *testing.T) {
	schema, err := schemaFromDDLErr(t, combinatorMarkerSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	cases := []struct{ sql, want string }{
		{"SELECT anyState(saf) AS a FROM t", "AggregateFunction(any, SimpleAggregateFunction(anyLast, Int32))"},
		{"SELECT anyState(safn) AS a FROM t", "AggregateFunction(any, SimpleAggregateFunction(anyLast, Nullable(Int32)))"},
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
