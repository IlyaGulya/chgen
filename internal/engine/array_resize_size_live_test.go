//go:build fuzzoracle

package engine

import "testing"

func TestArrayResizeSizeDomainAgainstClickHouse(t *testing.T) {
	schema, err := schemaFromDDLErr(t, oracleSchemaDDL)
	if err != nil {
		t.Fatal(err)
	}
	oracle := execWitnessFixture(t)
	for _, size := range []string{"i32", "f64", "dec", "u128", "api_b_float16"} {
		expression := "arrayResize(arr_i, " + size + ")"
		analysis, analysisErr := oracle.exec("SELECT toTypeName(" + expression + ") FROM t")
		execution := oracle.typeNames([]string{expression})[0]
		inferred, inferErr := chgenInferType(schema, expression)
		if analysisErr != nil || execution.err != "" || inferErr != nil || analysis != "Array(Int32)" || execution.typeName != analysis || inferred != analysis {
			t.Errorf("legal size %s: analysis=%s/%v execution=%s/%s inferred=%s/%v", size, analysis, analysisErr, execution.typeName, execution.err, inferred, inferErr)
		}
	}
	for _, size := range []string{"ni32", "nf64", "ndec", "e8", "e16", "atan(ndec)"} {
		expression := "arrayResize(arr_i, " + size + ")"
		_, analysisErr := oracle.exec("SELECT toTypeName(" + expression + ") FROM t")
		execution := oracle.typeNames([]string{expression})[0]
		_, inferErr := chgenInferType(schema, expression)
		if analysisErr == nil || execution.err == "" || inferErr == nil {
			t.Errorf("illegal size %s: analysis=%v execution=%s inferred=%v", size, analysisErr, execution.err, inferErr)
		}
	}
}
