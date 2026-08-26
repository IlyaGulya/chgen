package engine

import "testing"

// This file pins what the four array functions arrayDistinct,
// arraySort, arraySlice and arrayResize do with the transport wrappers
// of their argument.
//
// The subject is the SimpleAggregateFunction marker. The server READS
// THROUGH the marker and answers the bare Array. Before the fix the four
// names had the class wrapperOpaque. That class gives the rule the
// WRAPPED type, and both rules here give the first argument back, thus
// the marker survived onto the result and chgen answered
// SimpleAggregateFunction(anyLast, Array(Int32)). That is a silently
// wrong type, which is the worst defect class.
//
// The fix is the CLASS, not a new rule. wrapperAggregate keeps the
// marker only over a BARE SCALAR inner type, and these names demand an
// Array argument, thus the condition always drops the marker here. See
// TestArrayFunctionsRefuseASimpleAggregateScalar for the cell that
// proves the bare-scalar case is unreachable.
//
// Every expected value was measured on ClickHouse 25.8.29.51 through the
// HTTP interface, against real columns of a real table, never over
// literals, because the server folds constants. The rows were also
// SELECTed and not only read with toTypeName, because toTypeName is
// analysis and not execution.
const arrayFunctionMarkerSchema = `
CREATE TABLE t (
    k    UInt8,
    saf  SimpleAggregateFunction(anyLast, Array(Int32)),
    safs SimpleAggregateFunction(anyLast, Int32),
    an   Array(Nullable(Int32)),
    alc  Array(LowCardinality(String))
);
`

// TestArrayFunctionsDropTheSimpleAggregateMarker holds the measured
// marker grid.
//
// Measured with saf = SimpleAggregateFunction(anyLast, Array(Int32))
// that held [3,1,1,2], reading the VALUE as well as the type:
//
//	arrayDistinct(saf)     Array(Int32)  [3,1,2]
//	arraySort(saf)         Array(Int32)  [1,1,2,3]
//	arraySlice(saf, 1, 2)  Array(Int32)  [3,1]
//	arrayResize(saf, 2)    Array(Int32)  [3,1]
//
// The four names change the value in four different ways. arrayDistinct
// changes the element set, arraySlice and arrayResize change the length,
// and arraySort reorders. Each name was measured on its own, and all
// four agree about the marker.
func TestArrayFunctionsDropTheSimpleAggregateMarker(t *testing.T) {
	schema, err := schemaFromDDLErr(t, arrayFunctionMarkerSchema)
	if err != nil {
		t.Fatalf("schema: %v", err)
	}
	cases := []struct {
		expr string
		want string
	}{
		{"arrayDistinct(saf)", "Array(Int32)"},
		{"arraySort(saf)", "Array(Int32)"},
		{"arraySlice(saf, 1, 2)", "Array(Int32)"},
		{"arrayResize(saf, 2)", "Array(Int32)"},
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

// TestArrayFunctionsKeepTheMeasuredElementRules holds the NEIGHBOUR
// cells that the class change must not disturb.
//
// arrayDistinct removes the top-level Nullable of the element, because
// it removes the duplicate elements and thereby removes the NULL from
// the data. Its three neighbours keep every element, thus they keep the
// Nullable. Measured with an = Array(Nullable(Int32)) that held
// [1, NULL, 1]:
//
//	arrayDistinct(an)     Array(Int32)            [1]
//	arraySort(an)         Array(Nullable(Int32))  [1,1,NULL]
//	arraySlice(an, 1, 2)  Array(Nullable(Int32))  [1,NULL]
//	arrayResize(an, 2)    Array(Nullable(Int32))  [1,NULL]
//
// All four remove a LowCardinality below the top level. Measured with
// alc = Array(LowCardinality(String)) that held ['b','a','a']: every one
// of the four gives Array(String). The class supplies that removal
// through its stripNested field.
func TestArrayFunctionsKeepTheMeasuredElementRules(t *testing.T) {
	schema, err := schemaFromDDLErr(t, arrayFunctionMarkerSchema)
	if err != nil {
		t.Fatalf("schema: %v", err)
	}
	cases := []struct {
		expr string
		want string
	}{
		{"arrayDistinct(an)", "Array(Int32)"},
		{"arraySort(an)", "Array(Nullable(Int32))"},
		{"arraySlice(an, 1, 2)", "Array(Nullable(Int32))"},
		{"arrayResize(an, 2)", "Array(Nullable(Int32))"},
		{"arrayDistinct(alc)", "Array(String)"},
		{"arraySort(alc)", "Array(String)"},
		{"arraySlice(alc, 1, 2)", "Array(String)"},
		{"arrayResize(alc, 2)", "Array(String)"},
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

// TestArrayFunctionsRefuseASimpleAggregateScalar is the cell that
// SEPARATES the two candidate rules.
//
// Candidate A is the existing rule of the regression: the marker survives
// only over a bare scalar inner type. Candidate B would be a new rule
// that said these four names always drop the marker. The two give the
// SAME answer for every REACHABLE argument, thus no array cell can tell
// them apart. Only the bare-scalar inner separates them, and it is
// unreachable, because these names demand an Array.
//
// The server refuses it with Code 43, which is type evidence. Measured
// with safs = SimpleAggregateFunction(anyLast, Int32):
//
//	arrayDistinct(safs)      Code 43, argument must be array
//	arraySort(safs)          Code 43, argument must be array
//	arraySlice(safs, 1, 2)   Code 43, argument must be an array
//	arrayResize(safs, 2)     Code 43, expected Array
//
// chgen must refuse it too. This is why candidate A is sufficient and
// candidate B would be a second rule for no gain.
func TestArrayFunctionsRefuseASimpleAggregateScalar(t *testing.T) {
	schema, err := schemaFromDDLErr(t, arrayFunctionMarkerSchema)
	if err != nil {
		t.Fatalf("schema: %v", err)
	}
	for _, expr := range []string{
		"arrayDistinct(safs)",
		"arraySort(safs)",
		"arraySlice(safs, 1, 2)",
		"arrayResize(safs, 2)",
	} {
		got, err := inferSelectItemCHType(t, schema, "SELECT "+expr+" AS a FROM t")
		if err == nil {
			t.Errorf("%s = %q, but the server answers Code 43", expr, got)
		}
	}
}
