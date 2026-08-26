//go:build fuzzoracle

package engine

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/IlyaGulya/chgen/internal/conformance"
)

// TestFinalMatrixAgainstClickHouse keeps analysis and execution as separate
// witnesses for every FINAL scope and engine boundary.
func TestFinalMatrixAgainstClickHouse(t *testing.T) {
	ddl := setOperationFixtureParts(conformance.FinalFixtureDDL)
	seed := setOperationFixtureParts(conformance.FinalFixtureSeed)
	if len(ddl) == 0 || len(seed) == 0 {
		t.Fatal("FINAL fixture is empty")
	}
	oracle := execWitnessFixtureWithDDL(t, ddl[0], seed[0])
	for _, statement := range append(ddl[1:], seed[1:]...) {
		if _, err := oracle.exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	artifact := conformance.CurrentFinalArtifact(MeasuredCHVersion)
	if err := conformance.ValidateFinalArtifact(artifact); err != nil {
		t.Fatal(err)
	}
	for _, cell := range artifact.Cells {
		t.Run(strings.ReplaceAll(cell.ID, "/", "_"), func(t *testing.T) {
			analysis, analysisErr := finalServerResults(oracle, cell.Query)
			if cell.Analysis == "refuse" {
				if analysisErr == nil || !setOperationErrorHasCode(analysisErr, cell.AnalysisCode) {
					t.Fatalf("analysis error = %v, want code %d", analysisErr, cell.AnalysisCode)
				}
			} else {
				if analysisErr != nil {
					t.Fatal(analysisErr)
				}
				if len(analysis) != len(cell.Results) {
					t.Fatalf("analysis results = %#v, want %#v", analysis, cell.Results)
				}
				for index, want := range cell.Results {
					gotType, err := conformance.ParseType(analysis[index].Type)
					if err != nil {
						t.Fatal(err)
					}
					wantType, err := conformance.ParseType(want.Type)
					if err != nil {
						t.Fatal(err)
					}
					if analysis[index].Name != want.Name || !gotType.Equal(wantType) {
						t.Errorf("analysis result %d = %s %s, want %s %s", index, analysis[index].Name, analysis[index].Type, want.Name, want.Type)
					}
				}
			}
			_, executionErr := oracle.exec(cell.Query + " FORMAT Null")
			if cell.Execution == "accept" && executionErr != nil {
				t.Fatalf("execution error = %v", executionErr)
			}
			if cell.Execution == "refuse" && (executionErr == nil || !setOperationErrorHasCode(executionErr, cell.ExecutionCode)) {
				t.Fatalf("execution error = %v, want code %d", executionErr, cell.ExecutionCode)
			}
		})
	}
}

type finalDescribeResult struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

func finalServerResults(oracle *chOracle, query string) ([]finalDescribeResult, error) {
	raw, err := oracle.exec("DESCRIBE TABLE (" + query + ") FORMAT JSONEachRow")
	if err != nil {
		return nil, err
	}
	var results []finalDescribeResult
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		var result finalDescribeResult
		if err := json.Unmarshal([]byte(line), &result); err != nil {
			return nil, fmt.Errorf("decode FINAL analysis: %w", err)
		}
		results = append(results, result)
	}
	return results, nil
}

func TestFinalInsertSelectAgainstClickHouse(t *testing.T) {
	ddl := setOperationFixtureParts(conformance.FinalFixtureDDL)
	seed := setOperationFixtureParts(conformance.FinalFixtureSeed)
	oracle := execWitnessFixtureWithDDL(t, ddl[0], seed[0])
	for _, statement := range append(ddl[1:], seed[1:]...) {
		if _, err := oracle.exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	source := "SELECT id, value FROM final_rows FINAL ORDER BY id"
	results, err := finalServerResults(oracle, source)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].Type != "UInt64" || results[1].Type != "Int32" {
		t.Fatalf("source results = %#v", results)
	}
	if _, err := oracle.exec("INSERT INTO final_sink (id, value) " + source); err != nil {
		t.Fatal(err)
	}
	raw, err := oracle.exec("SELECT id, value FROM final_sink ORDER BY id FORMAT TSV")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(raw) != "1\t20\n2\t30" {
		t.Fatalf("inserted values = %q", strings.TrimSpace(raw))
	}
}
