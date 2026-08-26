package engine

import (
	"strings"
	"testing"
)

func TestWindowResultsRemoveGeoAliases(t *testing.T) {
	schema := schemaFromDDL(t, `CREATE TABLE probe
(
    i32 Int32,
    p Point,
    r Ring,
    poly Polygon,
    mp MultiPolygon
) ENGINE = Memory`)
	cases := map[string]string{
		"first_value(p) OVER (ORDER BY i32)":      "Tuple(Float64, Float64)",
		"last_value(r) OVER (ORDER BY i32)":       "Array(Tuple(Float64, Float64))",
		"lagInFrame(poly, 1) OVER (ORDER BY i32)": "Array(Array(Tuple(Float64, Float64)))",
		"leadInFrame(mp, 1) OVER (ORDER BY i32)":  "Array(Array(Array(Tuple(Float64, Float64))))",
		"any(p) OVER (ORDER BY i32)":              "Tuple(Float64, Float64)",
		"groupArray(r) OVER (ORDER BY i32)":       "Array(Array(Tuple(Float64, Float64)))",
		"first_value(p)":                          "Tuple(Float64, Float64)",
		"last_value(r)":                           "Array(Tuple(Float64, Float64))",
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

func TestDirectGeoResultsStayRefused(t *testing.T) {
	schema := schemaFromDDL(t, "CREATE TABLE geo_values (p Point) ENGINE = Memory")
	_, err := parseQueriesWithSchema(t, "-- name: ReadGeo :one\nSELECT p AS value FROM geo_values;", schema)
	if err == nil {
		t.Fatal("direct Geo result generated a Go type")
	}
	if !strings.Contains(err.Error(), "removes the Geo alias") {
		t.Fatalf("direct Geo result: %v", err)
	}
}
