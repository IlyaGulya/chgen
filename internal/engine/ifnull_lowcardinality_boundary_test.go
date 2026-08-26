package engine

import "testing"

const ifNullLowCardinalityBoundarySchema = `
CREATE TABLE probe (
    i16 Int16,
    u64 UInt64,
    i128 Int128,
    s String,
    lc LowCardinality(String),
    lcn LowCardinality(Nullable(String))
) ENGINE = MergeTree ORDER BY tuple()
`

func ifNullLowCardinalityTestSchema(t *testing.T) *Schema {
	t.Helper()
	schema, err := schemaFromDDLErr(t, ifNullLowCardinalityBoundarySchema)
	if err != nil {
		t.Fatalf("parse the ifNull LowCardinality schema: %v", err)
	}
	return schema
}

// TestIfNullKeepsLowCardinalityWithAConstantFallback checks the minimal
// cause of the seed 303 mismatch. The first branch carries LowCardinality.
// A constant fallback can widen its base type, but it does not remove the
// wrapper. A parent arithmetic call keeps the same measured wrapper.
func TestIfNullKeepsLowCardinalityWithAConstantFallback(t *testing.T) {
	schema := ifNullLowCardinalityTestSchema(t)
	for _, testCase := range []struct {
		expression string
		want       string
	}{
		{"ifNull(length(lc), toUInt64(1))", "LowCardinality(UInt64)"},
		{"ifNull(nullIf(length(lc), toUInt64(1)), toUInt64(1))", "LowCardinality(UInt64)"},
		{"ifNull(nullIf(length(lc), -(4000000000)), toInt128(CAST(70000 AS UInt64)))", "LowCardinality(Int128)"},
		{"ifNull(nullIf(length(lc), -(4000000000)), toInt128(CAST(70000 AS UInt64))) + 1", "LowCardinality(Int128)"},
	} {
		inferred, err := inferTestExprType(t, schema, testCase.expression)
		if err != nil {
			t.Errorf("%s: want %s, got refusal %v", testCase.expression, testCase.want, err)
		} else if inferred != testCase.want {
			t.Errorf("%s: want %s, got %s", testCase.expression, testCase.want, inferred)
		}
	}
}

// TestIfNullDropsLowCardinalityWithAColumnFallback checks the other side of
// the boundary. A second non-constant column removes LowCardinality. The
// String case checks the same rule without a numeric widening step.
func TestIfNullDropsLowCardinalityWithAColumnFallback(t *testing.T) {
	schema := ifNullLowCardinalityTestSchema(t)
	for _, testCase := range []struct {
		expression string
		want       string
	}{
		{"ifNull(length(lc), u64)", "UInt64"},
		{"ifNull(nullIf(length(lc), -(4000000000)), i128)", "Int128"},
		{"ifNull(lc, s)", "String"},
		{"ifNull(lc, 'x')", "LowCardinality(String)"},
		{"ifNull(lcn, 'x')", "LowCardinality(String)"},
	} {
		inferred, err := inferTestExprType(t, schema, testCase.expression)
		if err != nil {
			t.Errorf("%s: want %s, got refusal %v", testCase.expression, testCase.want, err)
		} else if inferred != testCase.want {
			t.Errorf("%s: want %s, got %s", testCase.expression, testCase.want, inferred)
		}
	}
}
