//go:build fuzzoracle

package engine

import "testing"

func TestWindowGeoAliasesAgainstClickHouse(t *testing.T) {
	schema, err := schemaFromDDLErr(t, oracleSchemaDDL)
	if err != nil {
		t.Fatal(err)
	}
	oracle := execWitnessFixture(t)
	cases := map[string]string{
		"first_value(api_point) OVER (ORDER BY i32)":                        "Tuple(Float64, Float64)",
		"last_value(api_ring) OVER (ORDER BY i32)":                          "Array(Tuple(Float64, Float64))",
		"first_value(api_point)":                                            "Tuple(Float64, Float64)",
		"last_value(api_ring)":                                              "Array(Tuple(Float64, Float64))",
		"lagInFrame(api_polygon, 1) OVER (ORDER BY i32)":                    "Array(Array(Tuple(Float64, Float64)))",
		"leadInFrame(api_multi_polygon, 1) OVER (ORDER BY i32)":             "Array(Array(Array(Tuple(Float64, Float64))))",
		"any(api_point) OVER (ORDER BY i32)":                                "Tuple(Float64, Float64)",
		"groupArray(api_ring) OVER (ORDER BY i32 ROWS UNBOUNDED PRECEDING)": "Array(Array(Tuple(Float64, Float64)))",
	}
	for expression, expected := range cases {
		t.Run(expression, func(t *testing.T) {
			analysis, analysisErr := oracle.exec("SELECT toTypeName(" + expression + ") FROM t")
			execution := oracle.typeNames([]string{expression})[0]
			inferred, chgenErr := chgenInferType(schema, expression)
			if analysisErr != nil || execution.err != "" || chgenErr != nil {
				t.Fatalf("Geo window differs: analysis=%v execution=%s chgen=%v", analysisErr, execution.err, chgenErr)
			}
			if analysis != expected || execution.typeName != expected || inferred != expected {
				t.Fatalf("Geo window types: analysis=%s execution=%s chgen=%s, want %s", analysis, execution.typeName, inferred, expected)
			}
		})
	}
}

func TestDirectGeoAliasRefusalAgainstClickHouse(t *testing.T) {
	schema, err := schemaFromDDLErr(t, oracleSchemaDDL)
	if err != nil {
		t.Fatal(err)
	}
	oracle := execWitnessFixture(t)
	for _, expression := range []string{"api_point"} {
		t.Run(expression, func(t *testing.T) {
			if _, err := oracle.exec("SELECT toTypeName(" + expression + ") FROM t"); err != nil {
				t.Fatalf("analysis refused the server control: %v", err)
			}
			if execution := oracle.typeNames([]string{expression})[0]; execution.err != "" {
				t.Fatalf("execution refused the server control: %s", execution.err)
			}
			inferred, err := chgenInferType(schema, expression)
			if err != nil {
				t.Fatalf("inference refused before the Go boundary: %v", err)
			}
			if inferred != "Point" {
				t.Fatalf("direct Geo result inferred %s, want Point before the Go boundary", inferred)
			}
			if _, err := goType(CHType{Name: inferred}); err == nil {
				t.Fatal("direct Geo result got a Go type")
			}
		})
	}
}
