package engine

import (
	"reflect"
	"strings"
	"testing"
)

func TestAggregatePrimitiveRosterMatchesMeasuredServer(t *testing.T) {
	want := []string{
		"ArgMax", "ArgMin", "Array", "Distinct", "ForEach", "If", "Map",
		"Merge", "Null", "OrDefault", "OrNull", "Resample", "SimpleState", "State",
	}
	got := aggregatePrimitiveSpellings()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("aggregate primitive roster = %v, want %v", got, want)
	}
}

func TestMeasuredAggregateChainsParseToArgumentPlans(t *testing.T) {
	testCases := []struct {
		name            string
		primitives      []string
		condition       bool
		arrayData       bool
		resampleKey     bool
		stateArgument   bool
		stateCondition  bool
		storesCondition bool
		dropsNullable   bool
	}{
		{name: "sumSimpleState", primitives: []string{"SimpleState"}},
		{name: "sumState", primitives: []string{"State"}},
		{name: "sumStateIf", primitives: []string{"State", "If"}, condition: true, dropsNullable: true},
		{name: "sumIfState", primitives: []string{"If", "State"}, condition: true, storesCondition: true},
		{name: "sumMerge", primitives: []string{"Merge"}, stateArgument: true},
		{name: "sumMergeState", primitives: []string{"Merge", "State"}, stateArgument: true},
		{name: "sumIfMerge", primitives: []string{"If", "Merge"}, stateArgument: true, stateCondition: true},
		{name: "sumIfMergeState", primitives: []string{"If", "Merge", "State"}, stateArgument: true, stateCondition: true},
		{name: "sumStateOrNull", primitives: []string{"State", "OrNull"}},
		{name: "sumOrNull", primitives: []string{"OrNull"}},
		{name: "sumOrDefault", primitives: []string{"OrDefault"}},
		{name: "sumResample", primitives: []string{"Resample"}, resampleKey: true},
		{name: "sumResampleIf", primitives: []string{"Resample", "If"}, condition: true, resampleKey: true},
		{name: "sumArray", primitives: []string{"Array"}, arrayData: true},
		{name: "sumArrayIf", primitives: []string{"Array", "If"}, condition: true, arrayData: true},
		{name: "sumIf", primitives: []string{"If"}, condition: true},
		{name: "sumIfOrNull", primitives: []string{"If", "OrNull"}, condition: true},
		{name: "sumIfOrDefault", primitives: []string{"If", "OrDefault"}, condition: true},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			base, chain, status := parseAggregateCombinatorChain(strings.ToLower(testCase.name))
			if status != aggregateChainSupported {
				t.Fatalf("parse status = %v, want supported", status)
			}
			if base != "sum" {
				t.Fatalf("base = %q, want sum", base)
			}
			if got := chain.primitiveSpellings(); !reflect.DeepEqual(got, testCase.primitives) {
				t.Fatalf("primitives = %v, want %v", got, testCase.primitives)
			}
			plan := chain.plan
			if plan.conditionArgument != testCase.condition ||
				plan.arrayDataArguments != testCase.arrayData ||
				plan.resampleKeyArgument != testCase.resampleKey ||
				plan.stateArgument != testCase.stateArgument ||
				plan.stateHasCondition != testCase.stateCondition ||
				plan.stateStoresCondition != testCase.storesCondition ||
				plan.dropsTopLevelNullable != testCase.dropsNullable {
				t.Fatalf("argument plan = %+v", plan)
			}
		})
	}
}

func TestAggregateChainParserUsesTheLongestBaseName(t *testing.T) {
	base, chain, status := parseAggregateCombinatorChain("anylaststate")
	if status != aggregateChainSupported || base != "anylast" || chain.suffix != "state" {
		t.Fatalf("parse = base %q suffix %q status %v, want anylast/state/supported", base, chain.suffix, status)
	}
}

func TestUnmeasuredPrimitiveChainsRefuseBeforeArgumentInference(t *testing.T) {
	schema, err := schemaFromDDLErr(t, combinatorSchema)
	if err != nil {
		t.Fatal(err)
	}
	for _, expression := range []string{
		"sumDistinct(missing_column)",
		"sumArrayState(missing_column)",
		"sumForEach(missing_column)",
		"sumArgMax(missing_column, missing_column)",
		"sumArgMin(missing_column, missing_column)",
		"sumMap(missing_column)",
		"sumMergeIf(missing_column)",
	} {
		_, inferErr := combinatorCHType(t, schema, "t", expression)
		if inferErr == nil || !strings.Contains(inferErr.Error(), "has no measured aggregate combinator chain") {
			t.Errorf("%s error = %v, want the primitive-chain refusal", expression, inferErr)
		}
	}
}

func TestEveryMeasuredAggregateChainRoundTripsThroughTheParser(t *testing.T) {
	seen := map[string]bool{}
	for _, chain := range aggregateCombinators {
		if seen[chain.suffix] {
			t.Fatalf("duplicate measured chain %q", chain.suffix)
		}
		seen[chain.suffix] = true
		base, parsed, status := parseAggregateCombinatorChain("sum" + chain.suffix)
		if status != aggregateChainSupported || base != "sum" || parsed.suffix != chain.suffix {
			t.Errorf("chain %q does not round trip: base=%q suffix=%q status=%v", chain.suffix, base, parsed.suffix, status)
		}
	}
	if len(seen) != 18 {
		t.Fatalf("measured chain count = %d, want 18", len(seen))
	}
}

func TestArrayPrimitiveNeedsOneDataArgument(t *testing.T) {
	schema, err := schemaFromDDLErr(t, combinatorSchema)
	if err != nil {
		t.Fatal(err)
	}
	for _, expression := range []string{"countArray()", "countArrayIf(b)"} {
		if got, inferErr := combinatorCHType(t, schema, "t", expression); inferErr == nil {
			t.Errorf("%s = %s, want the Array primitive arity refusal", expression, got)
		}
	}
}

func TestRegisteredPrimitiveAggregateKeepsItsMeasuredRule(t *testing.T) {
	schema, err := schemaFromDDLErr(t, combinatorSchema)
	if err != nil {
		t.Fatal(err)
	}
	got, inferErr := combinatorCHType(t, schema, "t", "countDistinct(i32)")
	if inferErr != nil || got != "UInt64" {
		t.Fatalf("countDistinct(i32) = %q, %v, want UInt64", got, inferErr)
	}
}

func TestCountZeroDataChainsKeepTheirMeasuredShapes(t *testing.T) {
	schema, err := schemaFromDDLErr(t, combGridSchemaDDL())
	if err != nil {
		t.Fatal(err)
	}
	testCases := map[string]string{
		"countStateIf(c_bool)":           "AggregateFunction(count)",
		"countIfState(c_bool)":           "AggregateFunction(countIf, Bool)",
		"countIfOrNull(c_bool)":          "Nullable(UInt64)",
		"countIfOrDefault(c_bool)":       "UInt64",
		"countMerge(c_state_count)":      "UInt64",
		"countMergeState(c_state_count)": "AggregateFunction(count)",
	}
	for expression, want := range testCases {
		got, inferErr := combinatorCHType(t, schema, "cg", expression)
		if inferErr != nil || got != want {
			t.Errorf("%s = %q, %v, want %s", expression, got, inferErr, want)
		}
	}
}

func TestArgumentPlanMutationsReachProductionDispatch(t *testing.T) {
	schema, err := schemaFromDDLErr(t, combGridSchemaDDL())
	if err != nil {
		t.Fatal(err)
	}
	testCases := []struct {
		name       string
		suffix     string
		expression string
		mutate     func(*aggregateArgumentPlan)
	}{
		{name: "condition role", suffix: "resampleif", expression: "sumResampleIf(0, 10, 1)(c_i32, c_u8, c_bool)", mutate: func(plan *aggregateArgumentPlan) { plan.conditionArgument = false }},
		{name: "array role", suffix: "array", expression: "sumArray(c_arr_i32)", mutate: func(plan *aggregateArgumentPlan) { plan.arrayDataArguments = false }},
		{name: "resample key role", suffix: "resample", expression: "sumResample(0, 10, 1)(c_i32, c_u8)", mutate: func(plan *aggregateArgumentPlan) { plan.resampleKeyArgument = false }},
		{name: "state argument role", suffix: "merge", expression: "sumMerge(c_state_sum)", mutate: func(plan *aggregateArgumentPlan) { plan.stateArgument = false }},
		{name: "conditional state role", suffix: "ifmerge", expression: "sumIfMerge(c_state_sum_if)", mutate: func(plan *aggregateArgumentPlan) { plan.stateHasCondition = false }},
		{name: "stored condition role", suffix: "ifstate", expression: "sumIfState(c_i32, c_bool)", mutate: func(plan *aggregateArgumentPlan) { plan.stateStoresCondition = false }},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			index := aggregateCombinatorIndex(t, testCase.suffix)
			original := aggregateCombinators[index]
			t.Cleanup(func() { aggregateCombinators[index] = original })
			testCase.mutate(&aggregateCombinators[index].plan)
			if got, inferErr := combinatorCHType(t, schema, "cg", testCase.expression); inferErr == nil || !strings.Contains(inferErr.Error(), "argument plan") {
				t.Fatalf("mutated %s = %q, %v, want an argument-plan refusal", testCase.expression, got, inferErr)
			}
		})
	}
}

func aggregateCombinatorIndex(t *testing.T, suffix string) int {
	t.Helper()
	for index, combinator := range aggregateCombinators {
		if combinator.suffix == suffix {
			return index
		}
	}
	t.Fatalf("aggregate combinator %s is not measured", suffix)
	return -1
}
