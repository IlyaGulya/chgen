package engine

import (
	"strings"
	"testing"
)

func TestWideIntegerGoTypeMeasuredBoundary(t *testing.T) {
	t.Parallel()

	refused := []string{
		"Int128",
		"UInt128",
		"Int256",
		"UInt256",
		"Nullable(Int128)",
		"Array(Int128)",
		"Array(Nullable(UInt256))",
		"Array(Array(Int256))",
		"LowCardinality(Int128)",
		"SimpleAggregateFunction(sum, Int128)",
		"Map(String, Int128)",
		"Map(String, Array(UInt256))",
		"Map(Int128, String)",
	}
	for _, clickHouseType := range refused {
		columnType, err := parseCHTypeName(clickHouseType)
		if err != nil {
			t.Fatalf("parse %s: %v", clickHouseType, err)
		}
		_, err = goType(columnType)
		if err == nil || (!strings.Contains(err.Error(), "measured refusal") && !strings.Contains(err.Error(), "panic")) {
			t.Errorf("goType(%s) error = %v, want measured wide integer refusal", clickHouseType, err)
		}
	}
}

func TestComplexTypeFamiliesHaveExactRefusals(t *testing.T) {
	t.Parallel()

	types := []string{
		"Tuple(a Int32, b String)",
		"Nested(a Int32, b String)",
		"Variant(Int32, String)",
		"Dynamic",
		"JSON",
		"Point",
		"Ring",
		"LineString",
		"Polygon",
		"MultiLineString",
		"MultiPolygon",
	}
	for _, clickHouseType := range types {
		columnType, err := parseCHTypeName(clickHouseType)
		if err != nil {
			t.Fatalf("parse %s: %v", clickHouseType, err)
		}
		_, err = goType(columnType)
		if err == nil || !strings.Contains(err.Error(), "measured refusal") {
			t.Errorf("goType(%s) error = %v, want exact measured refusal", clickHouseType, err)
		}
	}
}

func TestGeneratedCodeRefusesWideIntegers(t *testing.T) {
	schema, err := schemaFromDDLErr(t, `CREATE TABLE wide_values
(
    scalar Int128,
    optional Nullable(UInt256),
    values Array(Int256)
) ENGINE = Memory;`)
	if err != nil {
		t.Fatalf("parse schema: %v", err)
	}
	_, err = parseQueriesWithSchema(t, `-- name: ReadWideValues :many
SELECT scalar, optional, values FROM wide_values;`, schema)
	if err == nil || !strings.Contains(err.Error(), "measured refusal") {
		t.Fatalf("parse queries error = %v, want wide integer refusal", err)
	}
}

func TestGeoExpressionCannotGenerateAnOrbResult(t *testing.T) {
	schema, err := schemaFromDDLErr(t, `CREATE TABLE geo_values
(
    p Point
) ENGINE = Memory;`)
	if err != nil {
		t.Fatalf("parse schema: %v", err)
	}
	_, err = parseQueriesWithSchema(t, `-- name: ReadAnyPoint :one
SELECT any(p) AS value FROM geo_values;`, schema)
	if err == nil {
		t.Fatal("Geo expression generated a Go result")
	}
	if !strings.Contains(err.Error(), "Tuple(Float64, Float64)") || !strings.Contains(err.Error(), "no Go mapping") {
		t.Fatalf("Geo expression error = %v, want the Tuple refusal", err)
	}
}
