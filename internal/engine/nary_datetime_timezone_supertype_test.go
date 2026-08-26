package engine

import (
	"fmt"
	"testing"
)

// the regression: commonCHTypes folds the branch types in a SORTED order
// (Decimal, then Float, then everything else), and a DateTime/DateTime64
// timezone join is ORDER DEPENDENT on the server itself. Before this fix
// chgen refused every DateTime/DateTime64 pair or triple whose branches
// carried different timezones, even though the server always answers.
//
// THE RULE, measured on ClickHouse 25.8.29.51 with SELECT
// toTypeName(greatest(...)) FROM t on real table columns, never on
// constant literals, over 3096 machine-compared cells at arity 2, 3 and
// 4 (see dateTimeTimezoneCHTypes for the full derivation):
//
//	precision = the MAXIMUM precision over all branches. A bare
//	            DateTime counts as precision 0.
//	timezone  = if EVERY branch has the maximum precision, the
//	            timezone of the FIRST branch. Otherwise the timezone
//	            of the LAST branch that attains the maximum precision.
//
// The rule is genuinely ORDER DEPENDENT: reversing two DateTime branches
// with the same precision but different timezones changes the answer.
// This is the OPPOSITE of the regression, where the server had NO order
// dependence and chgen wrongly had one; here chgen must ADD the order
// dependence, not remove it, so the fold must read branches in the
// caller's original order and must run BEFORE the Decimal/Float sort.
func dateTimeTimezoneFixtureSchema(t *testing.T) *Schema {
	t.Helper()
	schema, err := schemaFromDDLErr(t, `CREATE TABLE t (
    b UInt8,
    dtb DateTime,
    dt_utc DateTime('UTC'), dt_ber DateTime('Europe/Berlin'), dt_tok DateTime('Asia/Tokyo'),
    dt64_1_utc DateTime64(1, 'UTC'),
    dt64_3_utc DateTime64(3, 'UTC'), dt64_3_ber DateTime64(3, 'Europe/Berlin'),
    dt64_6_tok DateTime64(6, 'Asia/Tokyo'),
    s String
) ENGINE = MergeTree ORDER BY tuple();`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	return schema
}

// TestDateTimeTimezonePairJoinsInOriginalOrder pins the measured
// order-dependent pair rule through the real greatest() inference path.
// Measured on ClickHouse 25.8.29.51:
//
//	greatest(DateTime('UTC'), DateTime('Europe/Berlin'))   -> DateTime('UTC')
//	greatest(DateTime('Europe/Berlin'), DateTime('UTC'))   -> DateTime('Europe/Berlin')
func TestDateTimeTimezonePairJoinsInOriginalOrder(t *testing.T) {
	schema := dateTimeTimezoneFixtureSchema(t)
	cases := []struct {
		expr string
		want string
	}{
		{"greatest(dt_utc, dt_ber)", "DateTime('UTC')"},
		{"greatest(dt_ber, dt_utc)", "DateTime('Europe/Berlin')"},
		{"greatest(dt_utc, dt_tok)", "DateTime('UTC')"},
		{"greatest(dt_tok, dt_utc)", "DateTime('Asia/Tokyo')"},
	}
	for _, testCase := range cases {
		if got := inferCHTypeString(t, schema, testCase.expr); got != testCase.want {
			t.Errorf("type of %q = %s, want %s", testCase.expr, got, testCase.want)
		}
	}
}

// TestDateTimeTimezonePrecisionWins pins that a higher DateTime64
// precision always beats a lower one, and that the timezone of the
// LAST branch attaining that higher precision wins, even against an
// earlier branch. Measured on ClickHouse 25.8.29.51:
//
//	greatest(DateTime64(3,'UTC'), DateTime64(6,'Asia/Tokyo'))  -> DateTime64(6, 'Asia/Tokyo')
//	greatest(DateTime64(6,'Asia/Tokyo'), DateTime64(3,'UTC'))  -> DateTime64(6, 'Asia/Tokyo')
func TestDateTimeTimezonePrecisionWins(t *testing.T) {
	schema := dateTimeTimezoneFixtureSchema(t)
	cases := []struct {
		expr string
		want string
	}{
		{"greatest(dt64_3_utc, dt64_6_tok)", "DateTime64(6, 'Asia/Tokyo')"},
		{"greatest(dt64_6_tok, dt64_3_utc)", "DateTime64(6, 'Asia/Tokyo')"},
	}
	for _, testCase := range cases {
		if got := inferCHTypeString(t, schema, testCase.expr); got != testCase.want {
			t.Errorf("type of %q = %s, want %s", testCase.expr, got, testCase.want)
		}
	}
}

// TestDateTimeTimezoneTripleLastAtMaxWins pins the triple case, where
// two branches tie at the maximum precision and one does not: the
// timezone of the LAST branch that attains the maximum precision wins,
// regardless of where the lower-precision branch sits. Measured on
// ClickHouse 25.8.29.51:
//
//	greatest(DateTime64(1,'UTC'), DateTime64(3,'UTC'), DateTime64(3,'Europe/Berlin'))
//	                                                    -> DateTime64(3, 'Europe/Berlin')
//	greatest(DateTime64(3,'Europe/Berlin'), DateTime64(3,'UTC'), DateTime64(1,'UTC'))
//	                                                    -> DateTime64(3, 'UTC')
func TestDateTimeTimezoneTripleLastAtMaxWins(t *testing.T) {
	schema := dateTimeTimezoneFixtureSchema(t)
	cases := []struct {
		expr string
		want string
	}{
		{"greatest(dt64_1_utc, dt64_3_utc, dt64_3_ber)", "DateTime64(3, 'Europe/Berlin')"},
		{"greatest(dt64_3_ber, dt64_3_utc, dt64_1_utc)", "DateTime64(3, 'UTC')"},
	}
	for _, testCase := range cases {
		if got := inferCHTypeString(t, schema, testCase.expr); got != testCase.want {
			t.Errorf("type of %q = %s, want %s", testCase.expr, got, testCase.want)
		}
	}
}

// TestDateTimeTimezoneAllAtMaxUsesFirstBranch pins the tie rule: when
// EVERY branch attains the maximum precision, the timezone of the FIRST
// branch wins, not the last. Measured on ClickHouse 25.8.29.51: all
// three orderings of a same-precision triple each keep their own first
// branch's timezone.
func TestDateTimeTimezoneAllAtMaxUsesFirstBranch(t *testing.T) {
	schema := dateTimeTimezoneFixtureSchema(t)
	cases := []struct {
		expr string
		want string
	}{
		{"greatest(dt_utc, dt_ber, dt_tok)", "DateTime('UTC')"},
		{"greatest(dt_ber, dt_tok, dt_utc)", "DateTime('Europe/Berlin')"},
		{"greatest(dt_tok, dt_utc, dt_ber)", "DateTime('Asia/Tokyo')"},
	}
	for _, testCase := range cases {
		if got := inferCHTypeString(t, schema, testCase.expr); got != testCase.want {
			t.Errorf("type of %q = %s, want %s", testCase.expr, got, testCase.want)
		}
	}
}

// TestBareDateTimeCountsAsPrecisionZero pins that a bare DateTime (no
// DateTime64 precision argument) counts as precision 0, so it loses to
// any DateTime64 peer regardless of order. Measured on ClickHouse
// 25.8.29.51:
//
//	greatest(DateTime, DateTime64(3,'UTC'))   -> DateTime64(3, 'UTC')
//	greatest(DateTime64(3,'UTC'), DateTime)   -> DateTime64(3, 'UTC')
func TestBareDateTimeCountsAsPrecisionZero(t *testing.T) {
	schema := dateTimeTimezoneFixtureSchema(t)
	cases := []struct {
		expr string
		want string
	}{
		{"greatest(dtb, dt64_3_utc)", "DateTime64(3, 'UTC')"},
		{"greatest(dt64_3_utc, dtb)", "DateTime64(3, 'UTC')"},
	}
	for _, testCase := range cases {
		if got := inferCHTypeString(t, schema, testCase.expr); got != testCase.want {
			t.Errorf("type of %q = %s, want %s", testCase.expr, got, testCase.want)
		}
	}
}

// TestDateTimeWithNonTemporalStaysRefused is the CONTROL. A DateTime
// with a String peer has no supertype on the server (measured on
// ClickHouse 25.8.29.51: greatest(dt, s) is Code: 386, NO_COMMON_TYPE,
// "some of them are String/FixedString/Enum and some of them are
// not"). The dateTimeTimezoneCHTypes shortcut must decline to handle
// this list (a non-DateTime branch is present) and let it fall through
// to the ordinary pairwise fold, which already refuses it; this test
// pins that the DateTime fix does not accidentally widen the lattice
// beyond what the server accepts.
func TestDateTimeWithNonTemporalStaysRefused(t *testing.T) {
	schema := dateTimeTimezoneFixtureSchema(t)
	for _, expr := range []string{
		"greatest(dt_utc, s)",
		"greatest(s, dt_utc)",
	} {
		if _, err := inferCHTypeStringErr(t, schema, expr); err == nil {
			t.Errorf("type of %q succeeded, want a refusal", expr)
		}
	}
}

// degradedSortedFoldDateTimeCHTypes reproduces the PRE-FIX defect shape
// on purpose: it mimics what a plain pairwise fold under commonCHTypes's
// existing Decimal/Float/everything-else sort would do to a
// DateTime/DateTime64 branch list, by sorting the branches into a FIXED
// canonical order (by type string) before folding pairwise. A rule that
// depends on the caller's original order cannot survive this reorder,
// so any order-sensitive answer collapses to the SAME single result
// regardless of the caller's actual argument order. This function is
// kept only so the self-test below can prove that the permutation sweep
// notices when order information has been thrown away.
func degradedSortedFoldDateTimeCHTypes(types []CHType) (CHType, error) {
	if len(types) == 0 {
		return CHType{}, fmt.Errorf("cannot infer common type from no expressions")
	}
	sorted := make([]CHType, len(types))
	copy(sorted, types)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j-1].String() > sorted[j].String(); j-- {
			sorted[j-1], sorted[j] = sorted[j], sorted[j-1]
		}
	}
	result := sorted[0]
	for _, next := range sorted[1:] {
		var err error
		result, err = commonCHType(result, next)
		if err != nil {
			return CHType{}, err
		}
	}
	return result, nil
}

// TestPermutationSweepNoticesAnOrderInsensitiveDateTimeFold is the
// REQUIRED self-test for an ORDER-DEPENDENT rule: it proves the
// permutation sweep can tell "the fixed fold answers a different
// timezone for a different order, as the server does" apart from "the
// sweep never ran, or the fold silently lost the order information".
//
// It runs the SAME triple, in every permutation, through the FIXED
// commonCHTypes and through degradedSortedFoldDateTimeCHTypes, the
// known-bad shape that a sort-then-fold would produce. It asserts TWO
// things: the fixed fold gives MORE THAN ONE distinct timezone across
// the six permutations (the order dependence is real and visible), and
// the degraded fold gives EXACTLY ONE timezone across all six (the sort
// destroyed that dependence, as it would in a broken implementation).
// If the fixed fold ever collapsed to a single answer regardless of
// order, this test would fail, exactly as required: a lattice that
// ignores order here is not a fix, it is the regression all over again.
func TestPermutationSweepNoticesAnOrderInsensitiveDateTimeFold(t *testing.T) {
	triple := []CHType{
		{Name: "DateTime", LiteralParams: []string{"'UTC'"}},
		{Name: "DateTime", LiteralParams: []string{"'Europe/Berlin'"}},
		{Name: "DateTime", LiteralParams: []string{"'Asia/Tokyo'"}},
	}
	permutations := permuteThreeCHTypes(triple)

	fixedAnswers := map[string]bool{}
	for _, perm := range permutations {
		got, err := commonCHTypes(perm)
		if err != nil {
			t.Fatalf("commonCHTypes(%v) error = %v, want a type", perm, err)
		}
		fixedAnswers[got.String()] = true
	}
	if len(fixedAnswers) < 2 {
		t.Fatalf("the FIXED fold gave only %d distinct answer(s) across all orders: %v; "+
			"this rule is order dependent on the server, so a single answer means the "+
			"fix lost the order information, not that it is correctly order-independent",
			len(fixedAnswers), fixedAnswers)
	}

	// The degraded fold uses commonCHType's plain pairwise DateTime rule,
	// which (before the regression) only accepts an EXACT type-string match
	// and refuses any other timezone pairing outright; after sorting
	// into a fixed canonical order it may thus refuse every permutation
	// instead of answering one fixed type. Both outcomes demonstrate the
	// same defect: the sort throws away the order dependence that the
	// server has, either by forcing a single wrong answer or by forcing
	// a uniform refusal where the server always accepts.
	degradedAnswers := map[string]bool{}
	degradedRefusals := 0
	for _, perm := range permutations {
		got, err := degradedSortedFoldDateTimeCHTypes(perm)
		if err != nil {
			degradedRefusals++
			continue
		}
		degradedAnswers[got.String()] = true
	}
	if degradedRefusals != len(permutations) && len(degradedAnswers) != 1 {
		t.Fatalf("the degraded sort-then-fold shape was expected to collapse to ONE answer "+
			"or refuse uniformly regardless of order, but gave %d distinct answer(s) and "+
			"%d refusal(s) out of %d orders: %v; the self-test's premise does not hold, "+
			"so it cannot demonstrate what a broken (order-blind) fold would look like",
			len(degradedAnswers), degradedRefusals, len(permutations), degradedAnswers)
	}
}
