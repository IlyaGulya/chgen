//go:build fuzzoracle

package engine

import "testing"

func TestAggregateGeoAliasesAgainstClickHouse(t *testing.T) {
	schema, err := schemaFromDDLErr(t, oracleSchemaDDL)
	if err != nil {
		t.Fatal(err)
	}
	oracle := execWitnessFixture(t)
	cases := map[string]string{
		"any(api_point)":                      "Tuple(Float64, Float64)",
		"anyLast(api_ring)":                   "Array(Tuple(Float64, Float64))",
		"min(api_polygon)":                    "Array(Array(Tuple(Float64, Float64)))",
		"max(api_multi_polygon)":              "Array(Array(Array(Tuple(Float64, Float64))))",
		"argMin(api_point, i32)":              "Tuple(Float64, Float64)",
		"argMax(api_ring, i32)":               "Array(Tuple(Float64, Float64))",
		"argMinIf(api_polygon, i32, b)":       "Array(Array(Tuple(Float64, Float64)))",
		"argMaxIf(api_multi_polygon, i32, b)": "Array(Array(Array(Tuple(Float64, Float64))))",
		"groupArray(api_polygon)":             "Array(Array(Array(Tuple(Float64, Float64))))",
		"groupUniqArray(api_multi_polygon)":   "Array(Array(Array(Array(Tuple(Float64, Float64)))))",
	}
	for expression, expected := range cases {
		t.Run(expression, func(t *testing.T) {
			analysis, analysisErr := oracle.exec("SELECT toTypeName(" + expression + ") FROM t")
			execution := oracle.typeNames([]string{expression})[0]
			inferred, chgenErr := chgenInferType(schema, expression)
			if analysisErr != nil || execution.err != "" || chgenErr != nil {
				t.Fatalf("Geo aggregate differs: analysis=%v execution=%s chgen=%v", analysisErr, execution.err, chgenErr)
			}
			if analysis != expected || execution.typeName != expected || inferred != expected {
				t.Fatalf("Geo aggregate types: analysis=%s execution=%s chgen=%s, want %s", analysis, execution.typeName, inferred, expected)
			}
		})
	}
}
