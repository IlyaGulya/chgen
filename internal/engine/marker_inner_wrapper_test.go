package engine

import "testing"

// This file pins the rule that a transport wrapper INSIDE a
// SimpleAggregateFunction marker must change the answer exactly as the
// same wrapper OUTSIDE the marker does.
//
// The marker is a merge hint on the storage. It does not change what the
// value IS. A rule that reads the wrappers ON the type therefore misses
// a wrapper that lives in the inner type, and the result loses a wrapper
// that the server keeps. That is a silently wrong type, which is the
// worst defect class.
//
// Two independent routes had the gap, and each one needed its own
// correction:
//
//   - The DateTime constructors, which run before the registry and hold
//     their own wrapper logic in inferTimezoneCarryingType.
//   - nullIf, which owns its marker through wrapperValuePreservingRules.
//
// Every expected answer below was measured on ClickHouse 25.8.29.51
// through the HTTP interface, against real columns of a real
// AggregatingMergeTree table with one inserted row, never over literals,
// because the server folds constants.

// markerInnerWrapperSchema gives the columns that this file needs. The
// names follow the grid fixture: saf is a marker over a bare scalar,
// saflc is a marker over a LowCardinality inner type, safn is a marker
// over a Nullable inner type.
func markerInnerWrapperSchema(t *testing.T) *Schema {
	t.Helper()
	schema, err := schemaFromDDLErr(t, `CREATE TABLE t (
    i32    Int32,
    lci32  LowCardinality(Int32),
    saf    SimpleAggregateFunction(anyLast, Int32),
    safn   SimpleAggregateFunction(anyLast, Nullable(Int32)),
    saflc  SimpleAggregateFunction(anyLast, LowCardinality(Int32)),
    saflcd SimpleAggregateFunction(anyLast, LowCardinality(Date)),
    safs   SimpleAggregateFunction(anyLast, String),
    saflcs SimpleAggregateFunction(anyLast, LowCardinality(String))
) ENGINE = MergeTree ORDER BY tuple();`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	return schema
}

// TestDateTimeConstructorsKeepLowCardinalityInsideMarker pins the regression.
//
// The DateTime constructors read the value inside the marker. A
// LowCardinality inner type therefore reaches the result, exactly as a
// plain LowCardinality column does.
//
// Measured on ClickHouse 25.8.29.51:
//
//	toDateTime(saflc)      LowCardinality(DateTime)
//	toDateTime(lci32)      LowCardinality(DateTime)
//	toStartOfDay(saflcd)   LowCardinality(DateTime)
//	toDateTime(saf)        DateTime
//
// The marker column and the plain LowCardinality column agree in every
// cell, thus the marker must never change the answer.
//
// toStartOfDay does NOT belong in this test with an Int32-based column.
// It reads its argument as a date or a time and refuses every other base
// type, while toDateTime reads the same Int32 as a Unix timestamp and
// gives it a type. saflc, lci32 and saf are all Int32-based, so
// toStartOfDay refuses all three with Code: 43; see
// TestToStartOfDayRefusesNonDateInsideMarker for that half, pinned
// separately with both a marker column and its plain-wrapper witness.
func TestDateTimeConstructorsKeepLowCardinalityInsideMarker(t *testing.T) {
	schema := markerInnerWrapperSchema(t)
	for _, testCase := range []struct {
		expr string
		want string
	}{
		// The marker over a LowCardinality inner type.
		{"toDateTime(saflc)", "LowCardinality(DateTime)"},
		{"toStartOfDay(saflcd)", "LowCardinality(DateTime)"},

		// The plain LowCardinality column gives the same answer. This
		// is the witness that the marker is not allowed to change it.
		{"toDateTime(lci32)", "LowCardinality(DateTime)"},

		// A BARE inner type terminates the search, thus the result
		// stays bare. The rule is not an unconditional wrap.
		{"toDateTime(saf)", "DateTime"},
		{"toDateTime(i32)", "DateTime"},

		// A Nullable inner type still makes the result Nullable. This
		// route already read through the marker for Nullable; keep it
		// pinned so the LowCardinality correction cannot break it.
		{"toDateTime(safn)", "Nullable(DateTime)"},
	} {
		got := inferCHTypeString(t, schema, testCase.expr)
		if got != testCase.want {
			t.Errorf("inferCHTypeString(%q) = %q, want %q", testCase.expr, got, testCase.want)
		}
	}
}

// TestToStartOfDayRefusesNonDateInsideMarker pins the regression.
//
// toStartOfDay owns the narrower dateArgumentDomain: it accepts Date,
// Date32, DateTime and DateTime64 only, and refuses every other base
// type with Code: 43, "Illegal type <T> of argument of function
// toStartOfDay. Should be Date, Date32, DateTime or DateTime64". This
// holds whether the Int32 sits bare, under a LowCardinality wrapper, or
// inside a SimpleAggregateFunction marker over either shape.
//
// Measured on ClickHouse 25.8.29.51 with a real AggregatingMergeTree
// table, never over literals, because the server folds constants:
//
//	toStartOfDay(i32)     Code: 43
//	toStartOfDay(lci32)   Code: 43
//	toStartOfDay(saf)     Code: 43
//	toStartOfDay(saflc)   Code: 43
//
// The marker column and the plain LowCardinality column refuse alike,
// which is the witness that the marker does not open a path around the
// domain check.
func TestToStartOfDayRefusesNonDateInsideMarker(t *testing.T) {
	schema := markerInnerWrapperSchema(t)
	for _, expr := range []string{
		"toStartOfDay(i32)",
		"toStartOfDay(lci32)",
		"toStartOfDay(saf)",
		"toStartOfDay(saflc)",
	} {
		// inferCHTypeError fails the test itself when inference
		// succeeds, so calling it is the assertion.
		_ = inferCHTypeError(t, schema, expr)
	}
}

// TestToDateTime64DropsLowCardinality pins the one name of the same
// switch that does NOT keep the wrapper.
//
// Measured on ClickHouse 25.8.29.51:
//
//	toDateTime64(saflc, 3)  DateTime64(3)
//	toDateTime64(lci32, 3)  DateTime64(3)
//	toDateTime64(lcdt, 3)   DateTime64(3)
//
// The marker column and the plain LowCardinality column agree here too,
// and both drop the wrapper. This is a property of toDateTime64 and not
// a gap in the marker rule. Without this test a later change could
// "correct" toDateTime64 to keep a wrapper that the server removes.
func TestToDateTime64DropsLowCardinality(t *testing.T) {
	schema := markerInnerWrapperSchema(t)
	for _, testCase := range []struct {
		expr string
		want string
	}{
		{"toDateTime64(saflc, 3)", "DateTime64(3)"},
		{"toDateTime64(lci32, 3)", "DateTime64(3)"},
		{"toDateTime64(saf, 3)", "DateTime64(3)"},
	} {
		got := inferCHTypeString(t, schema, testCase.expr)
		if got != testCase.want {
			t.Errorf("inferCHTypeString(%q) = %q, want %q", testCase.expr, got, testCase.want)
		}
	}
}

// TestNullIfMarkerWithLowCardinalityInner pins the regression.
//
// nullIf gives its first argument back, thus it keeps the
// SimpleAggregateFunction marker. It keeps the marker only when the
// inner type is NOT LowCardinality. Over a LowCardinality inner type the
// server drops the marker and answers about the value alone.
//
// Measured on ClickHouse 25.8.29.51 over two type families, so that the
// rule is not read off one column:
//
//	nullIf(saflc, saflc)    Nullable(Int32)
//	nullIf(saflcs, saflcs)  Nullable(String)
//	nullIf(saf, saf)        Nullable(SimpleAggregateFunction(anyLast, Int32))
//	nullIf(safs, safs)      Nullable(SimpleAggregateFunction(anyLast, String))
//
// The plain LowCardinality column agrees with the marker column:
//
//	nullIf(lci32, lci32)    Nullable(Int32)
//
// The LowCardinality survives only under the ordinary transparent rule,
// which needs every other argument to be a constant:
//
//	nullIf(saflc, 1)        LowCardinality(Nullable(Int32))
//	nullIf(lci32, 1)        LowCardinality(Nullable(Int32))
func TestNullIfMarkerWithLowCardinalityInner(t *testing.T) {
	schema := markerInnerWrapperSchema(t)
	for _, testCase := range []struct {
		expr string
		want string
	}{
		// The marker goes away over a LowCardinality inner type, and
		// the LowCardinality goes away too, because the second
		// argument is not a constant.
		{"nullIf(saflc, saflc)", "Nullable(Int32)"},
		{"nullIf(saflcs, saflcs)", "Nullable(String)"},
		{"nullIf(saflc, saf)", "Nullable(Int32)"},

		// The plain LowCardinality column gives the same answer.
		{"nullIf(lci32, lci32)", "Nullable(Int32)"},

		// A constant second argument lets the LowCardinality survive.
		// The marker is still gone.
		{"nullIf(saflc, 1)", "LowCardinality(Nullable(Int32))"},
		{"nullIf(lci32, 1)", "LowCardinality(Nullable(Int32))"},

		// A BARE inner type keeps the marker, and the Nullable that
		// nullIf adds goes OUTSIDE it.
		{"nullIf(saf, saf)", "Nullable(SimpleAggregateFunction(anyLast, Int32))"},
		{"nullIf(safs, safs)", "Nullable(SimpleAggregateFunction(anyLast, String))"},
		{"nullIf(saf, 1)", "Nullable(SimpleAggregateFunction(anyLast, Int32))"},
	} {
		got := inferCHTypeString(t, schema, testCase.expr)
		if got != testCase.want {
			t.Errorf("inferCHTypeString(%q) = %q, want %q", testCase.expr, got, testCase.want)
		}
	}
}

// TestNullIfMarkerWithNullableInnerIsNotDoubleWrapped pins the second
// fault of the regression.
//
// nullIf must make the value nullable. When the inner type of the marker
// is ALREADY Nullable, the server satisfies that by giving the marker
// back UNCHANGED. It does NOT add a second Nullable around it.
//
// The reading was verified at EXECUTION and not only by analysis.
// toTypeName is blind to an execution-time failure, thus the VALUE was
// selected as well. On the fixture row the two arguments are equal, and
// the call answers NULL with isNull = 1. The marker form therefore
// really does carry the null.
//
// Measured on ClickHouse 25.8.29.51:
//
//	toTypeName(nullIf(safn, safn))  SimpleAggregateFunction(anyLast, Nullable(Int32))
//	nullIf(safn, safn)              \N   isNull = 1
//
// The answer does not depend on the second argument:
//
//	nullIf(safn, 1)     SimpleAggregateFunction(anyLast, Nullable(Int32))
//	nullIf(safn, saf)   SimpleAggregateFunction(anyLast, Nullable(Int32))
func TestNullIfMarkerWithNullableInnerIsNotDoubleWrapped(t *testing.T) {
	schema := markerInnerWrapperSchema(t)
	const want = "SimpleAggregateFunction(anyLast, Nullable(Int32))"
	for _, expr := range []string{
		"nullIf(safn, safn)",
		"nullIf(safn, 1)",
		"nullIf(safn, saf)",
	} {
		got := inferCHTypeString(t, schema, expr)
		if got != want {
			t.Errorf("inferCHTypeString(%q) = %q, want %q", expr, got, want)
		}
	}
}
