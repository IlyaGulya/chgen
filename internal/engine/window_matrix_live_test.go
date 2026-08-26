//go:build fuzzoracle

package engine

import (
	"strings"
	"testing"
)

func TestWindowMatrixAgainstClickHouse(t *testing.T) {
	schema, err := schemaFromDDLErr(t, oracleSchemaDDL)
	if err != nil {
		t.Fatal(err)
	}
	oracle := execWitnessFixture(t)
	for _, cell := range currentWindowMatrix().Cells {
		t.Run(cell.ID, func(t *testing.T) {
			expression, clause := cell.SQL, ""
			if index := strings.Index(expression, " WINDOW "); index >= 0 {
				clause, expression = expression[index:], expression[:index]
			}
			analysis, analysisErr := oracle.exec("SELECT toTypeName(" + expression + ") FROM t" + clause)
			_, executionErr := oracle.exec("SELECT " + expression + " FROM t" + clause + " FORMAT Null")
			query := "SELECT " + expression + " AS x FROM t" + clause
			inferred, chgenErr := inferWindowQueryType(t, schema, query)
			if cell.Execution == "accept" && executionErr != nil || cell.Execution == "refuse" && executionErr == nil {
				t.Fatalf("execution=%v, want %s", executionErr, cell.Execution)
			}
			if cell.Analysis == "accept" && (analysisErr != nil || analysis != cell.Type) || cell.Analysis == "refuse" && analysisErr == nil {
				t.Fatalf("analysis=%s/%v, want %s %s", analysis, analysisErr, cell.Analysis, cell.Type)
			}
			if cell.Chgen == "accept" && (chgenErr != nil || inferred.String() != cell.Type) || cell.Chgen == "refuse" && chgenErr == nil {
				t.Fatalf("chgen=%s/%v, want %s %s", inferred.String(), chgenErr, cell.Chgen, cell.Type)
			}
		})
	}
}
