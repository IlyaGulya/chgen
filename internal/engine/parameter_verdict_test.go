package engine

import (
	"strings"
	"testing"
)

// parameterVerdictSchema holds one column of every parametric family that the
// curated table names, so that a verdict can be exercised over a REAL column
// and never over a literal. The server folds a literal, thus a rule measured
// over one lies.
const parameterVerdictSchema = `
CREATE TABLE probe (
    u8    UInt8,
    i32   Int32,
    dtz   DateTime('UTC'),
    dtz64 DateTime64(3, 'UTC'),
    dec   Decimal(18, 4),
    en    Enum8('a' = 1, 'b' = 2),
    fs    FixedString(8),
    arrdtz Array(DateTime('UTC')),
    m     Map(String, Decimal(18, 4)),
    tp    Tuple(a Int32, b DateTime('UTC'))
) ENGINE = MergeTree ORDER BY u8
`

// TestParameterVerdictRefusesAnUnstatedFamily is the core guarantee of
// the regression: the DEFAULT is REFUSE. A rule that states no verdict for the
// parameter family of its result must refuse, and must never give the
// parameter back unchanged.
//
// Before the mechanism landed this test could not be written: the generic
// function path returned the rule result directly, thus an unstated family
// answered a type whose base kind was right and whose parameter came from the
// argument. This test drives the gate through a rule name that the table does
// not name at all, which is the state that every future rule starts in.
func TestParameterVerdictRefusesAnUnstatedFamily(t *testing.T) {
	unstated := []CHType{
		{Name: "DateTime", LiteralParams: []string{"'UTC'"}},
		{Name: "DateTime64", LiteralParams: []string{"3", "'UTC'"}},
		{Name: "Decimal", LiteralParams: []string{"18", "4"}},
		{Name: "FixedString", LiteralParams: []string{"8"}},
		{Name: "Enum8", LiteralParams: []string{"'a' = 1"}},
		{Name: "Array", Params: []CHType{{Name: "Int32"}}},
		{Name: "Map", Params: []CHType{{Name: "String"}, {Name: "Int64"}}},
		{Name: "Tuple", Params: []CHType{{Name: "Int32"}}},
		{Name: "AggregateFunction", Params: []CHType{{Name: "uniq"}, {Name: "UInt64"}}},
	}
	for _, result := range unstated {
		got, err := applyParameterVerdict("ruleWithNoStatedVerdict", result)
		if err == nil {
			t.Errorf("applyParameterVerdict(unstated rule, %s) = %s, want a refusal: "+
				"an unstated parameter family must refuse and never copy the parameter through",
				result.String(), got.String())
		}
	}
}

// TestParameterVerdictKeepsAParameterlessResult pins the other side of the
// default. A result that carries NO parameter asks no question, thus the gate
// must let it through even for a rule that the table never names. Without
// this the refuse-by-default rule would refuse every scalar result too, which
// is a refusal far wider than the server's.
func TestParameterVerdictKeepsAParameterlessResult(t *testing.T) {
	for _, result := range []CHType{{Name: "UInt64"}, {Name: "String"}, {Name: "Float64"}, {Name: "Date"}} {
		got, err := applyParameterVerdict("ruleWithNoStatedVerdict", result)
		if err != nil {
			t.Errorf("applyParameterVerdictStrict(unstated rule, %s) error = %v, want the type back: "+
				"a result with no parameter asks no question", result.String(), err)
		} else if got.String() != result.String() {
			t.Errorf("applyParameterVerdictStrict(%s) = %s, want it unchanged", result.String(), got.String())
		}
	}
}

func TestLatticeParameterPolicyIsLoadBearing(t *testing.T) {
	result := CHType{Name: "Array", Params: []CHType{{Name: "Int32"}}}
	original := functionRegistry["array"]
	mutated := original
	mutated.parameterPolicy = parameterResultUnknown
	functionRegistry["array"] = mutated
	t.Cleanup(func() { functionRegistry["array"] = original })

	if _, err := applyParameterVerdict("array", result); err == nil {
		t.Error("array with an unknown parameter policy passed the gate, want a refusal")
	}
}

// TestParameterVerdictKeepAndDrop pins the two acting verdicts through the
// mechanism itself, and not through the rule bodies.
//
// The DROP cell is the regression defect: groupUniqArray loses the timezone of
// a bare DateTime leaf. The KEEP cell beside it is the same family under a
// different rule name, which is what shows that the key must carry the rule
// name and not only the family.
func TestParameterVerdictKeepAndDrop(t *testing.T) {
	dateTimeUTC := CHType{Name: "DateTime", LiteralParams: []string{"'UTC'"}}

	kept, err := applyParameterVerdictStrict("max", dateTimeUTC)
	if err != nil {
		t.Fatalf("max / DateTime.timezone: error = %v, want KEEP", err)
	}
	if kept.String() != "DateTime('UTC')" {
		t.Errorf("max / DateTime.timezone = %s, want DateTime('UTC') kept", kept.String())
	}

	dropped, err := applyParameterVerdictStrict("groupuniqarray", dateTimeUTC)
	if err != nil {
		t.Fatalf("groupuniqarray / DateTime.timezone: error = %v, want DROP", err)
	}
	if dropped.String() != "DateTime" {
		t.Errorf("groupuniqarray / DateTime.timezone = %s, want the timezone dropped", dropped.String())
	}
}

// TestValuePreservingRulesRouteThroughTheVerdictTable pins the measured KEEP
// grid end to end, through the resolver and thus through the gate. Every cell
// was measured for this ticket on ClickHouse 25.8.29.51 over the real columns
// of parameterVerdictSchema, with BOTH witnesses agreeing:
// DESCRIBE (SELECT expr FROM probe WHERE 0) over an empty table, and
// SELECT toTypeName(expr) FROM probe over a row.
//
// These are the cells that a copy-through happened to get right. They are
// pinned here so that the refuse-by-default gate cannot silently make the
// rules NARROWER than the server: a refusal wider than the server's breaks a
// query that runs today, which is a defect of its own.
func TestValuePreservingRulesRouteThroughTheVerdictTable(t *testing.T) {
	schema := schemaFromDDL(t, parameterVerdictSchema)
	cases := []struct{ expr, want string }{
		{"max(dtz)", "DateTime('UTC')"},
		{"min(dtz)", "DateTime('UTC')"},
		{"any(dtz)", "DateTime('UTC')"},
		{"anyLast(dtz)", "DateTime('UTC')"},
		{"max(dtz64)", "DateTime64(3, 'UTC')"},
		{"max(dec)", "Decimal(18, 4)"},
		{"max(en)", "Enum8('a' = 1, 'b' = 2)"},
		{"max(fs)", "FixedString(8)"},
		{"max(arrdtz)", "Array(DateTime('UTC'))"},
		{"max(m)", "Map(String, Decimal(18, 4))"},
		{"argMax(dtz, u8)", "DateTime('UTC')"},
		{"argMin(dec, u8)", "Decimal(18, 4)"},
		{"maxIf(dtz, u8 = 5)", "DateTime('UTC')"},
		{"minIf(fs, u8 = 5)", "FixedString(8)"},
		{"arraySort(arrdtz)", "Array(DateTime('UTC'))"},
		{"arraySlice(arrdtz, 1, 1)", "Array(DateTime('UTC'))"},
		{"arrayResize(arrdtz, 2)", "Array(DateTime('UTC'))"},
	}
	for _, testCase := range cases {
		inferred, err := inferTestExprType(t, schema, testCase.expr)
		if err != nil {
			t.Errorf("%s: error = %v, want %s: the server runs this call, thus a refusal here is too wide",
				testCase.expr, err, testCase.want)
			continue
		}
		if inferred != testCase.want {
			t.Errorf("%s = %s, want %s", testCase.expr, inferred, testCase.want)
		}
	}
}

// TestGroupUniqArrayTimezoneDropStillRoutesThroughTheTable pins the regression
// cell after the ad hoc name-shaped check inside
// groupUniqArrayFunctionResult became a table row. The behaviour must be the
// same and it must now come from the verdict, thus this test asserts the
// resolver result AND the table row that produces it.
func TestGroupUniqArrayTimezoneDropStillRoutesThroughTheTable(t *testing.T) {
	if got := parameterVerdictFor("groupuniqarray", familyDateTimeTimezone); got != verdictDrop {
		t.Errorf("parameterVerdictFor(groupuniqarray, %s) = %s, want DROP", familyDateTimeTimezone, got)
	}
	if got := parameterVerdictFor("grouparray", familyArrayElement); got != verdictKeep {
		t.Errorf("parameterVerdictFor(grouparray, %s) = %s, want KEEP", familyArrayElement, got)
	}

	schema := schemaFromDDL(t, parameterVerdictSchema)
	cases := []struct{ expr, want string }{
		{"groupUniqArray(dtz)", "Array(DateTime)"},
		{"groupUniqArrayIf(dtz, u8 = 5)", "Array(DateTime)"},
		{"groupUniqArray(dtz64)", "Array(DateTime64(3, 'UTC'))"},
		{"groupUniqArray(dec)", "Array(Decimal(18, 4))"},
		{"groupUniqArray(en)", "Array(Enum8('a' = 1, 'b' = 2))"},
		{"groupUniqArray(fs)", "Array(FixedString(8))"},
		{"groupArray(dtz)", "Array(DateTime('UTC'))"},
	}
	for _, testCase := range cases {
		inferred, err := inferTestExprType(t, schema, testCase.expr)
		if err != nil {
			t.Errorf("%s: error = %v, want %s", testCase.expr, err, testCase.want)
			continue
		}
		if inferred != testCase.want {
			t.Errorf("%s = %s, want %s", testCase.expr, inferred, testCase.want)
		}
	}
}

// TestParameterFamilyOfNamesEveryParametricType pins the family classifier.
// A parametric type that the switch does not name must still ask a question,
// because an unnamed family cannot be answered and therefore refuses. A
// classifier that answered "not parametric" for an unknown type would let the
// type pass with no verdict, which is the exact hole that this ticket closes.
func TestParameterFamilyOfNamesEveryParametricType(t *testing.T) {
	if _, parametric := parameterFamilyOf(CHType{Name: "UInt64"}); parametric {
		t.Error("parameterFamilyOf(UInt64) says parametric, want not parametric")
	}
	unknown := CHType{Name: "SomeFutureType", LiteralParams: []string{"7"}}
	family, parametric := parameterFamilyOf(unknown)
	if !parametric {
		t.Fatal("parameterFamilyOf(an unknown parametric type) says not parametric, " +
			"want a family so that the pair refuses")
	}
	if !strings.HasPrefix(string(family), "unnamed:") {
		t.Errorf("parameterFamilyOf(an unknown parametric type) = %s, want an unnamed: family", family)
	}
	if _, err := applyParameterVerdictStrict("max", unknown); err == nil {
		t.Error("an unknown parametric type passed the gate for max, want a refusal")
	}
}

// TestUnverifiedParameterCells prints every rule and family pair that the
// curated table leaves unstated. It is the remaining work of the regression as a
// concrete number rather than as an invisible gap.
//
// The test does not FAIL on an unverified cell. An unstated cell already
// refuses at run time, thus the behaviour is safe; the list is bookkeeping. A
// failure here would only say that work remains, which the count already
// says.
func TestUnverifiedParameterCells(t *testing.T) {
	cells := unverifiedParameterCells()
	t.Logf("unverified parameter cells: %d", len(cells))
	for _, cell := range cells {
		t.Logf("  UNVERIFIED %s", cell)
	}
	// Every rule that the table names must state at least one verdict,
	// otherwise the rule is in the table by accident.
	rules := make(map[string]bool)
	for key := range parameterVerdicts {
		rules[key.rule] = true
	}
	if len(rules) == 0 {
		t.Fatal("the verdict table names no rule at all")
	}
	for rule := range rules {
		if !ruleHasAnyVerdict(rule) {
			t.Errorf("rule %s is a key of the table but ruleHasAnyVerdict says no", rule)
		}
	}
}

// TestReachableUnverifiedParameterCells prints the HONEST remaining work of
// the regression (the regression). unverifiedParameterCells counts the cartesian
// product of every wired rule against all 10 families, and most of that
// count is noise: 72 of the 122 cells come from 8 rules (grouparray,
// grouparrayif, abs, negate, sum, sumif, quantilestate, quantilestateif)
// whose OWN registered domain or OWN result shape can never produce 8 of
// the 9 families a verdict cell asks about for them. That noise makes the
// raw count move the WRONG way: wiring a genuinely reachable cell can
// leave 71 fake ones sitting in the same total, and the total can even
// rise when a rule gains a domain that used to accept a wider set.
//
// This test does not FAIL on a nonzero count, for the same reason
// TestUnverifiedParameterCells does not: an unstated cell already refuses
// at run time. It exists so the real remainder is a number someone reads,
// and so a regression in the FILTER itself (not in the underlying cells)
// shows up as a count that moves without an underlying change.
func TestReachableUnverifiedParameterCells(t *testing.T) {
	all := unverifiedParameterCells()
	reachable := reachableUnverifiedParameterCells()
	t.Logf("unverified parameter cells: %d total, %d reachable, %d structurally unreachable",
		len(all), len(reachable), len(all)-len(reachable))
	for _, cell := range reachable {
		t.Logf("  REACHABLE (real remaining work) %s", cell)
	}

	// The 8 rules named in the regression must contribute ZERO reachable
	// cells: every one of their unstated cells was measured, on
	// ClickHouse 25.8.29.51 over real columns, to be a Code 43 refusal
	// (abs, negate, sum, sumif, quantilestate, quantilestateif) or a
	// value the rule's own element-type helper always strips
	// (grouparray, grouparrayif never carry a bare DateTime, Decimal,
	// FixedString, Enum, Map, Tuple or AggregateFunction marker as their
	// OWN family; their family is always Array.element).
	structurallyUnreachableRules := []string{
		"grouparray", "grouparrayif", "abs", "negate",
		"sum", "sumif", "quantilestate", "quantilestateif",
	}
	reachableSet := make(map[string]bool, len(reachable))
	for _, cell := range reachable {
		reachableSet[cell] = true
	}
	for _, cell := range all {
		rule, _, ok := strings.Cut(cell, " / ")
		if !ok {
			continue
		}
		for _, unreachableRule := range structurallyUnreachableRules {
			if rule == unreachableRule && reachableSet[cell] {
				t.Errorf("%s was measured as structurally unreachable but "+
					"reachableUnverifiedParameterCells still reports it: %s", rule, cell)
			}
		}
	}
}

// TestRuleCanReachFamilySelfTest injects two known-wrong reachability
// facts and asserts that ruleCanReachFamily notices them. A check that cannot
// distinguish "no difference" from
// "never ran" is a broken deliverable.
//
// Fault 1: a rule and family pair that is measured REACHABLE (nullif /
// LowCardinality.inner: nullIf(lc, ”) is LowCardinality(Nullable(String))
// on ClickHouse 25.8.29.51) must report true. Flipping the expectation to
// false must fail this test, which shows the function is not just
// returning false unconditionally.
//
// Fault 2: a rule and family pair that is measured UNREACHABLE (sum /
// familyDateTimeTimezone: sum(dtz) is Code 43 on the same server) must
// report false. sumFunctionArgument's own "default: result = first"
// branch would echo the DateTime argument straight back if the domain
// gate were skipped, so this pins that the domain gate actually runs.
func TestRuleCanReachFamilySelfTest(t *testing.T) {
	if !ruleCanReachFamily("nullif", familyLowCardinalityInner) {
		t.Error("self-test fault 1: ruleCanReachFamily(nullif, LowCardinality.inner) = false, want true " +
			"(measured: nullIf(lc, '') is LowCardinality(Nullable(String)))")
	}
	if ruleCanReachFamily("sum", familyDateTimeTimezone) {
		t.Error("self-test fault 2: ruleCanReachFamily(sum, DateTime.timezone) = true, want false " +
			"(measured: sum(dtz) is Code 43; the domain gate must refuse before the rule's " +
			"pass-through default branch ever runs)")
	}
}

// TestUnwiredRuleCount prints the count and the risk class of every
// registry rule that ruleHasAnyVerdict does not name. THE GAP THIS TEST
// CLOSES: applyParameterVerdict skips the gate entirely for a rule with no
// row in the table, thus refuse-by-default holds only INSIDE the wired
// rules. TestUnverifiedParameterCells counts cells that are already safe by
// construction; this test counts the rules that are NOT yet safe, which is
// the real remaining size of the ticket.
//
// The test does not FAIL on a nonzero count. A failure would only repeat
// what the count says, and it would block every unrelated change while the
// rules are wired one measured batch at a time. This is bookkeeping, in the
// same spirit as TestUnverifiedParameterCells.
func TestUnwiredRuleCount(t *testing.T) {
	report := buildUnwiredRuleReport()
	t.Logf("unwired registry rules: %d (nil-rule %d, sized-constructor marker %d, fixed-result %d, lattice-derived %d, parametric %d)",
		report.total(), len(report.nilRule), len(report.sizedMarker), len(report.fixed), len(report.latticeDerived), len(report.parametric))
	t.Logf("  nil-rule (never reaches the gate, no risk):")
	for _, name := range report.nilRule {
		t.Logf("    %s", name)
	}
	t.Logf("  sized-constructor marker (routed away before the registry rule runs, no risk through this gate):")
	for _, name := range report.sizedMarker {
		t.Logf("    %s", name)
	}
	t.Logf("  fixed-result (computes a type, cannot copy a parameter, no risk):")
	for _, name := range report.fixed {
		t.Logf("    %s", name)
	}
	t.Logf("  lattice-derived (result comes from the supertype lattice, a verdict cannot answer for it):")
	for _, name := range report.latticeDerived {
		t.Logf("    %s", name)
	}
	t.Logf("  parametric (result depends on argument type, REMAINING RISK):")
	for _, name := range report.parametric {
		t.Logf("    %s", name)
	}
}

// TestLatticeDerivedClassIsExactlyTheFourMeasuredRules pins the regression
// membership: array, greatest, least and map, and no other registry name.
// A fifth name here, or a missing one, is exactly the kind of drift a
// hand-written list would allow silently; this test catches it because
// isLatticeDerivedRule computes the class fresh from the registry every
// time.
func TestLatticeDerivedClassIsExactlyTheFourMeasuredRules(t *testing.T) {
	report := buildUnwiredRuleReport()
	want := []string{"array", "greatest", "least", "map"}
	if len(report.latticeDerived) != len(want) {
		t.Fatalf("latticeDerived = %v, want exactly %v", report.latticeDerived, want)
	}
	for i, name := range want {
		if report.latticeDerived[i] != name {
			t.Errorf("latticeDerived[%d] = %s, want %s (full list: %v)", i, report.latticeDerived[i], name, report.latticeDerived)
		}
	}
}

// TestIsLatticeDerivedRuleDirectFunctionPointers pins the first half of the
// derivation: arrayFunctionResult and mapFunctionResult are identified by
// comparing function pointers, because registry.go stores them directly as
// spec.rule and not behind a factory closure.
func TestIsLatticeDerivedRuleDirectFunctionPointers(t *testing.T) {
	if !isLatticeDerivedRule("array", arrayFunctionResult) {
		t.Error("isLatticeDerivedRule(array, arrayFunctionResult) = false, want true")
	}
	if !isLatticeDerivedRule("map", mapFunctionResult) {
		t.Error("isLatticeDerivedRule(map, mapFunctionResult) = false, want true")
	}
	if isLatticeDerivedRule("tuple", tupleFunctionResult) {
		t.Error("isLatticeDerivedRule(tuple, tupleFunctionResult) = true, want false: " +
			"tupleFunctionResult does not use the supertype lattice, it keeps every element as its own")
	}
}

// TestIsLatticeDerivedRuleGreatestLeastClosures pins the second half: the
// factory greatestLeastFunctionType returns a closure, so membership comes
// from probing the closure's OUTPUT against greatestLeastCommonCHTypes
// itself, not from a function pointer.
func TestIsLatticeDerivedRuleGreatestLeastClosures(t *testing.T) {
	if !isLatticeDerivedRule("greatest", greatestLeastFunctionType("greatest")) {
		t.Error("isLatticeDerivedRule(greatest, its own closure) = false, want true")
	}
	if !isLatticeDerivedRule("least", greatestLeastFunctionType("least")) {
		t.Error("isLatticeDerivedRule(least, its own closure) = false, want true")
	}
}

// TestIsLatticeDerivedRuleSelfTest injects two known-wrong classifications
// and asserts that the derivation notices them. A check that cannot
// distinguish "no difference" from "never ran" is a
// broken deliverable.
//
// Fault 1: a rule that COPIES its first argument (firstFunctionArgument)
// must NOT classify as lattice-derived, because it answers a copy-through
// question directly and a verdict cell CAN cover it.
//
// Fault 2: greatestLeastFunctionType called with the WRONG name closes
// over a name that greatestLeastCommonCHTypes's signed/UInt64 branch
// treats differently ("least" flips the branch that "greatest" takes),
// so probing the "greatest" closure against the "least" comparison
// target must fail the match for at least one direction of the pair.
func TestIsLatticeDerivedRuleSelfTest(t *testing.T) {
	if isLatticeDerivedRule("max", functionTypeRule(firstFunctionArgument)) {
		t.Error("self-test fault 1 not caught: firstFunctionArgument (a copy-through rule) " +
			"was classified as lattice-derived")
	}

	i64 := CHType{Name: "Int64"}
	u64 := CHType{Name: "UInt64"}
	greatestOnPair, err := greatestLeastCommonCHTypes("greatest", []CHType{i64, u64})
	if err != nil {
		t.Fatalf("greatestLeastCommonCHTypes(greatest, [Int64, UInt64]): %v", err)
	}
	leastOnPair, err := greatestLeastCommonCHTypes("least", []CHType{i64, u64})
	if err != nil {
		t.Fatalf("greatestLeastCommonCHTypes(least, [Int64, UInt64]): %v", err)
	}
	if greatestOnPair.String() == leastOnPair.String() {
		t.Fatalf("greatest and least agree on [Int64, UInt64] (%s), "+
			"the self-test needs a pair where the two names diverge", greatestOnPair.String())
	}
	greatestClosure := greatestLeastFunctionType("greatest")
	if matchesGreatestLeastLattice("least", greatestClosure) {
		t.Error("self-test fault 2 not caught: the \"greatest\" closure matched the \"least\" " +
			"comparison target, although the two names disagree on a signed/UInt64 pair")
	}
}
