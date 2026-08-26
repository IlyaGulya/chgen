package engine

import "testing"

// This file pins the marker rule of the VALUE-PRESERVING SCALAR family:
// greatest, least, lagInFrame and leadInFrame.
//
// Before the rule, chgen kept the SimpleAggregateFunction marker over
// EVERY inner type for these four names. Over a composite inner type
// that is a SILENTLY WRONG TYPE, not a refusal. For example
// greatest(saflc) answered
// SimpleAggregateFunction(anyLast, LowCardinality(Int32)) stripped to
// SimpleAggregateFunction(anyLast, Int32), where the server answers
// LowCardinality(Int32): the marker stayed AND the LowCardinality was
// lost.
//
// Two independent facts make the answers correct:
//
//  1. The marker does not survive a LowCardinality, Array, Map or Tuple
//     inner type. This fact is SHARED by all four names.
//  2. The LowCardinality that the marker hid must then be seen. greatest
//     and least keep it at arity 1; lagInFrame and leadInFrame always
//     drop it. Each transport already owned that second decision, thus
//     the fix adds no second copy of it.
//
// Every expected value below was measured on ClickHouse 25.8.29.51
// through the HTTP interface, against real columns of a real
// AggregatingMergeTree table with one row, never over literals, because
// the server folds constants.
const valuePreservingScalarMarkerSchema = `
CREATE TABLE t (
    saflc  SimpleAggregateFunction(anyLast, LowCardinality(Int32)),
    saflcs SimpleAggregateFunction(anyLast, LowCardinality(String)),
    saflcn SimpleAggregateFunction(anyLast, LowCardinality(Nullable(String))),
    saggn  SimpleAggregateFunction(sum, Nullable(Int64)),
    sagg   SimpleAggregateFunction(sum, Int64),
    safarr SimpleAggregateFunction(anyLast, Array(Int32)),
    saftup SimpleAggregateFunction(anyLast, Tuple(Int32, Int32)),
    saf    SimpleAggregateFunction(anyLast, Int32),
    lc     LowCardinality(Int32),
    i32    Int32,
    s      String
);
`

// TestValuePreservingScalarMarkerOverLowCardinalityInner locks the four
// cells of the ticket and their close neighbours.
//
// The two groups DISAGREE on the final type although they agree that the
// marker goes. greatest and least keep the LowCardinality at arity 1;
// the frame-shift functions drop it. A single rule that kept the wrapper
// for all four would make the two window functions wrong in a NEW way.
//
// Measured:
//
//	greatest(saflc)                LowCardinality(Int32)
//	least(saflc)                   LowCardinality(Int32)
//	lagInFrame(saflc, 2) OVER ()   Int32
//	leadInFrame(saflc, 2) OVER ()  Int32
func TestValuePreservingScalarMarkerOverLowCardinalityInner(t *testing.T) {
	schema, err := schemaFromDDLErr(t, valuePreservingScalarMarkerSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	cases := []struct{ expr, want string }{
		// greatest and least KEEP the LowCardinality that the marker
		// hid, at arity 1.
		{"greatest(saflc)", "LowCardinality(Int32)"},
		{"least(saflc)", "LowCardinality(Int32)"},
		{"greatest(saflcs)", "LowCardinality(String)"},
		{"least(saflcs)", "LowCardinality(String)"},
		{"greatest(saflcn)", "LowCardinality(Nullable(String))"},
		{"least(saflcn)", "LowCardinality(Nullable(String))"},
		// lagInFrame and leadInFrame DROP it, at every arity.
		{"lagInFrame(saflc, 2) OVER ()", "Int32"},
		{"leadInFrame(saflc, 2) OVER ()", "Int32"},
		{"lagInFrame(saflcs, 2) OVER ()", "String"},
		{"leadInFrame(saflcs, 2) OVER ()", "String"},
		// The Nullable inside the LowCardinality still reaches the
		// result, thus the drop removes the LowCardinality only.
		{"lagInFrame(saflcn, 2) OVER ()", "Nullable(String)"},
		{"leadInFrame(saflcn, 2) OVER ()", "Nullable(String)"},
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

// TestValuePreservingScalarMarkerAtArityTwo locks the ARITY-TWO forms.
// The one-argument form is not representative: at arity 2 greatest and
// least drop the LowCardinality as well, thus the two groups agree
// again there.
//
// Measured:
//
//	greatest(saflc, saflc)   Int32
//	greatest(saflc, i32)     Int32
//	greatest(saflc, lc)      Int32
//	greatest(saflcs, s)      String
//	greatest(saflcn, s)      Nullable(String)
//	least(saflcs, saflcs)    String
func TestValuePreservingScalarMarkerAtArityTwo(t *testing.T) {
	schema, err := schemaFromDDLErr(t, valuePreservingScalarMarkerSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	cases := []struct{ expr, want string }{
		{"greatest(saflc, saflc)", "Int32"},
		{"least(saflc, saflc)", "Int32"},
		{"greatest(saflc, i32)", "Int32"},
		{"greatest(saflc, lc)", "Int32"},
		{"greatest(saflc, saf)", "Int32"},
		{"greatest(saflcs, s)", "String"},
		{"least(saflcs, saflcs)", "String"},
		{"greatest(saflcn, s)", "Nullable(String)"},
		{"least(saflcn, saflcn)", "Nullable(String)"},
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

// TestValuePreservingScalarMarkerCompositeInner locks the Array and
// Tuple inner types. The marker does not survive them either, for all
// four names. These cells were silently wrong before the rule as well,
// thus the rule is wider than the LowCardinality cells of the ticket.
//
// Measured:
//
//	greatest(safarr)                Array(Int32)
//	greatest(saftup)                Tuple(Int32, Int32)
//	lagInFrame(safarr, 1) OVER ()   Array(Int32)
//	lagInFrame(saftup, 1) OVER ()   Tuple(Int32, Int32)
func TestValuePreservingScalarMarkerCompositeInner(t *testing.T) {
	schema, err := schemaFromDDLErr(t, valuePreservingScalarMarkerSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	cases := []struct{ expr, want string }{
		{"greatest(safarr)", "Array(Int32)"},
		{"least(safarr)", "Array(Int32)"},
		{"greatest(saftup)", "Tuple(Int32, Int32)"},
		{"least(saftup)", "Tuple(Int32, Int32)"},
		{"lagInFrame(safarr, 1) OVER ()", "Array(Int32)"},
		{"leadInFrame(safarr, 1) OVER ()", "Array(Int32)"},
		{"lagInFrame(saftup, 1) OVER ()", "Tuple(Int32, Int32)"},
		{"leadInFrame(saftup, 1) OVER ()", "Tuple(Int32, Int32)"},
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

// TestValuePreservingScalarMarkerKeepsBareAndNullableInner locks the
// cells that must NOT change. The marker SURVIVES a bare scalar inner
// type, and it survives a Nullable of one.
//
// This is the cell where this family DISAGREES with the rule of
// simpleAggregateMarkerSurvives, which a function that READS the value
// uses. That is why the two rules are separate functions. Measured:
//
//	identity(saggn)                 Nullable(Int64)
//	greatest(saggn)                 SimpleAggregateFunction(sum, Nullable(Int64))
//	lagInFrame(saggn, 1) OVER ()    SimpleAggregateFunction(sum, Nullable(Int64))
//	leadInFrame(saggn, 1) OVER ()   SimpleAggregateFunction(sum, Nullable(Int64))
//	greatest(saf)                   SimpleAggregateFunction(anyLast, Int32)
//	lagInFrame(saf, 2) OVER ()      SimpleAggregateFunction(anyLast, Int32)
func TestValuePreservingScalarMarkerKeepsBareAndNullableInner(t *testing.T) {
	schema, err := schemaFromDDLErr(t, valuePreservingScalarMarkerSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	cases := []struct{ expr, want string }{
		{"greatest(saf)", "SimpleAggregateFunction(anyLast, Int32)"},
		{"least(saf)", "SimpleAggregateFunction(anyLast, Int32)"},
		{"greatest(sagg)", "SimpleAggregateFunction(sum, Int64)"},
		{"greatest(saggn)", "SimpleAggregateFunction(sum, Nullable(Int64))"},
		{"least(saggn)", "SimpleAggregateFunction(sum, Nullable(Int64))"},
		{"lagInFrame(saf, 2) OVER ()", "SimpleAggregateFunction(anyLast, Int32)"},
		{"leadInFrame(saf, 2) OVER ()", "SimpleAggregateFunction(anyLast, Int32)"},
		{"lagInFrame(saggn, 1) OVER ()", "SimpleAggregateFunction(sum, Nullable(Int64))"},
		{"leadInFrame(saggn, 1) OVER ()", "SimpleAggregateFunction(sum, Nullable(Int64))"},
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

// TestValuePreservingScalarRuleDiffersFromReadRule pins the ONE cell
// where the two marker rules must disagree. A test that only checked the
// four names could be satisfied by pointing them at the shared rule, and
// that would make the Nullable inner cell silently wrong.
func TestValuePreservingScalarRuleDiffersFromReadRule(t *testing.T) {
	nullableInner := CHType{Name: "Nullable", Params: []CHType{{Name: "Int64"}}}
	if simpleAggregateMarkerSurvives(nullableInner) {
		t.Errorf("the READ rule must drop the marker over a Nullable inner type")
	}
	if !simpleAggregateMarkerSurvivesValuePreserving(nullableInner) {
		t.Errorf("the value-preserving rule must KEEP the marker over a Nullable inner type")
	}
	// Both rules agree on every other shape that the family can meet.
	shared := []CHType{
		{Name: "Int64"},
		{Name: "String"},
		{Name: "LowCardinality", Params: []CHType{{Name: "String"}}},
		{Name: "Array", Params: []CHType{{Name: "Int32"}}},
		{Name: "Tuple", Params: []CHType{{Name: "Int32"}, {Name: "Int32"}}},
		{Name: "Map", Params: []CHType{{Name: "String"}, {Name: "Int32"}}},
	}
	for _, inner := range shared {
		if simpleAggregateMarkerSurvives(inner) != simpleAggregateMarkerSurvivesValuePreserving(inner) {
			t.Errorf("the two rules must agree on %s", inner.String())
		}
	}
	// A Nullable of a COMPOSITE type is still composite, thus the
	// value-preserving rule drops the marker there as well.
	nullableLowCardinality := CHType{
		Name:   "Nullable",
		Params: []CHType{{Name: "LowCardinality", Params: []CHType{{Name: "String"}}}},
	}
	if simpleAggregateMarkerSurvivesValuePreserving(nullableLowCardinality) {
		t.Errorf("a Nullable of a composite inner type must drop the marker")
	}
}

// TestGreatestLeastAnswerSignedWithUInt64 pins the measured answer for a
// signed argument mixed with UInt64.
//
// This test held the opposite expectation before the scalar transport change:
// over-refusal of the regression as the state of the day and said the marker
// rule must not remove that refusal. The refusal itself was the defect,
// thus the expectation is now the server answer.
//
// Measured on ClickHouse 25.8.29.51 over real columns of a real
// AggregatingMergeTree table, with
// allow_suspicious_low_cardinality_types=1 on the CREATE and on the
// SELECT:
//
//	greatest(i32, u64)     Int128
//	least(i32, u64)        Int128
//	greatest(saflc, u64)   Int128
//	least(saflc, u64)      Int128
//
// The SimpleAggregateFunction marker does NOT change the answer here.
// The marker of saflc holds LowCardinality(Int32), thus it does not
// survive a value read (measured: identity(saflc) is
// LowCardinality(Int32)), and the pair rule then sees a plain Int32
// against UInt64. The marker rule and the signed-with-UInt64 rule are
// independent, and this test keeps BOTH forms so that a later change to
// either one cannot pass unseen.
func TestGreatestLeastAnswerSignedWithUInt64(t *testing.T) {
	schema, err := schemaFromDDLErr(t, `
CREATE TABLE t (
    saflc SimpleAggregateFunction(anyLast, LowCardinality(Int32)),
    i32   Int32,
    u64   UInt64
);
`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	for _, expr := range []string{
		"greatest(i32, u64)",
		"least(i32, u64)",
		"greatest(saflc, u64)",
		"least(saflc, u64)",
	} {
		got, err := inferSelectItemCHType(t, schema, "SELECT "+expr+" AS a FROM t")
		if err != nil {
			t.Errorf("%s gave error %v, want Int128", expr, err)
			continue
		}
		if got != "Int128" {
			t.Errorf("%s = %q, want Int128", expr, got)
		}
	}
}
