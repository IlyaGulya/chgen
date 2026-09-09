package engine

import (
	"fmt"
	"strings"
	"testing"
)

const nullableShiftDDL = `CREATE TABLE t (
    d Date, d32 Date32, dt DateTime('UTC'), dt64 DateTime64(6, 'UTC'),
    n Nullable(Int32)
) ENGINE = Memory`

func TestTemporalShiftNullableCount(t *testing.T) {
	schema := schemaFromDDL(t, strings.Replace(nullableShiftDDL, "TABLE t", "TABLE probe", 1))
	for function, subDay := range temporalShiftFunctions {
		for _, column := range []struct{ name, result string }{
			{"d", "Date"}, {"d32", "Date32"},
			{"dt", "DateTime('UTC')"}, {"dt64", "DateTime64(6, 'UTC')"},
		} {
			want := column.result
			if subDay && column.name == "d" {
				want = "DateTime"
			}
			if subDay && column.name == "d32" {
				want = "DateTime64(3)"
			}
			expression := fmt.Sprintf("%s(%s, n)", functionRegistry[function].gen.spelling, column.name)
			got, err := inferTestExprType(t, schema, expression)
			if err != nil || got != "Nullable("+want+")" {
				t.Errorf("%s: %s, %v; want Nullable(%s)", expression, got, err, want)
			}
		}
	}
}

var nonNullableGeometryTypes = []string{"Point", "Ring", "LineString", "Polygon", "MultiLineString", "MultiPolygon"}

func TestNullIfRefusesGeometryResult(t *testing.T) {
	for _, name := range nonNullableGeometryTypes {
		schema := schemaFromDDL(t, "CREATE TABLE probe (g "+name+") ENGINE = Memory")
		_, err := inferTestExprType(t, schema, "nullIf(g, g)")
		if err == nil || !strings.Contains(err.Error(), "inside Nullable") {
			t.Errorf("nullIf(%s, %s): %v", name, name, err)
		}
	}
}
