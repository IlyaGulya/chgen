package engine

import (
	"strings"
	"testing"
)

// This test pins the regression delegation:
// simpleAggregateMarkerSurvivesValuePreserving must equal
// simpleAggregateMarkerSurvives(inner with one outer Nullable peeled
// off), over every inner shape the wrapper grid alphabet can produce.
//
// The equivalence is a MEASURED fact, not a proof by construction: it
// holds because a Nullable inner type can never itself be a Nullable, a
// LowCardinality, an Array, a Tuple or a Map (Code 43 on ClickHouse
// 25.8.29.51 for all five, checked live during the regression). A future edit
// that changes either function without re-checking that fact must fail
// this test.
func TestValuePreservingMarkerDelegatesToBaseRule(t *testing.T) {
	scalars := []string{"Int32", "String"}

	var inners []CHType
	for _, s := range scalars {
		inners = append(inners,
			CHType{Name: s},
			CHType{Name: "Nullable", Params: []CHType{{Name: s}}},
			CHType{Name: "LowCardinality", Params: []CHType{{Name: s}}},
			CHType{Name: "LowCardinality", Params: []CHType{{Name: "Nullable", Params: []CHType{{Name: s}}}}},
		)
	}
	inners = append(inners,
		CHType{Name: "Array", Params: []CHType{{Name: "Int32"}}},
		CHType{Name: "Tuple", Params: []CHType{{Name: "Int32"}, {Name: "String"}}},
		CHType{Name: "Map", Params: []CHType{{Name: "String"}, {Name: "Int64"}}},
	)

	for _, inner := range inners {
		got := simpleAggregateMarkerSurvivesValuePreserving(inner)

		peeled := inner
		if strings.EqualFold(peeled.Name, "Nullable") && len(peeled.Params) == 1 {
			peeled = peeled.Params[0]
		}
		want := simpleAggregateMarkerSurvives(peeled)

		if got != want {
			t.Errorf("simpleAggregateMarkerSurvivesValuePreserving(%+v) = %v, "+
				"want %v (delegation to simpleAggregateMarkerSurvives(%+v) disagrees)",
				inner, got, want, peeled)
		}
	}
}

// TestValuePreservingMarkerDelegationDivergesOnUnreachableDoubleNullable
// names the ONE shape where the collapsed delegation and the pre-fix
// base implementation disagree as FUNCTIONS, and records why that
// disagreement never runs.
//
// The pre-collapse code read the inner type ONCE: it peeled at most one
// outer Nullable and then ran its OWN five-case switch, which did not
// list "Nullable" among the drop cases. Called on
// Nullable(Nullable(Int32)), it peeled the outer Nullable, saw
// Nullable(Int32), matched none of its five cases, and answered true.
//
// The collapsed function peels one outer Nullable exactly the same way,
// but then DELEGATES to simpleAggregateMarkerSurvives, whose switch DOES
// list "Nullable" as a drop case (that is the very asymmetry the two
// functions exist to express for a single-wrapped inner). Fed the
// left-over Nullable(Int32), the base rule now sees a second-level
// Nullable name and drops, so the collapsed function answers false for
// the same Nullable(Nullable(Int32)) argument.
//
// This is a genuine divergence of the two functions, found by the
// coordinator's exhaustive 147-shape, two-level hand check outside this
// test (a964a25 base vs. The regression collapse): "checked 147 types,
// divergences 7", all seven being Nullable(Nullable(T)) for each scalar
// family. This test pins the ONE representative cell and the reason it
// is safe to ignore; it does not re-run that outside enumeration.
//
// The shape is UNREACHABLE. A marker's inner type T comes from the
// SERVER, and a Nullable cannot hold a second Nullable. Confirmed live
// on ClickHouse 25.8.29.51 by CREATE TABLE:
//
//	Nullable(Nullable(Int32))  Code 43, ILLEGAL_TYPE_OF_ARGUMENT,
//	                           "Nested type Nullable(Int32) cannot be
//	                           inside Nullable type"
//
// Thus simpleAggregateMarkerSurvivesValuePreserving can be CALLED with
// this shape in Go (the parameter type does not forbid it), but no live
// SimpleAggregateFunction(f, Nullable(Nullable(T))) marker can ever
// reach the call: callMarkerInner reads the marker's Params[1] straight
// from what the SERVER put there, and the server refuses to create a
// column of that shape at all. The divergence is real in Go and
// impossible in production.
//
// DO NOT delete this test to silence a future failure here, and do NOT
// "fix" the divergence by making simpleAggregateMarkerSurvivesValuePreserving
// peel more than one Nullable. The peel is exactly one level because a
// real marker inner can carry at most one Nullable; a wider peel would
// silently accept an argument shape the server can never produce, which
// hides a bug rather than removing one. If this test ever needs to
// change, the change must start from a new server measurement, not from
// making the assertion pass.
func TestValuePreservingMarkerDelegationDivergesOnUnreachableDoubleNullable(t *testing.T) {
	doubleNullable := CHType{
		Name: "Nullable",
		Params: []CHType{{
			Name:   "Nullable",
			Params: []CHType{{Name: "Int32"}},
		}},
	}

	// The collapsed delegation peels the outer Nullable and asks
	// simpleAggregateMarkerSurvives about the left-over Nullable(Int32),
	// whose Name is itself "Nullable", one of the five drop cases. The
	// collapsed function therefore answers false for this argument,
	// where the pre-collapse base implementation answered true. This is
	// the documented divergence, not a bug in the assertion.
	if got := simpleAggregateMarkerSurvivesValuePreserving(doubleNullable); got {
		t.Fatalf("simpleAggregateMarkerSurvivesValuePreserving(%+v) = true, want false; "+
			"the divergence documented above no longer matches the code, re-measure before editing this test",
			doubleNullable)
	}
}
