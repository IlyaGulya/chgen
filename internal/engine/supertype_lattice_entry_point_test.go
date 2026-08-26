package engine

import (
	"strings"
	"testing"
)

// This file is the witness for the supertypeLattice enum in supertype.go.
//
// THE CLAIM UNDER TEST. The three lattice entry points (branchLattice,
// containerMemberLattice, greatestLeastLattice) are NOT interchangeable:
// naming the wrong one for a construct gives the OTHER measured answer,
// wrong for the construct that asked. A mechanism that "names the entry
// point" is worthless unless a wrong name is actually caught, so this
// file:
//
//  1. pins the three measured cells that separate the entry points, each
//     resolved through resolveSupertypeLattice and named correctly;
//  2. shows a caller that names the WRONG entry point for one of those
//     cells and demonstrates the failure directly, by running the
//     assertion an honest caller would run and showing it does not hold;
//  3. shows resolveSupertypeLattice panics on an unnamed value, which is
//     the "caller did not choose" case the enum forbids by construction.
//
// Every expected value is measured on ClickHouse 25.8.29.51 through the
// HTTP interface, against real columns of a real Memory table, never over
// a literal, because the server folds a constant and the measurement
// would lie. See the recorded defect for the session that took these
// readings.

// TestResolveSupertypeLatticeGivesTheMeasuredAnswerPerEntryPoint pins the
// three cells that separate the entry points, each called through
// resolveSupertypeLattice with the CORRECT tag for its construct.
func TestResolveSupertypeLatticeGivesTheMeasuredAnswerPerEntryPoint(t *testing.T) {
	lc := CHType{Name: "LowCardinality", Params: []CHType{{Name: "String"}}}
	i32 := CHType{Name: "Int32"}
	u64 := CHType{Name: "UInt64"}

	cases := []struct {
		name    string
		lattice supertypeLattice
		fn      string
		types   []CHType
		want    string
		wantErr bool
	}{
		// Branch transport (if, multiIf, CASE, coalesce): LowCardinality
		// is gone at every depth. Measured: if(b, lc, lc) is String, not
		// LowCardinality(String).
		{
			name:    "branch transport drops LowCardinality",
			lattice: branchLattice,
			types:   []CHType{lc, lc},
			want:    "String",
		},
		// Container constructor (array, map): LowCardinality survives
		// when every member carries it. Measured: array(lc, lc) is
		// Array(LowCardinality(String)) (the Array wrapper itself is
		// added by arrayFunctionResult; this call checks only the
		// element type that commonContainerMemberCHType hands back).
		{
			name:    "container constructor keeps LowCardinality",
			lattice: containerMemberLattice,
			types:   []CHType{lc, lc},
			want:    "LowCardinality(String)",
		},
		// greatest/least: the signed/UInt64 arity-2 exception that the
		// branch family refuses outright. Measured: if(c, i32, u64) is
		// Code 386 NO_COMMON_TYPE, while greatest(i32, u64) is Int128.
		{
			name:    "greatest accepts signed with UInt64",
			lattice: greatestLeastLattice,
			fn:      "greatest",
			types:   []CHType{i32, u64},
			want:    "Int128",
		},
		// The branch family refuses the SAME pair that greatest accepts.
		// This is the cell that proves the entry points disagree, not
		// merely that they are named differently.
		{
			name:    "branch transport refuses signed with UInt64",
			lattice: branchLattice,
			types:   []CHType{i32, u64},
			wantErr: true,
		},
	}

	for _, testCase := range cases {
		got, err := resolveSupertypeLattice(testCase.lattice, testCase.fn, testCase.types)
		if testCase.wantErr {
			if err == nil {
				t.Errorf("%s: got %s, want a refusal", testCase.name, got.String())
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: error = %v, want %q", testCase.name, err, testCase.want)
			continue
		}
		if got.String() != testCase.want {
			t.Errorf("%s: got %s, want %s", testCase.name, got.String(), testCase.want)
		}
	}
}

// TestResolveSupertypeLatticeCatchesWrongEntryPoint is the required
// self-test: it INJECTS a caller that names the WRONG entry point for a
// construct and shows that the wrong choice produces the wrong answer for
// that construct, so a caller who bothered to assert the measured cell
// for their OWN construct catches the mistake immediately.
//
// The construct under test is the container element type of
// array(lc, lc). The measured, correct answer is
// LowCardinality(String) (containerMemberLattice). A caller who
// mistakenly reached for the branch entry point instead - the exact
// mistake the tracking item describes, "a caller can grab the nearest helper and
// get a wrong answer that looks right" - gets String, silently missing
// the wrapper. This test asserts the CORRECT cell against the WRONG
// entry point's result and shows the assertion fails, which is exactly
// what would stop a real wrong-entry-point call in CI.
func TestResolveSupertypeLatticeCatchesWrongEntryPoint(t *testing.T) {
	lc := CHType{Name: "LowCardinality", Params: []CHType{{Name: "String"}}}
	members := []CHType{lc, lc}

	const wantForContainerConstruct = "LowCardinality(String)"

	// The WRONG choice: a hypothetical array/map rule that reached for
	// the branch entry point because "commonCHTypes" is the more
	// familiar name, exactly the mistake this tracking item's mechanism exists to
	// catch.
	wrongEntryPoint := branchLattice
	gotWrong, err := resolveSupertypeLattice(wrongEntryPoint, "", members)
	if err != nil {
		t.Fatalf("resolveSupertypeLattice(%s, ...) error = %v", wrongEntryPoint, err)
	}

	// BEFORE: the wrong entry point silently answers a plausible-looking
	// but wrong type. Confirm the failure actually reproduces, so this
	// test cannot pass by coincidence if the two entry points ever
	// stopped disagreeing on this cell.
	if gotWrong.String() == wantForContainerConstruct {
		t.Fatalf(
			"setup invariant broken: %s and containerMemberLattice now agree on %v (both gave %s); "+
				"this test needs them to disagree to demonstrate the catch",
			wrongEntryPoint, members, gotWrong.String(),
		)
	}
	t.Logf("BEFORE (wrong entry point %s): array(lc, lc) element resolved to %s, want %s -- silently wrong, nothing panics",
		wrongEntryPoint, gotWrong.String(), wantForContainerConstruct)

	// This is the assertion a real caller of the container rule would run
	// against the WRONG result: a plain equality check, exactly as a
	// unit test would write it. It must fail, proving the mistake is
	// catchable. The check is evaluated as data here (a bool this test
	// asserts ON), not run through *testing.T, because this OUTER test's
	// job is to PASS when the wrong choice is caught, not to fail
	// because of the wrong choice itself.
	assertionOnWrongResultPasses := gotWrong.String() == wantForContainerConstruct
	if assertionOnWrongResultPasses {
		t.Fatalf(
			"expected the measured-cell assertion to FAIL against the wrong entry point's result (proving the mistake is caught), but it passed: got %s",
			gotWrong.String(),
		)
	}

	// AFTER: the correct entry point gives the measured answer, and the
	// same assertion passes.
	gotRight, err := resolveSupertypeLattice(containerMemberLattice, "", members)
	if err != nil {
		t.Fatalf("resolveSupertypeLattice(containerMemberLattice, ...) error = %v", err)
	}
	if gotRight.String() != wantForContainerConstruct {
		t.Fatalf("AFTER (correct entry point containerMemberLattice): got %s, want %s",
			gotRight.String(), wantForContainerConstruct)
	}
	t.Logf("AFTER (correct entry point containerMemberLattice): array(lc, lc) element resolved to %s, matches measured value",
		gotRight.String())
}

// TestResolveSupertypeLatticePanicsOnUnnamedEntryPoint shows that the
// dispatcher refuses to guess. A zero value or any value outside the
// three named constants must panic rather than silently pick an
// implementation, because a silent default is the same defect class this
// enum exists to close.
func TestResolveSupertypeLatticePanicsOnUnnamedEntryPoint(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("resolveSupertypeLattice(0, ...) did not panic on an unnamed lattice value")
		}
		message, ok := r.(string)
		if !ok || !strings.Contains(message, "did not name a supertype lattice entry point") {
			t.Fatalf("panic value = %v, want a message naming the missing choice", r)
		}
	}()
	_, _ = resolveSupertypeLattice(supertypeLattice(0), "", []CHType{{Name: "Int32"}, {Name: "Int32"}})
}

// TestSupertypeLatticeStringNamesEveryMember is a hygiene check: every
// named constant must print its own name, and an unnamed value must not
// print a name that looks legitimate.
func TestSupertypeLatticeStringNamesEveryMember(t *testing.T) {
	cases := []struct {
		lattice supertypeLattice
		want    string
	}{
		{branchLattice, "branchLattice"},
		{containerMemberLattice, "containerMemberLattice"},
		{greatestLeastLattice, "greatestLeastLattice"},
	}
	for _, testCase := range cases {
		if got := testCase.lattice.String(); got != testCase.want {
			t.Errorf("%d.String() = %q, want %q", int(testCase.lattice), got, testCase.want)
		}
	}
	if got := supertypeLattice(0).String(); !strings.Contains(got, "supertypeLattice(0)") {
		t.Errorf("supertypeLattice(0).String() = %q, want it to name itself as unresolved, not a real member", got)
	}
}
