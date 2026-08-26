package engine

import (
	"strings"
	"testing"
)

const jx2kBoundarySchema = `
CREATE TABLE probe (
    b Bool,
    nb Nullable(Bool),
    i8 Int8,
    u8 UInt8,
    i32 Int32,
    ni32 Nullable(Int32),
    f64 Float64,
    dec Decimal(18, 4),
    ns Nullable(String),
    e8 Enum8('a' = 1, 'b' = 2),
    sagg SimpleAggregateFunction(sum, Int64),
    arr_i Array(Int32),
    arr_ni Array(Nullable(Int32)),
    arr_b Array(Bool),
    arr_nb Array(Nullable(Bool))
) ENGINE = MergeTree ORDER BY tuple()
`

func jx2kBoundaryType(t *testing.T, expression string) (string, error) {
	t.Helper()
	schema, err := schemaFromDDLErr(t, jx2kBoundarySchema)
	if err != nil {
		t.Fatalf("parse the boundary schema: %v", err)
	}
	return inferTestExprType(t, schema, expression)
}

func TestSumWithOverflowNormalizesBoolOnly(t *testing.T) {
	tests := map[string]string{
		"sumWithOverflow(b)":    "UInt8",
		"sumWithOverflow(nb)":   "Nullable(UInt8)",
		"sumWithOverflow(i8)":   "Int8",
		"sumWithOverflow(u8)":   "UInt8",
		"sumWithOverflow(i32)":  "Int32",
		"sumWithOverflow(ni32)": "Nullable(Int32)",
		"sumWithOverflow(f64)":  "Float64",
		"sumWithOverflow(dec)":  "Decimal(18, 4)",
		"sumWithOverflow(e8)":   "Int8",
	}
	for expression, want := range tests {
		got, err := jx2kBoundaryType(t, expression)
		if err != nil {
			t.Errorf("%s: ClickHouse accepts this call, but chgen refused it: %v", expression, err)
			continue
		}
		if got != want {
			t.Errorf("%s: got %s, want %s", expression, got, want)
		}
	}
}

func TestSumWithOverflowKeepsNeighborRules(t *testing.T) {
	tests := map[string]string{
		"sum(b)":         "UInt64",
		"groupBitAnd(b)": "UInt8",
		"groupBitOr(b)":  "UInt8",
		"groupBitXor(b)": "UInt8",
		"sumWithOverflow(b) + sumWithOverflow(i32)":        "Int64",
		"tuple(sumWithOverflow(b), sumWithOverflow(ni32))": "Tuple(UInt8, Nullable(Int32))",
	}
	for expression, want := range tests {
		got, err := jx2kBoundaryType(t, expression)
		if err != nil {
			t.Errorf("%s: ClickHouse accepts this expression, but chgen refused it: %v", expression, err)
			continue
		}
		if got != want {
			t.Errorf("%s: got %s, want %s", expression, got, want)
		}
	}
}

func TestPredicateLambdaBoundaryAndPropagation(t *testing.T) {
	accepted := map[string]string{
		"arrayCount(x -> x > 0, arr_i)":   "UInt32",
		"arrayCount(x -> x > 0, arr_ni)":  "UInt32",
		"arrayCount(x -> x, arr_b)":       "UInt32",
		"arrayCount(x -> x, arr_nb)":      "UInt32",
		"arrayExists(x -> x > 0, arr_ni)": "UInt8",
		"arrayAll(x -> x, arr_nb)":        "UInt8",
		"arrayFilter(x -> x > 0, arr_ni)": "Array(Nullable(Int32))",
		"arraySort(x -> x, arr_i)":        "Array(Int32)",
		"arrayMap(x -> x + 1, arr_i)":     "Array(Int64)",
	}
	for expression, want := range accepted {
		got, err := jx2kBoundaryType(t, expression)
		if err != nil {
			t.Errorf("%s: ClickHouse accepts this expression, but chgen refused it: %v", expression, err)
			continue
		}
		if got != want {
			t.Errorf("%s: got %s, want %s", expression, got, want)
		}
	}

	refused := []string{
		"arrayCount(x -> x + 1, arr_i)",
		"arrayCount(x -> x + 1, arr_ni)",
		"arrayExists(x -> x, arr_i)",
		"arrayAll(x -> toInt8(x), arr_i)",
		"arrayFilter(x -> x, arr_i)",
		"arrayCount(x -> x + 1, arr_i) + 1",
		"sum(arrayCount(x -> x + 1, arr_i))",
	}
	for _, expression := range refused {
		got, err := jx2kBoundaryType(t, expression)
		if err == nil {
			t.Errorf("%s: chgen gave the type %s, but ClickHouse refuses the expression with Code: 43", expression, got)
			continue
		}
		if !strings.Contains(err.Error(), "lambda body") {
			t.Errorf("%s: the refusal must name the lambda body, got %q", expression, err)
		}
	}
}

func TestPredicateLambdaRosterKeepsEveryMeasuredFunction(t *testing.T) {
	for _, name := range []string{"arraycount", "arrayexists", "arrayall", "arrayfilter"} {
		if rule, ok := higherOrderArrayFunctions[name]; !ok || rule.bodyDomain != hofBodyDomainPredicate {
			t.Errorf("function %s must keep the predicate-body check", name)
		}
	}
	if rule := higherOrderArrayFunctions["arraysort"]; rule.bodyDomain == hofBodyDomainPredicate {
		t.Error("arraySort must not use the predicate-body check")
	}
}

func TestPredicateLambdaRefusalSurvivesAComparisonCondition(t *testing.T) {
	expression := "sumIf(sagg, (arrayCount(x -> (x + 1), arr_i) = (dec / dec))) * " +
		"sumIf(length(concat('abc', ns)), (false AND (f64 <= u8)))"
	got, err := jx2kBoundaryType(t, expression)
	if err == nil {
		t.Fatalf("chgen gave the parent type %s, but ClickHouse refuses the arrayCount lambda with Code: 43", got)
	}
	if !strings.Contains(err.Error(), "arrayCount") || !strings.Contains(err.Error(), "lambda body") {
		t.Fatalf("the refusal must name the arrayCount lambda body, got %q", err)
	}
}

func TestIfConditionErrorPropagationKeepsThePlaceholderControl(t *testing.T) {
	got, err := jx2kBoundaryType(t, "sumIf(i32, ?)")
	if err != nil {
		t.Fatalf("sumIf(i32, ?): the unknown placeholder must stay accepted, got %v", err)
	}
	if got != "Int64" {
		t.Fatalf("sumIf(i32, ?): got %s, want Int64", got)
	}

	got, err = jx2kBoundaryType(t, "sumIf(i32, bogusFunction(i32))")
	if err == nil {
		t.Fatalf("chgen gave the type %s after the condition inference failed", got)
	}
	if !strings.Contains(err.Error(), "condition") || !strings.Contains(err.Error(), "bogusFunction") {
		t.Fatalf("the refusal must keep the condition cause, got %q", err)
	}
}
