package engine

import (
	"strings"
	"testing"
)

func TestAggregateResultsRemoveGeoAliases(t *testing.T) {
	schema := schemaFromDDL(t, `CREATE TABLE probe
(
    i32 Int32,
    b Bool,
    p Point,
    r Ring,
    poly Polygon,
    mp MultiPolygon
) ENGINE = Memory`)
	cases := map[string]string{
		"any(p)":                 "Tuple(Float64, Float64)",
		"anyLast(r)":             "Array(Tuple(Float64, Float64))",
		"min(poly)":              "Array(Array(Tuple(Float64, Float64)))",
		"max(mp)":                "Array(Array(Array(Tuple(Float64, Float64))))",
		"argMin(p, i32)":         "Tuple(Float64, Float64)",
		"argMax(r, i32)":         "Array(Tuple(Float64, Float64))",
		"argMinIf(poly, i32, b)": "Array(Array(Tuple(Float64, Float64)))",
		"argMaxIf(mp, i32, b)":   "Array(Array(Array(Tuple(Float64, Float64))))",
		"groupArray(poly)":       "Array(Array(Array(Tuple(Float64, Float64))))",
		"groupUniqArray(mp)":     "Array(Array(Array(Array(Tuple(Float64, Float64)))))",
	}
	for expression, expected := range cases {
		actual, err := inferTestExprType(t, schema, expression)
		if err != nil {
			t.Errorf("infer %s: %v", expression, err)
			continue
		}
		if actual != expected {
			t.Errorf("infer %s = %s, want %s", expression, actual, expected)
		}
	}
}

func TestAggregateGeoResultStillHasNoGoMapping(t *testing.T) {
	schema := schemaFromDDL(t, "CREATE TABLE geo_values (p Point) ENGINE = Memory")
	_, err := parseQueriesWithSchema(t, "-- name: ReadGeo :one\nSELECT any(p) AS value FROM geo_values;", schema)
	if err == nil {
		t.Fatal("aggregate Geo result generated a Go type")
	}
	if !strings.Contains(err.Error(), "Tuple") || !strings.Contains(err.Error(), "no Go mapping") {
		t.Fatalf("aggregate Geo result: %v", err)
	}
}
