package engine

import (
	"strings"
	"testing"
)

const jx2kSumVariantSchema = `
CREATE TABLE probe (
    b Bool,
    i32 Int32,
    u32 UInt32,
    f64 Float64,
    dec Decimal(18, 4),
    ni32 Nullable(Int32),
    nf64 Nullable(Float64),
    variant Variant(Int32, String),
    variant_peer Variant(Int32, String),
    dyn Dynamic
) ENGINE = MergeTree ORDER BY tuple()
`

func jx2kSumVariantTestSchema(t *testing.T) *Schema {
	t.Helper()
	schema, err := schemaFromDDLErr(t, jx2kSumVariantSchema)
	if err != nil {
		t.Fatalf("parse the sum and comparison boundary schema: %v", err)
	}
	return schema
}

// TestSumSimpleStateKeepsInheritedNullableInside checks the boundary
// between a NULL from CASE and a Nullable value from the selected branch.
func TestSumSimpleStateKeepsInheritedNullableInside(t *testing.T) {
	schema := jx2kSumVariantTestSchema(t)
	for _, testCase := range []struct {
		expression string
		want       string
	}{
		{"sumSimpleState(CASE WHEN ('abc' > '%a%') THEN (nf64 / u32) END)", "SimpleAggregateFunction(sum, Nullable(Float64))"},
		{"sumSimpleState(CASE WHEN b THEN (nf64 / u32) END)", "Nullable(SimpleAggregateFunction(sum, Float64))"},
		{"sumSimpleState(CASE WHEN b THEN f64 END)", "Nullable(SimpleAggregateFunction(sum, Float64))"},
		{"sumSimpleState(CASE WHEN b THEN u32 END)", "Nullable(SimpleAggregateFunction(sum, UInt64))"},
		{"sumSimpleState(CASE WHEN b THEN dec END)", "Nullable(SimpleAggregateFunction(sum, Decimal(38, 4)))"},
		{"sumSimpleState(nf64)", "SimpleAggregateFunction(sum, Nullable(Float64))"},
		{"sumSimpleState(ni32)", "SimpleAggregateFunction(sum, Nullable(Int64))"},
		{"sumSimpleState(CASE WHEN b THEN nf64 ELSE f64 END)", "SimpleAggregateFunction(sum, Nullable(Float64))"},
		{"toString(sumSimpleState(CASE WHEN ('abc' > '%a%') THEN (nf64 / u32) END))", "Nullable(String)"},
	} {
		inferred, err := inferTestExprType(t, schema, testCase.expression)
		if err != nil {
			t.Errorf("%s: want %s, got refusal %v", testCase.expression, testCase.want, err)
		} else if inferred != testCase.want {
			t.Errorf("%s: want %s, got %s", testCase.expression, testCase.want, inferred)
		}
	}
}

// TestVariantDynamicComparisonRefuses checks all shared comparison routes.
// The server can analyze this pair, but it refuses the value operation.
func TestVariantDynamicComparisonRefuses(t *testing.T) {
	schema := jx2kSumVariantTestSchema(t)
	for _, pair := range [][2]string{{"variant", "dyn"}, {"dyn", "variant"}} {
		for _, operator := range []string{"=", "!=", "<", "<=", ">", ">="} {
			expression := pair[0] + " " + operator + " " + pair[1]
			assertComparisonRefusal(t, schema, expression)
		}
		for _, function := range []string{"equals", "notEquals", "less", "lessOrEquals", "greater", "greaterOrEquals"} {
			expression := function + "(" + pair[0] + ", " + pair[1] + ")"
			assertComparisonRefusal(t, schema, expression)
		}
	}
	assertComparisonRefusal(t, schema, "if(b, greaterOrEquals(variant, dyn), b)")
}

// TestVariantDynamicComparisonKeepsLegalNeighbors checks the narrow controls.
func TestVariantDynamicComparisonKeepsLegalNeighbors(t *testing.T) {
	schema := jx2kSumVariantTestSchema(t)
	for _, testCase := range []struct {
		expression string
		want       string
	}{
		{"variant = variant_peer", "UInt8"},
		{"greaterOrEquals(variant, variant_peer)", "UInt8"},
		{"dyn = i32", "Nullable(UInt8)"},
		{"i32 = dyn", "Nullable(UInt8)"},
		{"greaterOrEquals(dyn, i32)", "Nullable(UInt8)"},
		{"greaterOrEquals(i32, dyn)", "Nullable(UInt8)"},
		{"dyn = ni32", "Nullable(UInt8)"},
		{"ni32 = dyn", "Nullable(UInt8)"},
		{"greaterOrEquals(dyn, ni32)", "Nullable(UInt8)"},
		{"greaterOrEquals(ni32, dyn)", "Nullable(UInt8)"},
	} {
		inferred, err := inferTestExprType(t, schema, testCase.expression)
		if err != nil {
			t.Errorf("%s: want %s, got refusal %v", testCase.expression, testCase.want, err)
		} else if inferred != testCase.want {
			t.Errorf("%s: want %s, got %s", testCase.expression, testCase.want, inferred)
		}
	}
}

func assertComparisonRefusal(t *testing.T, schema *Schema, expression string) {
	t.Helper()
	inferred, err := inferTestExprType(t, schema, expression)
	if err == nil {
		t.Errorf("%s: want a refusal, got %s", expression, inferred)
	} else if !strings.Contains(err.Error(), "cannot compare") {
		t.Errorf("%s: want a comparison refusal, got %v", expression, err)
	}
}
