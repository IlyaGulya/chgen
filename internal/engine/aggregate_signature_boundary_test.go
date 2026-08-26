package engine

import (
	"strings"
	"testing"
)

// TestCountIfChecksItsOnlyConditionArgument pins the one-argument -If form.
// ClickHouse 25.8.29.51 accepts UInt8 and Bool columns and refuses String and
// Int32 columns with Code 43.
func TestCountIfChecksItsOnlyConditionArgument(t *testing.T) {
	schema, err := schemaFromDDLErr(t, combinatorSchema+`
		CREATE TABLE count_conditions (
			nu8 Nullable(UInt8)
		);`)
	if err != nil {
		t.Fatal(err)
	}
	for _, expression := range []string{"countIf(u8)", "countIf(b)"} {
		got, inferErr := combinatorCHType(t, schema, "t", expression)
		if inferErr != nil || got != "UInt64" {
			t.Errorf("%s = %q, %v, want UInt64", expression, got, inferErr)
		}
	}
	if got, inferErr := combinatorCHType(t, schema, "count_conditions", "countIf(nu8)"); inferErr != nil || got != "UInt64" {
		t.Errorf("countIf(nu8) = %q, %v, want UInt64", got, inferErr)
	}
	for _, expression := range []string{"countIf(s)", "countIf(i32)", "countIf(arr_i)"} {
		if got, inferErr := combinatorCHType(t, schema, "t", expression); inferErr == nil {
			t.Errorf("%s = %s, want a refusal for a non-UInt8 condition", expression, got)
		}
	}
}

// TestDynamicCombinatorSignaturesRefuseWrongArity pins the base argument
// count after each measured suffix changes the call shape.
func TestDynamicCombinatorSignaturesRefuseWrongArity(t *testing.T) {
	schema, err := schemaFromDDLErr(t, combinatorSchema)
	if err != nil {
		t.Fatal(err)
	}
	for _, testCase := range []struct {
		table      string
		expression string
	}{
		{"t", "sumSimpleState(i32, u8)"},
		{"t", "sumState(i32, u8)"},
		{"t", "sumStateIf(i32, u8, b)"},
		{"t", "sumIfState(i32, u8, b)"},
		{"t", "sumStateOrNull(i32, u8)"},
		{"t", "sumArray(arr_i, arr_i)"},
		{"t", "sumArrayIf(arr_i, arr_i, b)"},
		{"t", "sumOrNull(i32, u8)"},
		{"t", "sumOrDefault(i32, u8)"},
		{"t", "sumIf(i32, u8, b)"},
		{"t", "sumIfOrNull(i32, u8, b)"},
		{"t", "sumIfOrDefault(i32, u8, b)"},
		{"t", "sumResample(0, 10, 1)(i32, i32, u8)"},
		{"t", "sumResampleIf(0, 10, 1)(i32, i32, u8, b)"},
		{"agg", "sumMerge(ss, ss)"},
		{"agg", "sumMergeState(ss, ss)"},
		{"agg", "uniqMerge(us, us)"},
		{"agg", "sumIfMerge(sif, sif)"},
		{"agg", "sumIfMergeState(sif, sif)"},
	} {
		if got, inferErr := combinatorCHType(t, schema, testCase.table, testCase.expression); inferErr == nil {
			t.Errorf("%s = %s, want an arity refusal", testCase.expression, got)
		}
	}
}

// TestDynamicCombinatorSignatureControls pins one legal call for every call
// shape that the fail-closed validator handles.
func TestDynamicCombinatorSignatureControls(t *testing.T) {
	schema, err := schemaFromDDLErr(t, combinatorSchema)
	if err != nil {
		t.Fatal(err)
	}
	for _, testCase := range []struct {
		table      string
		expression string
		want       string
	}{
		{"t", "sumSimpleState(i32)", "SimpleAggregateFunction(sum, Int64)"},
		{"t", "sumState(i32)", "AggregateFunction(sum, Int32)"},
		{"t", "sumStateIf(i32, b)", "AggregateFunction(sum, Int32)"},
		{"t", "sumIfState(i32, b)", "AggregateFunction(sumIf, Int32, Bool)"},
		{"t", "sumStateOrNull(i32)", "AggregateFunction(sumOrNull, Int32)"},
		{"t", "sumArray(arr_i)", "Int64"},
		{"t", "sumArrayIf(arr_i, b)", "Int64"},
		{"t", "sumOrNull(i32)", "Nullable(Int64)"},
		{"t", "sumOrDefault(i32)", "Int64"},
		{"t", "sumIf(i32, b)", "Int64"},
		{"t", "sumIfOrNull(i32, b)", "Nullable(Int64)"},
		{"t", "sumIfOrDefault(i32, b)", "Int64"},
		{"t", "sumResample(0, 10, 1)(i32, i32)", "Array(Int64)"},
		{"t", "sumResampleIf(0, 10, 1)(i32, i32, b)", "Array(Int64)"},
		{"agg", "sumMerge(ss)", "Int64"},
		{"agg", "sumMergeState(ss)", "AggregateFunction(sum, Int32)"},
		{"agg", "uniqMerge(us)", "UInt64"},
		{"agg", "sumIfMerge(sif)", "Int64"},
		{"agg", "sumIfMergeState(sif)", "AggregateFunction(sumIf, Int32, UInt8)"},
	} {
		got, inferErr := combinatorCHType(t, schema, testCase.table, testCase.expression)
		if inferErr != nil || got != testCase.want {
			t.Errorf("%s = %q, %v, want %s", testCase.expression, got, inferErr, testCase.want)
		}
	}
}

// TestDynamicCombinatorChecksArrayAndResampleRoles pins the argument roles
// that do not belong to the base aggregate.
func TestDynamicCombinatorChecksArrayAndResampleRoles(t *testing.T) {
	schema, err := schemaFromDDLErr(t, combinatorSchema)
	if err != nil {
		t.Fatal(err)
	}
	for _, expression := range []string{
		"sumArray(i32)",
		"sumResample(0, 10, 1)(i32, s)",
		"sumResample(0, 10, 1)(i32, f32)",
		"sumResample(0, 10, 0)(i32, i32)",
		"sumResample(0, 10)(i32, i32)",
		"sumResample(0, 10, 1, 2)(i32, i32)",
		"sumResample(0, 1048577, 1)(i32, i32)",
	} {
		if got, inferErr := combinatorCHType(t, schema, "t", expression); inferErr == nil {
			t.Errorf("%s = %s, want a role or parameter refusal", expression, got)
		}
	}
	for _, expression := range []string{
		"sumResample(-10, 10, 1)(i32, i32)",
		"sumResample(10, 0, 1)(i32, i32)",
		"sumResample(0, 1048576, 1)(i32, i32)",
		"sumResample(0, 10, 1)(i32, ni32)",
	} {
		if _, inferErr := combinatorCHType(t, schema, "t", expression); inferErr != nil {
			t.Errorf("%s: %v, want a type", expression, inferErr)
		}
	}
}

// TestDynamicMergeChecksTheStoredStateShape pins the state arity in addition
// to the aggregate name. A schema annotation can name a state shape that a
// live server would not permit, and inference must not treat it as evidence.
func TestDynamicMergeChecksTheStoredStateShape(t *testing.T) {
	schema, err := schemaFromDDLErr(t, combinatorSchema+`
		CREATE TABLE bad_agg (
			bad_sum AggregateFunction(sum, Int32, UInt8),
			bad_sumif AggregateFunction(sumIf, Int32),
			bad_sumif_condition AggregateFunction(sumIf, Int32, String)
		);`)
	if err != nil {
		t.Fatal(err)
	}
	for _, expression := range []string{
		"sumMerge(bad_sum)",
		"sumIfMerge(bad_sumif)",
		"sumIfMerge(bad_sumif_condition)",
	} {
		if got, inferErr := combinatorCHType(t, schema, "bad_agg", expression); inferErr == nil {
			t.Errorf("%s = %s, want a refusal for an invalid stored state shape", expression, got)
		}
	}
}

// TestUnmodeledCombinatorChainsStayRefused pins the fail-closed boundary. Each
// call is legal on the measured server, but its chain has no current model.
func TestUnmodeledCombinatorChainsStayRefused(t *testing.T) {
	schema, err := schemaFromDDLErr(t, combinatorSchema)
	if err != nil {
		t.Fatal(err)
	}
	for _, expression := range []string{
		"sumArrayState(arr_i)",
		"sumDistinct(i32)",
		"sumForEach(arr_i)",
		"sumArgMax(i32, i32)",
	} {
		if got, inferErr := combinatorCHType(t, schema, "t", expression); inferErr == nil {
			t.Errorf("%s = %s, want an explicit refusal for an unmodeled chain", expression, got)
		} else if !strings.Contains(inferErr.Error(), "no measured aggregate combinator chain") {
			t.Errorf("%s refusal = %v, want the unmodeled-rule boundary", expression, inferErr)
		}
	}
}

// TestAggregateSizeParametersMustBePositive pins the server boundary for the
// optional size parameter of groupArray and groupUniqArray.
func TestAggregateSizeParametersMustBePositive(t *testing.T) {
	schema, err := schemaFromDDLErr(t, combinatorSchema)
	if err != nil {
		t.Fatal(err)
	}
	for _, expression := range []string{
		"groupArray(1)(i32)",
		"groupArrayIf(1)(i32, b)",
		"groupUniqArray(1)(i32)",
		"groupUniqArrayIf(1)(i32, b)",
	} {
		if _, inferErr := combinatorCHType(t, schema, "t", expression); inferErr != nil {
			t.Errorf("%s: %v, want a type", expression, inferErr)
		}
	}
	for _, expression := range []string{
		"groupArray(0)(i32)",
		"groupArray(-1)(i32)",
		"groupArrayIf(0)(i32, b)",
		"groupArrayIf(-1)(i32, b)",
		"groupUniqArray(0)(i32)",
		"groupUniqArray(-1)(i32)",
		"groupUniqArrayIf(0)(i32, b)",
		"groupUniqArrayIf(-1)(i32, b)",
	} {
		if got, inferErr := combinatorCHType(t, schema, "t", expression); inferErr == nil {
			t.Errorf("%s = %s, want a positive-size refusal", expression, got)
		}
	}
}
