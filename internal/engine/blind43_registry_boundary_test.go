package engine

import (
	"strings"
	"testing"
)

const blind43BoundarySchema = `
CREATE TABLE probe (
    i32 Int32,
    i64 Int64,
    u8 UInt8,
    b Bool,
    s String,
    d Date,
    nd Nullable(Date),
    ni32 Nullable(Int32),
    dtz64 DateTime64(6, 'UTC'),
    variant Variant(Int32, String),
    variant_peer Variant(Int32, String),
    variant_other Variant(Int32, UInt64),
    variant_array Array(Variant(Int32, String)),
    variant_array_peer Array(Variant(Int32, String)),
    variant_array_other Array(Variant(Int32, UInt64)),
    dyn Dynamic,
    js JSON
) ENGINE = MergeTree ORDER BY tuple()
`

func blind43BoundaryTestSchema(t *testing.T) *Schema {
	t.Helper()
	schema, err := schemaFromDDLErr(t, blind43BoundarySchema)
	if err != nil {
		t.Fatalf("parse the registry boundary schema: %v", err)
	}
	return schema
}

// TestVariantComparisonBoundary checks the exact Variant boundary that
// ClickHouse 25.8.29.51 gives for real columns. A Variant compares only
// with the same Variant type. A constant String is a legal neighbor because
// ClickHouse folds it into a held Variant branch before the comparison.
func TestVariantComparisonBoundary(t *testing.T) {
	schema := blind43BoundaryTestSchema(t)
	for _, expression := range []string{
		"greater(addQuarters(dtz64, 3), variant)",
		"equals(variant, i32)",
		"variant = s",
		"variant = variant_other",
		"variant_array = variant",
		"variant_array = variant_array_other",
		"last_value(equals(variant, i32)) OVER (ORDER BY i64)",
	} {
		if inferred, err := inferTestExprType(t, schema, expression); err == nil {
			t.Errorf("%s: want a refusal, got %s", expression, inferred)
		} else if !strings.Contains(err.Error(), "cannot compare") {
			t.Errorf("%s: want a comparison refusal, got %v", expression, err)
		}
	}

	for _, testCase := range []struct {
		expression string
		want       string
	}{
		{"variant = variant_peer", "UInt8"},
		{"equals(variant, variant)", "UInt8"},
		{"variant_array = variant_array_peer", "UInt8"},
		{"equals(variant, '3')", "UInt8"},
	} {
		inferred, err := inferTestExprType(t, schema, testCase.expression)
		if err != nil {
			t.Errorf("%s: want %s, got refusal %v", testCase.expression, testCase.want, err)
		} else if inferred != testCase.want {
			t.Errorf("%s: want %s, got %s", testCase.expression, testCase.want, inferred)
		}
	}
}

// TestDynamicOrderingAggregateBoundary checks that a Dynamic value cannot
// be an ordering key. The same measured domain serves argMax, argMin, max,
// min, and the If forms. A Dynamic result value is legal when its key is a
// stable scalar type.
func TestDynamicOrderingAggregateBoundary(t *testing.T) {
	schema := blind43BoundaryTestSchema(t)
	for _, expression := range []string{
		"argMax(i32, dyn)",
		"argMin(i32, dyn)",
		"argMaxIf(d, dyn, b)",
		"argMinIf(d, dyn, b)",
		"max(dyn)",
		"min(dyn)",
		"argMax(i32, variant)",
		"argMinIf(d, variant, b)",
		"max(variant)",
		"min(variant)",
	} {
		if inferred, err := inferTestExprType(t, schema, expression); err == nil {
			t.Errorf("%s: want a refusal, got %s", expression, inferred)
		}
	}

	for _, testCase := range []struct {
		expression string
		want       string
	}{
		{"argMax(dyn, i32)", "Dynamic"},
		{"argMin(dyn, i64)", "Dynamic"},
		{"argMax(i32, i64)", "Int32"},
		{"argMinIf(d, ni32, b)", "Nullable(Date)"},
	} {
		inferred, err := inferTestExprType(t, schema, testCase.expression)
		if err != nil {
			t.Errorf("%s: want %s, got refusal %v", testCase.expression, testCase.want, err)
		} else if inferred != testCase.want {
			t.Errorf("%s: want %s, got %s", testCase.expression, testCase.want, inferred)
		}
	}
}

// TestJSONConversionBoundary checks the numeric and temporal conversion
// families. The analysis witness gives the fixed target type for these
// calls, but the execution witness refuses the real JSON column with Code
// 43. Each changed family has a legal real-column control.
func TestJSONConversionBoundary(t *testing.T) {
	schema := blind43BoundaryTestSchema(t)
	for _, expression := range []string{
		"toUInt8(js)", "toUInt16(js)", "toUInt32(js)", "toUInt64(js)",
		"toInt8(js)", "toInt16(js)", "toInt32(js)", "toInt64(js)",
		"toFloat32(js)", "toFloat64(js)",
		"toDate(js)", "toDate32(js)", "toDateTime(js)",
		"toDateTime64(js, 3)",
		"toUInt64(variant)", "toFloat32(variant)",
		"toDate(variant)", "toDate32(variant)", "toDateTime(variant)",
		"toDateTime64(variant, 3)",
	} {
		if inferred, err := inferTestExprType(t, schema, expression); err == nil {
			t.Errorf("%s: want a refusal, got %s", expression, inferred)
		}
	}

	for _, testCase := range []struct {
		expression string
		want       string
	}{
		{"toUInt64(i32)", "UInt64"},
		{"toInt64(s)", "Int64"},
		{"toFloat64(i64)", "Float64"},
		{"toDate(i32)", "Date"},
		{"toDate32(i32)", "Date32"},
		{"toDateTime(i32)", "DateTime"},
		{"toDateTime64(i32, 3)", "DateTime64(3)"},
		{"toInt64(dyn)", "Int64"},
		{"toDateTime(dyn)", "DateTime"},
	} {
		inferred, err := inferTestExprType(t, schema, testCase.expression)
		if err != nil {
			t.Errorf("%s: want %s, got refusal %v", testCase.expression, testCase.want, err)
		} else if inferred != testCase.want {
			t.Errorf("%s: want %s, got %s", testCase.expression, testCase.want, inferred)
		}
	}
}

// TestDynamicAggregateResultCannotBecomeNullable checks the invalid wrapper
// boundary. A Nullable key still makes an ordinary result Nullable, but a
// Dynamic result stays bare because Nullable(Dynamic) is not a ClickHouse
// type.
func TestDynamicAggregateResultCannotBecomeNullable(t *testing.T) {
	schema := blind43BoundaryTestSchema(t)
	for _, testCase := range []struct {
		expression string
		want       string
	}{
		{"argMaxIf(dyn, addMonths(nd, 2), u8 = 5)", "Dynamic"},
		{"argMinIf(dyn, ni32, b)", "Dynamic"},
		{"argMaxIf(d, addMonths(nd, 2), u8 = 5)", "Nullable(Date)"},
		{"argMinIf(i32, ni32, b)", "Nullable(Int32)"},
	} {
		inferred, err := inferTestExprType(t, schema, testCase.expression)
		if err != nil {
			t.Errorf("%s: want %s, got refusal %v", testCase.expression, testCase.want, err)
		} else if inferred != testCase.want {
			t.Errorf("%s: want %s, got %s", testCase.expression, testCase.want, inferred)
		}
	}
}
