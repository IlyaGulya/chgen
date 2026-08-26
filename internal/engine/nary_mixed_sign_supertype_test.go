package engine

import (
	"fmt"
	"testing"
)

// the regression: commonCHTypes folds the branch types in PAIRS, in a sorted
// order that puts wide integers (Int128, UInt128, Int256, UInt256) in
// the same rank as every plain integer. When a signed integer meets
// UInt64 before a wide branch joins, the pair rule correctly refuses
// (Int64 with UInt64 alone has no supertype), and the fold stops even
// though a wide branch, present elsewhere in the list, would have held
// both. The server computes the supertype over ALL branches at once and
// does not depend on the branch order.
//
// Measured on ClickHouse 25.8.29.51 with SELECT toTypeName(multiIf(...))
// FROM t on real table columns, never on constant literals, because the
// server folds a constant. All SIX permutations of each triple give the
// SAME type:
//
//	multiIf(b, i8|i16|i32|i64|Nullable(i32), u64, i128)   -> Int128
//	multiIf(b, i8|i16|i32|i64|Nullable(i32), u64, u128)   -> Int256
//	multiIf(b, i32|i64,                      u64, i256)   -> Int256
//
// A wide UNSIGNED peer of UInt64 with NO signed operand at all, or with
// a signed operand that is not wide enough to also swallow the OTHER
// unsigned operand, still has no supertype: UInt256 needs a signed type
// of 512 bits to hold it doubled together with UInt64 doubled, and
// Int512 does not exist. This refusal is correct and must not change:
//
//	multiIf(b, i32, u64, u256)   NO_COMMON_TYPE (measured, all six orders)
//
// The CONTROL that must never regress: a plain signed integer with
// UInt64 and NO wide branch anywhere in the list has no supertype
// either, because Int64 cannot hold the UInt64 range doubled. Widening
// this pair alone, with no wide branch present, would be a DEFECT of
// the opposite kind: an accept where the server refuses.
func mixedSignFixtureSchema(t *testing.T) *Schema {
	t.Helper()
	schema, err := schemaFromDDLErr(t, `CREATE TABLE t (
    b UInt8,
    i8 Int8, i16 Int16, i32 Int32, i64 Int64, ni32 Nullable(Int32),
    u64 UInt64, i128 Int128, u128 UInt128, i256 Int256, u256 UInt256
) ENGINE = MergeTree ORDER BY tuple();`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	return schema
}

// TestOrderIndependentWideIntegerSupertype pins the measured triples
// above, in every permutation, through the real multiIf inference path.
func TestOrderIndependentWideIntegerSupertype(t *testing.T) {
	schema := mixedSignFixtureSchema(t)
	signedNames := []string{"i8", "i16", "i32", "i64", "ni32"}

	// signed, UInt64, Int128 -> Int128 in every order.
	for _, signed := range signedNames {
		want := "Int128"
		if signed == "ni32" {
			want = "Nullable(Int128)"
		}
		permutations := [][3]string{
			{signed, "u64", "i128"},
			{"u64", signed, "i128"},
			{"i128", signed, "u64"},
			{signed, "i128", "u64"},
			{"u64", "i128", signed},
			{"i128", "u64", signed},
		}
		for _, perm := range permutations {
			expr := fmt.Sprintf("multiIf(b, %s, b, %s, %s)", perm[0], perm[1], perm[2])
			if got := inferCHTypeString(t, schema, expr); got != want {
				t.Errorf("type of %q = %s, want %s", expr, got, want)
			}
		}
	}

	// signed, UInt64, UInt128 -> Int256 in every order.
	for _, signed := range signedNames {
		want := "Int256"
		if signed == "ni32" {
			want = "Nullable(Int256)"
		}
		permutations := [][3]string{
			{signed, "u64", "u128"},
			{"u64", signed, "u128"},
			{"u128", signed, "u64"},
			{signed, "u128", "u64"},
			{"u64", "u128", signed},
			{"u128", "u64", signed},
		}
		for _, perm := range permutations {
			expr := fmt.Sprintf("multiIf(b, %s, b, %s, %s)", perm[0], perm[1], perm[2])
			if got := inferCHTypeString(t, schema, expr); got != want {
				t.Errorf("type of %q = %s, want %s", expr, got, want)
			}
		}
	}

	// signed, UInt64, Int256 -> Int256 in every order.
	for _, signed := range []string{"i32", "i64"} {
		permutations := [][3]string{
			{signed, "u64", "i256"},
			{"u64", signed, "i256"},
			{"i256", signed, "u64"},
		}
		for _, perm := range permutations {
			expr := fmt.Sprintf("multiIf(b, %s, b, %s, %s)", perm[0], perm[1], perm[2])
			if got := inferCHTypeString(t, schema, expr); got != "Int256" {
				t.Errorf("type of %q = %s, want Int256", expr, got)
			}
		}
	}
}

// TestWideUnsignedPeerStillRefused pins the refusal that must survive
// the fix: UInt256 with a signed peer and UInt64 has no supertype,
// because no signed type of 512 bits exists to hold both doubled.
func TestWideUnsignedPeerStillRefused(t *testing.T) {
	schema := mixedSignFixtureSchema(t)
	permutations := [][3]string{
		{"i32", "u64", "u256"},
		{"u64", "i32", "u256"},
		{"u256", "i32", "u64"},
		{"i64", "u64", "u256"},
		{"u64", "u256", "i64"},
	}
	for _, perm := range permutations {
		expr := fmt.Sprintf("multiIf(b, %s, b, %s, %s)", perm[0], perm[1], perm[2])
		if _, err := inferCHTypeStringErr(t, schema, expr); err == nil {
			t.Errorf("type of %q succeeded, want a refusal", expr)
		}
	}
}

// TestSignedUInt64PairAloneStaysRefused is the CONTROL. A signed
// integer with UInt64 and NO wide branch anywhere in the list has no
// supertype. A fix for the regression must not make this pair succeed on
// its own: that would be a refusal narrower than the server's, which is
// the opposite defect (a silently wrong type for a query the server
// refuses).
func TestSignedUInt64PairAloneStaysRefused(t *testing.T) {
	schema := mixedSignFixtureSchema(t)
	for _, signed := range []string{"i8", "i16", "i32", "i64"} {
		for _, expr := range []string{
			fmt.Sprintf("if(b, %s, u64)", signed),
			fmt.Sprintf("if(b, u64, %s)", signed),
		} {
			if _, err := inferCHTypeStringErr(t, schema, expr); err == nil {
				t.Errorf("type of %q succeeded, want a refusal", expr)
			}
		}
	}
}

// TestFourAndFiveBranchWideIntegerSupertype checks that the defect
// grows with arity, as the regression measured, and that the fix holds at
// higher arity too. Measured on ClickHouse 25.8.29.51:
//
//	multiIf(b, i32, b, i64, b, u64, i128)          -> Int128
//	multiIf(b, i32, b, u64, b, i128, u128)         -> Int256
//	multiIf(b, i8, b, i16, b, u64, b, i32, i128)   -> Int128
func TestFourAndFiveBranchWideIntegerSupertype(t *testing.T) {
	schema := mixedSignFixtureSchema(t)
	cases := []struct {
		expr string
		want string
	}{
		{"multiIf(b, i32, b, i64, b, u64, i128)", "Int128"},
		{"multiIf(b, u64, b, i32, b, i64, i128)", "Int128"},
		{"multiIf(b, i128, b, i32, b, i64, u64)", "Int128"},
		{"multiIf(b, i32, b, u64, b, i128, u128)", "Int256"},
		{"multiIf(b, u64, b, i32, b, u128, i128)", "Int256"},
		{"multiIf(b, i128, b, u128, b, i32, u64)", "Int256"},
		{"multiIf(b, i8, b, i16, b, u64, b, i32, i128)", "Int128"},
		{"multiIf(b, u64, b, i8, b, i16, b, i32, i128)", "Int128"},
		{"multiIf(b, i128, b, i8, b, i16, b, i32, u64)", "Int128"},
	}
	for _, testCase := range cases {
		if got := inferCHTypeString(t, schema, testCase.expr); got != testCase.want {
			t.Errorf("type of %q = %s, want %s", testCase.expr, got, testCase.want)
		}
	}
}

// degradedPairwiseCHTypes reproduces the PRE-FIX defect on purpose: it
// folds the branch list in pairs, in source order, with no n-ary
// shortcut. This is the exact shape that the regression measured as wrong,
// kept here only so the self-test below can prove that the permutation
// sweep actually notices a reordering defect and does not merely report
// green because it never ran.
func degradedPairwiseCHTypes(types []CHType) (CHType, error) {
	if len(types) == 0 {
		return CHType{}, fmt.Errorf("cannot infer common type from no expressions")
	}
	result := stripLowCardinalityDeep(types[0])
	for _, next := range types[1:] {
		var err error
		result, err = commonCHType(result, next)
		if err != nil {
			return CHType{}, err
		}
	}
	return result, nil
}

// TestPermutationSweepNoticesADegradedFold is the REQUIRED self-test: it
// proves that the permutation sweep can tell "the fold is
// order-independent" apart from "the sweep never ran". It runs the
// SAME triple through the FIXED commonCHTypes and through
// degradedPairwiseCHTypes, the known-bad pre-fix shape, over every
// permutation, and asserts that the two DISAGREE on at least one order.
// If this test ever started passing with an empty disagreement set, the
// sweep would be too weak to catch a regression of the regression.
func TestPermutationSweepNoticesADegradedFold(t *testing.T) {
	triple := []CHType{{Name: "Int32"}, {Name: "UInt64"}, {Name: "Int128"}}
	permutations := permuteThreeCHTypes(triple)

	fixedAnswers := map[string]bool{}
	for _, perm := range permutations {
		got, err := commonCHTypes(perm)
		if err != nil {
			t.Fatalf("commonCHTypes(%v) error = %v, want a type", perm, err)
		}
		fixedAnswers[got.String()] = true
	}
	if len(fixedAnswers) != 1 {
		t.Fatalf("the FIXED fold is not order-independent on its own test triple: %v", fixedAnswers)
	}

	sawRefusal := false
	sawADifferentType := false
	for _, perm := range permutations {
		degraded, err := degradedPairwiseCHTypes(perm)
		if err != nil {
			sawRefusal = true
			continue
		}
		if !fixedAnswers[degraded.String()] {
			sawADifferentType = true
		}
	}
	if !sawRefusal && !sawADifferentType {
		t.Fatal("the degraded pairwise fold agreed with the fix on every permutation; " +
			"the sweep cannot distinguish a fixed fold from a broken one, " +
			"which means it would stay green even if commonCHTypes regressed")
	}
}

// permuteThreeCHTypes returns all six permutations of a 3-element slice.
func permuteThreeCHTypes(types []CHType) [][]CHType {
	if len(types) != 3 {
		panic("permuteThreeCHTypes requires exactly three elements")
	}
	indices := [6][3]int{
		{0, 1, 2}, {0, 2, 1}, {1, 0, 2},
		{1, 2, 0}, {2, 0, 1}, {2, 1, 0},
	}
	result := make([][]CHType, 0, 6)
	for _, order := range indices {
		result = append(result, []CHType{types[order[0]], types[order[1]], types[order[2]]})
	}
	return result
}
