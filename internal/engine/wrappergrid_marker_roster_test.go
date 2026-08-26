package engine

import (
	"fmt"
	"sort"
	"strings"
	"testing"
)

// This file guards the VALUE-PRESERVING MARKER FAMILY ROSTER.
//
// The roster is stated in prose today: the doc comment on
// simpleAggregateMarkerSurvivesValuePreserving (supertype.go, near
// line 582) names the exact members. The regression found that comment stale once already, after
// commit af69938 added nullIf to the family without updating it. A
// prose roster drifts from the code silently, because nothing reads the
// comment and nothing fails when the code changes under it.
//
// This test finds membership by INSPECTING BEHAVIOUR, not by reading the
// comment: for every function that carries its own transport override
// (wrapper_transport.go, wrapperTransportOverrides), it drives that
// function's simpleAggregateWhen condition over the SAME alphabet of
// marker inner types that the doc comment measures (bare scalar,
// Nullable(scalar), Array, Tuple, LowCardinality(scalar)) and compares
// the answers against simpleAggregateMarkerSurvivesValuePreserving
// directly. A function whose condition agrees with that rule on every
// cell of the alphabet is a behavioural member of the family, whether or
// not a comment says so.
//
// The test fails in BOTH directions:
//   - a name that behaves like a member but is missing from
//     wrapperMarkerRoster (a member the roster comment forgot), and
//   - a name in wrapperMarkerRoster that does not behave like a member
//     any more (a stale roster entry).
//
// wrapper_transport.go and supertype.go are NOT owned by this file. If
// this test ever needs a production change to pass honestly, that change
// is reported, not made here.

// wrapperMarkerRoster is this test's copy of the roster that the doc
// comment on simpleAggregateMarkerSurvivesValuePreserving states. Keep
// the two in sync by hand today; that is exactly the sync this test
// exists to check, in the direction code-to-roster. The reverse
// direction, roster-to-code, is checked by comparing this list against
// the set that transportBehavesAsValuePreservingMember reports.
var wrapperMarkerRoster = []string{
	"first_value_respect_nulls",
	"firstvaluerespectnulls",
	"greatest",
	"lag",
	"laginframe",
	"last_value_respect_nulls",
	"lastvaluerespectnulls",
	"lead",
	"leadinframe",
	"least",
	"nth_value",
	"nullif",
}

// markerRosterAlphabet is the closed set of marker inner shapes that
// simpleAggregateMarkerSurvivesValuePreserving documents a measured
// answer for. Two scalar bases (Int64, String) stand for "bare scalar",
// because the rule is a KIND test and not a per-base test; the numeric
// kind alone would leave the "kept at arity 1, dropped at arity 2 over a
// numeric inner" arm of greatest/least unexercised for the non-numeric
// side, so both are kept.
var markerRosterAlphabet = []CHType{
	{Name: "Int64"},
	{Name: "String"},
	{Name: "Nullable", Params: []CHType{{Name: "Int64"}}},
	{Name: "Nullable", Params: []CHType{{Name: "String"}}},
	{Name: "Array", Params: []CHType{{Name: "Int32"}}},
	{Name: "Tuple", Params: []CHType{{Name: "Int32"}, {Name: "Int32"}}},
	{Name: "LowCardinality", Params: []CHType{{Name: "String"}}},
}

// callWithMarker builds a wrapperCall that carries one
// SimpleAggregateFunction(anyLast, inner) marker on its first argument,
// at the given arity. The base and every later argument are a plain
// Int64, because the marker-survival question is decided by the marker's
// inner type and, for greatest/least, by argCount and the call's base
// type; a fixed numeric base isolates the inner-type axis under test.
func callWithMarker(inner CHType, argCount int) wrapperCall {
	marker := CHType{Name: "SimpleAggregateFunction", Params: []CHType{{Name: "anyLast"}, inner}}
	_, markerStack := splitWrapperStack(marker)
	stacks := make([]wrapperStack, argCount)
	stacks[0] = markerStack
	return wrapperCall{base: CHType{Name: "Int64"}, stacks: stacks, argCount: argCount}
}

// transportBehavesAsValuePreservingMember reports whether the given
// transport's simpleAggregateWhen condition agrees with
// simpleAggregateMarkerSurvivesValuePreserving on every cell of
// markerRosterAlphabet, AT ARITY 1.
//
// Arity 1 is the shape that isolates the shared family rule. greatest
// and least layer a SECOND, INDEPENDENT axis on top of it
// (greatestLeastKeepsSimpleAggregateCondition also drops the marker at
// arity 2 or more over a numeric inner, which the shared rule alone does
// not do), and that second axis is a property of greatest/least, not a
// property of family membership: nullIf and the window value functions
// carry no such axis at all. Testing at arity 2 as well would therefore
// make greatest/least fail this check although they are family members,
// which is why the alphabet is walked at arity 1 only.
//
// A transport with no simpleAggregateWhen (disposition is not
// wrapperConditional) cannot be a family member: it does not decide the
// marker per call at all.
func transportBehavesAsValuePreservingMember(transport wrapperTransport) bool {
	if transport.simpleAggregate != wrapperConditional || transport.simpleAggregateWhen == nil {
		return false
	}
	for _, inner := range markerRosterAlphabet {
		call := callWithMarker(inner, 1)
		want := simpleAggregateMarkerSurvivesValuePreserving(inner)
		got := transport.simpleAggregateWhen(call)
		if got != want {
			return false
		}
	}
	return true
}

// TestValuePreservingMarkerRosterMatchesBehaviour is the two-direction
// guard. It walks every name that carries its own transport override,
// classifies each one as a behavioural member or not, and compares that
// classification against wrapperMarkerRoster.
func TestValuePreservingMarkerRosterMatchesBehaviour(t *testing.T) {
	behavioural := map[string]bool{}
	for name, transport := range wrapperTransportOverrides {
		if transportBehavesAsValuePreservingMember(transport) {
			behavioural[name] = true
		}
	}

	roster := map[string]bool{}
	for _, name := range wrapperMarkerRoster {
		roster[name] = true
	}

	var forgotten []string
	for name := range behavioural {
		if !roster[name] {
			forgotten = append(forgotten, name)
		}
	}
	sort.Strings(forgotten)

	var stale []string
	for name := range roster {
		if !behavioural[name] {
			stale = append(stale, name)
		}
	}
	sort.Strings(stale)

	if len(forgotten) > 0 {
		t.Errorf("these names behave like a value-preserving marker family member "+
			"but wrapperMarkerRoster does not list them: %s. Add them to the roster "+
			"here AND to the doc comment on simpleAggregateMarkerSurvivesValuePreserving "+
			"in supertype.go.", strings.Join(forgotten, ", "))
	}
	if len(stale) > 0 {
		t.Errorf("these names are in wrapperMarkerRoster but no longer behave like a "+
			"value-preserving marker family member: %s. Remove them from the roster "+
			"here AND from the doc comment on "+
			"simpleAggregateMarkerSurvivesValuePreserving in supertype.go.",
			strings.Join(stale, ", "))
	}
}

// TestValuePreservingMarkerRosterCatchesAnInjectedFakeMember proves the
// forgotten-name direction: a transport that answers YES on the whole
// alphabet but is not in wrapperMarkerRoster must be reported.
func TestValuePreservingMarkerRosterCatchesAnInjectedFakeMember(t *testing.T) {
	fakeName := "chgen_test_fake_marker_member"
	fakeTransport := wrapperTransport{
		simpleAggregate: wrapperConditional,
		simpleAggregateWhen: func(call wrapperCall) bool {
			inner, ok := callMarkerInner(call)
			if !ok {
				return false
			}
			return simpleAggregateMarkerSurvivesValuePreserving(inner)
		},
	}
	if !transportBehavesAsValuePreservingMember(fakeTransport) {
		t.Fatal("the fake transport must behave exactly like a family member; " +
			"the injected condition is a direct pass-through of the family rule")
	}
	if err := runRosterCheckWithout(fakeName, fakeTransport); err == nil {
		t.Fatal("a behavioural member absent from wrapperMarkerRoster must fail the check")
	}
}

// TestValuePreservingMarkerRosterCatchesARemovedRealMember proves the
// stale-name direction: removing a real roster member's override from
// the set under test must be reported.
func TestValuePreservingMarkerRosterCatchesARemovedRealMember(t *testing.T) {
	if err := runRosterCheckWithoutName("nullif"); err == nil {
		t.Fatal("dropping nullif's transport while nullif stays in the roster " +
			"must fail the check")
	}
}

// runRosterCheckWithout runs the roster comparison over
// wrapperTransportOverrides plus one injected extra entry, and returns an
// error describing the first mismatch, or nil when the roster and the
// behaviour agree.
func runRosterCheckWithout(extraName string, extraTransport wrapperTransport) error {
	overrides := map[string]wrapperTransport{extraName: extraTransport}
	for name, transport := range wrapperTransportOverrides {
		overrides[name] = transport
	}
	return compareRosterAgainst(overrides, wrapperMarkerRoster)
}

// runRosterCheckWithoutName runs the roster comparison over
// wrapperTransportOverrides with the named entry deleted, and returns an
// error describing the first mismatch, or nil when the roster and the
// behaviour agree.
func runRosterCheckWithoutName(dropName string) error {
	overrides := map[string]wrapperTransport{}
	for name, transport := range wrapperTransportOverrides {
		if name == dropName {
			continue
		}
		overrides[name] = transport
	}
	return compareRosterAgainst(overrides, wrapperMarkerRoster)
}

// compareRosterAgainst is the same two-direction comparison that
// TestValuePreservingMarkerRosterMatchesBehaviour runs, factored out so
// the two proof tests can run it over an altered input set without
// duplicating the walk.
func compareRosterAgainst(overrides map[string]wrapperTransport, wantRoster []string) error {
	behavioural := map[string]bool{}
	for name, transport := range overrides {
		if transportBehavesAsValuePreservingMember(transport) {
			behavioural[name] = true
		}
	}
	roster := map[string]bool{}
	for _, name := range wantRoster {
		roster[name] = true
	}
	for name := range behavioural {
		if !roster[name] {
			return fmt.Errorf("%s behaves like a member but is not in the roster", name)
		}
	}
	for name := range roster {
		if !behavioural[name] {
			return fmt.Errorf("%s is in the roster but does not behave like a member", name)
		}
	}
	return nil
}
