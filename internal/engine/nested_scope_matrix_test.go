package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/IlyaGulya/chgen/internal/conformance"
)

var nestedScopeMatrixPath = moduleRootPath("testdata", "clickhouse-nested-scope-matrix.json")

func TestNestedScopeMatrixAgainstChgen(t *testing.T) {
	artifact := conformance.CurrentNestedScopeArtifact(MeasuredCHVersion)
	if err := conformance.ValidateNestedScopeArtifact(artifact); err != nil {
		t.Fatal(err)
	}
	for _, cell := range artifact.Cells {
		t.Run(strings.ReplaceAll(cell.ID, "/", "_"), func(t *testing.T) {
			results, err := inferQueryResults(conformance.NestedScopeFixtureDDL, cell.Query)
			if cell.Chgen == "refuse" {
				if err == nil {
					t.Fatalf("chgen accepted cell as %#v", results)
				}
				if !strings.Contains(err.Error(), cell.ChgenError) {
					t.Fatalf("chgen error = %v, want boundary %q", err, cell.ChgenError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			result, found := nestedScopeResult(results, cell.ResultName)
			if !found {
				t.Fatalf("result name %q is missing from %#v", cell.ResultName, results)
			}
			if result.CHType.String() != cell.ChgenType {
				t.Fatalf("chgen type = %s, want %s", result.CHType.String(), cell.ChgenType)
			}
		})
	}
}

func TestPinnedNestedScopeMatrixIsCurrent(t *testing.T) {
	wantArtifact := conformance.CurrentNestedScopeArtifact(MeasuredCHVersion)
	want, err := json.MarshalIndent(wantArtifact, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, '\n')
	path := filepath.Clean(nestedScopeMatrixPath)
	if os.Getenv("CHGEN_UPDATE_NESTED_SCOPE_MATRIX") == "1" {
		if err := os.WriteFile(path, want, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var gotArtifact conformance.NestedScopeArtifact
	if err := json.Unmarshal(got, &gotArtifact); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotArtifact, wantArtifact) {
		t.Fatal("nested scope matrix artifact is stale; run with CHGEN_UPDATE_NESTED_SCOPE_MATRIX=1")
	}
}

func TestNestedScopeResultNameMutationFails(t *testing.T) {
	artifact := conformance.CurrentNestedScopeArtifact(MeasuredCHVersion)
	cell := artifact.Cells[0]
	results, err := inferQueryResults(conformance.NestedScopeFixtureDDL, cell.Query)
	if err != nil {
		t.Fatal(err)
	}
	if _, found := nestedScopeResult(results, "mutated_result"); found {
		t.Fatal("changed result name passed")
	}
}

func nestedScopeResult(results []Result, name string) (Result, bool) {
	for _, result := range results {
		if result.SQLName == name {
			return result, true
		}
	}
	return Result{}, false
}
