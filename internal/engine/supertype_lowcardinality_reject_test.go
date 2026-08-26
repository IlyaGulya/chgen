package engine

import "testing"

// This file pins the rejection rule of the container-constructor
// LowCardinality wrappers.
//
// commonContainerMemberCHType keeps a LowCardinality wrapper when EVERY
// member carried one. That keep-if-all rule is measured and it is
// correct about WHEN to keep a wrapper. It says nothing about WHETHER
// the common base can hold a wrapper at all. A base in the measured
// rejection table (see lowCardinalityRejectedResultNames) cannot, thus
// the keep-if-all rule alone gives a type that the server refuses. That
// is a silently wrong type, which is the worst defect class in this
// package. The gap was the regression.
//
// The rule applies at TWO depths, because the constructor asks it again
// for each parameter position:
//
//	commonContainerMemberCHType          the outer node
//	restoreContainerMemberLowCardinality the nested nodes
//
// MEASUREMENT (ClickHouse 25.8.29.51, real columns in a real table, at
// allow_suspicious_low_cardinality_types=1, which is what the rest of
// the harness uses).
//
// The server REFUSES the whole expression for a rejected base. It does
// NOT give back the bare type. Every rejected base gives the same code:
//
//	CAST(lcs AS LowCardinality(Decimal(18, 2)))     Code: 43 ILLEGAL_TYPE_OF_ARGUMENT
//	CAST(lcs AS LowCardinality(DateTime64(3)))      Code: 43 ILLEGAL_TYPE_OF_ARGUMENT
//	CAST(lcs AS LowCardinality(Enum8('a'=1,'b'=2))) Code: 43 ILLEGAL_TYPE_OF_ARGUMENT
//	CAST(lcs AS LowCardinality(Array(String)))      Code: 43 ILLEGAL_TYPE_OF_ARGUMENT
//	CAST(lcs AS LowCardinality(Tuple(Int32)))       Code: 43 ILLEGAL_TYPE_OF_ARGUMENT
//	CAST(lcs AS LowCardinality(Map(String, String))) Code: 43 ILLEGAL_TYPE_OF_ARGUMENT
//
// A LowCardinality(Enum8) COLUMN is refused with the same Code: 43, thus
// no column can carry such a member either.
//
// The accepted bases keep the wrapper, and four of them are the ones
// that the server MESSAGE wrongly omits. The message says
// "supported only for numbers, strings, Date or DateTime", but:
//
//	CAST(lcs AS LowCardinality(String))  LowCardinality(String)  accepted
//	CAST(lcs AS LowCardinality(Int32))   LowCardinality(Int32)   accepted
//	CAST(lcs AS LowCardinality(UUID))    LowCardinality(UUID)    accepted
//	CAST(lcs AS LowCardinality(IPv4))    LowCardinality(IPv4)    accepted
//	CAST(lcs AS LowCardinality(Bool))    LowCardinality(Bool)    accepted
//
// Thus the correct answer for a rejected base is the BARE common type:
// chgen must not claim a wrapper that no expression can have. Dropping
// the wrapper is what wrapLowCardinality already does, and routing both
// sites through it makes the measured table apply once for all paths.

// tmwLowCardinality builds a LowCardinality node for a test input. The
// test INPUTS are deliberately hand-built, because the point of the test
// is what the code under test does with a member that carries the
// wrapper.
func tmwLowCardinality(inner CHType) CHType {
	return CHType{Name: "LowCardinality", Params: []CHType{inner}}
}

// tmwRejectedBases are the measured Code: 43 families, each with a
// realistic parameter shape.
func tmwRejectedBases() []CHType {
	return []CHType{
		{Name: "Decimal", Params: []CHType{{Name: "18"}, {Name: "2"}}},
		{Name: "DateTime64", Params: []CHType{{Name: "3"}}},
		{Name: "Enum8"},
		{Name: "Enum16"},
		{Name: "Array", Params: []CHType{{Name: "String"}}},
		{Name: "Tuple", Params: []CHType{{Name: "Int32"}}},
		{Name: "Map", Params: []CHType{{Name: "String"}, {Name: "String"}}},
	}
}

// tmwAcceptedBases are measured bases that DO hold the wrapper. The last
// three are the ones the server message wrongly omits.
func tmwAcceptedBases() []CHType {
	return []CHType{
		{Name: "String"},
		{Name: "Int32"},
		{Name: "UUID"},
		{Name: "IPv4"},
		{Name: "Bool"},
	}
}

// TestContainerMemberOuterDropsRejectedLowCardinality pins the OUTER
// site. Two members that BOTH carry LowCardinality over a rejected base
// must give the bare common type, because the wrapped form is refused
// with Code: 43.
func TestContainerMemberOuterDropsRejectedLowCardinality(t *testing.T) {
	for _, base := range tmwRejectedBases() {
		member := tmwLowCardinality(base)
		result, err := commonContainerMemberCHType([]CHType{member, member})
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", base.String(), err)
		}
		if resultHasLowCardinality(result) {
			t.Errorf("outer %s: got %s, want no LowCardinality wrapper; "+
				"the server refuses that type with Code: 43",
				base.String(), result.String())
		}
	}
}

// TestContainerMemberNestedDropsRejectedLowCardinality pins the NESTED
// site. The member is a Tuple whose one parameter position carries
// LowCardinality over a rejected base, thus the keep-if-all rule fires
// at that position and not at the top.
func TestContainerMemberNestedDropsRejectedLowCardinality(t *testing.T) {
	for _, base := range tmwRejectedBases() {
		member := CHType{
			Name:   "Tuple",
			Params: []CHType{tmwLowCardinality(base)},
		}
		result, err := commonContainerMemberCHType([]CHType{member, member})
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", base.String(), err)
		}
		if resultHasLowCardinality(result) {
			t.Errorf("nested %s: got %s, want no LowCardinality wrapper at the "+
				"parameter position; the server refuses that type with Code: 43",
				base.String(), result.String())
		}
	}
}

// TestContainerMemberKeepsAcceptedLowCardinality is the counter-test. It
// makes sure the correction removes ONLY the refused wrappers. An
// unnecessary strip would be a real loss of information, and the
// UUID, IPv4 and Bool cells are exactly the ones that a fix written from
// the server MESSAGE instead of the measurement would get wrong.
func TestContainerMemberKeepsAcceptedLowCardinality(t *testing.T) {
	for _, base := range tmwAcceptedBases() {
		member := tmwLowCardinality(base)

		outer, err := commonContainerMemberCHType([]CHType{member, member})
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", base.String(), err)
		}
		want := "LowCardinality(" + base.String() + ")"
		if outer.String() != want {
			t.Errorf("outer %s: got %s, want %s", base.String(), outer.String(), want)
		}

		nestedMember := CHType{Name: "Tuple", Params: []CHType{member}}
		nested, err := commonContainerMemberCHType([]CHType{nestedMember, nestedMember})
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", base.String(), err)
		}
		wantNested := "Tuple(LowCardinality(" + base.String() + "))"
		if nested.String() != wantNested {
			t.Errorf("nested %s: got %s, want %s",
				base.String(), nested.String(), wantNested)
		}
	}
}

// resultHasLowCardinality reports whether a LowCardinality wrapper is
// present at any depth of a type.
func resultHasLowCardinality(value CHType) bool {
	if value.Name == "LowCardinality" {
		return true
	}
	for _, param := range value.Params {
		if resultHasLowCardinality(param) {
			return true
		}
	}
	return false
}
