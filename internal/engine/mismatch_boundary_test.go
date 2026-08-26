package engine

import "testing"

const mismatchBoundarySchema = `
CREATE TABLE probe (
    s String,
    lc LowCardinality(String),
    lcn LowCardinality(Nullable(String)),
    i32 Int32,
    ni32 Nullable(Int32)
) ENGINE = MergeTree ORDER BY tuple()
`

func mismatchTestSchema(t *testing.T) *Schema {
	t.Helper()
	schema, err := schemaFromDDLErr(t, mismatchBoundarySchema)
	if err != nil {
		t.Fatalf("parse the mismatch schema: %v", err)
	}
	return schema
}

// TestSumSimpleStatePlacesImplicitCaseNullOutside checks the measured
// wrapper boundary. A CASE with no ELSE introduces its own NULL, and sum
// puts that Nullable outside the SimpleAggregateFunction marker. A direct
// Nullable column and a branch that inherits Nullable keep it inside.
func TestSumSimpleStatePlacesImplicitCaseNullOutside(t *testing.T) {
	schema := mismatchTestSchema(t)
	for _, testCase := range []struct {
		expression string
		want       string
	}{
		{"sumSimpleState(CASE WHEN i32 > 0 THEN i32 END)", "Nullable(SimpleAggregateFunction(sum, Int64))"},
		{"sumSimpleState(CASE WHEN i32 > 0 THEN ni32 END)", "Nullable(SimpleAggregateFunction(sum, Int64))"},
		{"sumSimpleState(ni32)", "SimpleAggregateFunction(sum, Nullable(Int64))"},
		{"sumSimpleState(if(i32 > 0, ni32, i32))", "SimpleAggregateFunction(sum, Nullable(Int64))"},
		{"sumSimpleState(CASE WHEN i32 > 0 THEN ni32 ELSE i32 END)", "SimpleAggregateFunction(sum, Nullable(Int64))"},
		{"sumSimpleState(i32)", "SimpleAggregateFunction(sum, Int64)"},
		{"toString(sumSimpleState(CASE WHEN i32 > 0 THEN i32 END))", "Nullable(String)"},
	} {
		inferred, err := inferTestExprType(t, schema, testCase.expression)
		if err != nil {
			t.Errorf("%s: want %s, got refusal %v", testCase.expression, testCase.want, err)
		} else if inferred != testCase.want {
			t.Errorf("%s: want %s, got %s", testCase.expression, testCase.want, inferred)
		}
	}
}

// TestConcatKeepsLowCardinalityForConstantCase checks that a CASE is a
// constant only when all its explicit expressions are constants. A column
// condition or a column value removes the LowCardinality result wrapper.
func TestConcatKeepsLowCardinalityForConstantCase(t *testing.T) {
	schema := mismatchTestSchema(t)
	for _, testCase := range []struct {
		expression string
		want       string
	}{
		{"lower(lcn) || (CASE WHEN true THEN 'abc' END)", "LowCardinality(Nullable(String))"},
		{"lcn || (CASE WHEN true THEN 'abc' ELSE 'def' END)", "LowCardinality(Nullable(String))"},
		{"lcn || (CASE 'x' WHEN 'x' THEN 'abc' ELSE 'def' END)", "LowCardinality(Nullable(String))"},
		{"lcn || (CASE WHEN i32 > 0 THEN 'abc' END)", "Nullable(String)"},
		{"lcn || (CASE WHEN true THEN s END)", "Nullable(String)"},
		{"lcn || (CASE WHEN true THEN 'abc' ELSE s END)", "Nullable(String)"},
		{"lcn || s", "Nullable(String)"},
		{"length(lower(lcn) || (CASE WHEN true THEN 'abc' END))", "LowCardinality(Nullable(UInt64))"},
	} {
		inferred, err := inferTestExprType(t, schema, testCase.expression)
		if err != nil {
			t.Errorf("%s: want %s, got refusal %v", testCase.expression, testCase.want, err)
		} else if inferred != testCase.want {
			t.Errorf("%s: want %s, got %s", testCase.expression, testCase.want, inferred)
		}
	}
}
