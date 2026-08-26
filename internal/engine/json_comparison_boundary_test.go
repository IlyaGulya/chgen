package engine

import (
	"strings"
	"testing"
)

const jsonComparisonBoundarySchema = `
CREATE TABLE probe (
    i32 Int32,
    i64 Int64,
    dec Decimal(18, 4),
    s String,
    ns Nullable(String),
    d Date,
    uid UUID,
    arr Array(Int32),
    tup Tuple(Int32, String),
    m Map(String, Int64),
    variant Variant(Int32, String),
    dyn Dynamic,
    js JSON,
    js_peer JSON
) ENGINE = MergeTree ORDER BY tuple()
`

func jsonComparisonTestSchema(t *testing.T) *Schema {
	t.Helper()
	schema, err := schemaFromDDLErr(t, jsonComparisonBoundarySchema)
	if err != nil {
		t.Fatalf("parse the JSON comparison schema: %v", err)
	}
	return schema
}

// TestJSONComparisonRefusesEveryDifferentColumnType checks the boundary
// measured on ClickHouse 25.8.29.51 over all real current fixture columns. JSON
// compares with JSON and refuses every different column type. The String,
// Nullable(String) and Dynamic cells are important because toTypeName gives
// them UInt8 or Nullable(UInt8), but the value witness refuses them.
func TestJSONComparisonRefusesEveryDifferentColumnType(t *testing.T) {
	schema := jsonComparisonTestSchema(t)
	for _, expression := range []string{
		"js = i32",
		"notEquals(js, dec)",
		"less(js, s)",
		"lessOrEquals(js, ns)",
		"greater(js, d)",
		"greaterOrEquals(js, uid)",
		"equals(js, arr)",
		"notEquals(js, tup)",
		"less(js, m)",
		"greater(js, variant)",
		"equals(js, dyn)",
		"last_value(notEquals(js, dec)) OVER (ORDER BY i64)",
	} {
		if inferred, err := inferTestExprType(t, schema, expression); err == nil {
			t.Errorf("%s: want a refusal, got %s", expression, inferred)
		} else if !strings.Contains(err.Error(), "cannot compare") {
			t.Errorf("%s: want a comparison refusal, got %v", expression, err)
		}
	}
}

// TestJSONComparisonKeepsExactAndConstantNeighbors checks the accepted side
// of the measured boundary. Two JSON columns compare in every comparison
// form. A constant JSON text also compares because ClickHouse converts the
// constant before it runs the comparison.
func TestJSONComparisonKeepsExactAndConstantNeighbors(t *testing.T) {
	schema := jsonComparisonTestSchema(t)
	for _, expression := range []string{
		"js = js_peer",
		"notEquals(js, js_peer)",
		"less(js, js_peer)",
		"lessOrEquals(js, js_peer)",
		"greater(js, js_peer)",
		"greaterOrEquals(js, js_peer)",
		`equals(js, '{"a":1}')`,
	} {
		inferred, err := inferTestExprType(t, schema, expression)
		if err != nil {
			t.Errorf("%s: want UInt8, got refusal %v", expression, err)
		} else if inferred != "UInt8" {
			t.Errorf("%s: want UInt8, got %s", expression, inferred)
		}
	}
}
