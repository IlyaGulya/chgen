//go:build fuzzoracle

package engine

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/IlyaGulya/chgen/internal/conformance"
)

// TestNestedScopeMatrixAgainstClickHouse keeps analysis and execution as
// separate witnesses. The IN width cell requires the two lanes to disagree.
func TestNestedScopeMatrixAgainstClickHouse(t *testing.T) {
	const outerDDL = "CREATE TABLE ns_outer (id UInt64, value Int32, label String) ENGINE = Memory"
	const outerSeed = "INSERT INTO ns_outer VALUES (1, 10, 'outer'), (2, 11, 'outer-2')"
	oracle := execWitnessFixtureWithDDL(t, outerDDL, outerSeed)
	if _, err := oracle.exec("CREATE TABLE ns_inner (id UInt64, value Int32, label String) ENGINE = Memory"); err != nil {
		t.Fatal(err)
	}
	if _, err := oracle.exec("INSERT INTO ns_inner VALUES (1, 20, 'inner'), (3, 30, 'inner-3')"); err != nil {
		t.Fatal(err)
	}
	artifact := conformance.CurrentNestedScopeArtifact(MeasuredCHVersion)
	if err := conformance.ValidateNestedScopeArtifact(artifact); err != nil {
		t.Fatal(err)
	}
	for _, cell := range artifact.Cells {
		t.Run(strings.ReplaceAll(cell.ID, "/", "_"), func(t *testing.T) {
			analysis, analysisErr := nestedScopeServerResults(oracle, cell.Query)
			if cell.Analysis == "refuse" {
				if analysisErr == nil {
					t.Fatalf("analysis accepted as %#v", analysis)
				}
				if !nestedScopeErrorHasCode(analysisErr, cell.AnalysisCode) {
					t.Fatalf("analysis error = %v, want code %d", analysisErr, cell.AnalysisCode)
				}
			} else {
				if analysisErr != nil {
					t.Fatal(analysisErr)
				}
				result, found := nestedScopeServerResult(analysis, cell.ResultName)
				if !found {
					t.Fatalf("analysis result name %q is missing from %#v", cell.ResultName, analysis)
				}
				gotType, err := conformance.ParseType(result.Type)
				if err != nil {
					t.Fatal(err)
				}
				wantType, err := conformance.ParseType(cell.AnalysisType)
				if err != nil {
					t.Fatal(err)
				}
				if !gotType.Equal(wantType) {
					t.Fatalf("analysis type = %s, want %s", result.Type, cell.AnalysisType)
				}
			}

			_, executionErr := oracle.exec(cell.Query + " FORMAT Null")
			if cell.Execution == "accept" && executionErr != nil {
				t.Fatalf("execution error = %v", executionErr)
			}
			if cell.Execution == "refuse" {
				if executionErr == nil {
					t.Fatal("execution accepted")
				}
				if !nestedScopeErrorHasCode(executionErr, cell.ExecutionCode) {
					t.Fatalf("execution error = %v, want code %d", executionErr, cell.ExecutionCode)
				}
			}
		})
	}
}

type nestedScopeDescribeResult struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

func nestedScopeServerResults(oracle *chOracle, query string) ([]nestedScopeDescribeResult, error) {
	raw, err := oracle.exec("DESCRIBE TABLE (" + query + ") FORMAT JSONEachRow")
	if err != nil {
		return nil, err
	}
	var results []nestedScopeDescribeResult
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		var result nestedScopeDescribeResult
		if err := json.Unmarshal([]byte(line), &result); err != nil {
			return nil, fmt.Errorf("decode nested scope analysis: %w", err)
		}
		results = append(results, result)
	}
	return results, nil
}

func nestedScopeServerResult(results []nestedScopeDescribeResult, name string) (nestedScopeDescribeResult, bool) {
	for _, result := range results {
		if result.Name == name {
			return result, true
		}
	}
	return nestedScopeDescribeResult{}, false
}

func nestedScopeErrorHasCode(err error, code int) bool {
	return err != nil && strings.Contains(err.Error(), fmt.Sprintf("Code: %d", code))
}
