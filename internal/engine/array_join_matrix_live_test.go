//go:build fuzzoracle

package engine

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/IlyaGulya/chgen/internal/conformance"
)

// TestArrayJoinMatrixAgainstClickHouse keeps analysis and execution as
// separate witnesses for every ARRAY JOIN boundary.
func TestArrayJoinMatrixAgainstClickHouse(t *testing.T) {
	ddl := setOperationFixtureParts(conformance.ArrayJoinFixtureDDL)
	seed := setOperationFixtureParts(conformance.ArrayJoinFixtureSeed)
	if len(ddl) == 0 || len(seed) == 0 {
		t.Fatal("ARRAY JOIN fixture is empty")
	}
	oracle := execWitnessFixtureWithDDL(t, ddl[0], seed[0])
	for _, statement := range append(ddl[1:], seed[1:]...) {
		if _, err := oracle.exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	artifact := conformance.CurrentArrayJoinArtifact(MeasuredCHVersion)
	if err := conformance.ValidateArrayJoinArtifact(artifact); err != nil {
		t.Fatal(err)
	}
	for _, cell := range artifact.Cells {
		t.Run(strings.ReplaceAll(cell.ID, "/", "_"), func(t *testing.T) {
			analysis, analysisErr := arrayJoinServerResults(oracle, cell.Query)
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

type arrayJoinDescribeResult struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

func arrayJoinServerResults(oracle *chOracle, query string) ([]arrayJoinDescribeResult, error) {
	raw, err := oracle.exec("DESCRIBE TABLE (" + query + ") FORMAT JSONEachRow")
	if err != nil {
		return nil, err
	}
	var results []arrayJoinDescribeResult
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		var result arrayJoinDescribeResult
		if err := json.Unmarshal([]byte(line), &result); err != nil {
			return nil, fmt.Errorf("decode ARRAY JOIN analysis: %w", err)
		}
		results = append(results, result)
	}
	return results, nil
}
