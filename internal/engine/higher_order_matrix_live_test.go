//go:build fuzzoracle

package engine

import (
	"strings"
	"testing"
)

// TestHigherOrderMatrixAgainstClickHouse executes both server witnesses for
// every pinned matrix cell. Analysis and execution can disagree for unequal
// arrays, thus neither witness stands in for the other.
func TestHigherOrderMatrixAgainstClickHouse(t *testing.T) {
	oracle := execWitnessFixtureWithDDL(t, higherOrderLiveDDL, higherOrderLiveSeed)
	schema, err := schemaFromDDLErr(t, higherOrderLiveDDL)
	if err != nil {
		t.Fatal(err)
	}
	artifact := buildHigherOrderMatrix()
	if err := validateHigherOrderMatrix(artifact); err != nil {
		t.Fatal(err)
	}
	for _, cell := range artifact.Cells {
		analysis, analysisErr := oracle.exec("SELECT toTypeName(" + cell.SQL + ") FROM t")
		if cell.Analysis == "refuse" {
			if analysisErr == nil {
				t.Errorf("cell %s server analysis accepted as %s", cell.ID, strings.TrimSpace(analysis))
			} else if strings.HasSuffix(cell.ID, "/linked_arity") && cell.Scalar == "limit_before_arrays" &&
				!strings.Contains(analysisErr.Error(), "Code: 43") {
				t.Errorf("cell %s server analysis error = %v, want Code 43", cell.ID, analysisErr)
			}
		} else if analysisErr != nil {
			t.Errorf("cell %s server analysis error = %v", cell.ID, analysisErr)
		} else if got := strings.TrimSpace(analysis); got != cell.Type {
			t.Errorf("cell %s server type = %s, want %s", cell.ID, got, cell.Type)
		}

		_, executionErr := oracle.exec("SELECT ignore(" + cell.SQL + ") FROM t")
		if cell.Execution == "refuse" && executionErr == nil {
			t.Errorf("cell %s server execution accepted", cell.ID)
		} else if cell.Execution == "refuse" && strings.HasSuffix(cell.ID, "/linked_arity") &&
			cell.Scalar == "limit_before_arrays" && !strings.Contains(executionErr.Error(), "Code: 43") {
			t.Errorf("cell %s server execution error = %v, want Code 43", cell.ID, executionErr)
		}
		if cell.Execution == "accept" && executionErr != nil {
			t.Errorf("cell %s server execution error = %v", cell.ID, executionErr)
		}

		chgenType, chgenErr := chgenInferType(schema, cell.SQL)
		if cell.Analysis == "refuse" {
			if chgenErr == nil {
				t.Errorf("cell %s chgen accepted as %s", cell.ID, chgenType)
			}
		} else if chgenErr != nil {
			t.Errorf("cell %s chgen error = %v", cell.ID, chgenErr)
		} else if chgenType != cell.Type {
			t.Errorf("cell %s chgen type = %s, want %s", cell.ID, chgenType, cell.Type)
		}
	}
}
