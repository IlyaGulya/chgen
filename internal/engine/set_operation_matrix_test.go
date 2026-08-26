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

var setOperationMatrixPath = moduleRootPath("testdata", "clickhouse-set-operation-matrix.json")

func TestSetOperationMatrixAgainstChgen(t *testing.T) {
	artifact := conformance.CurrentSetOperationArtifact(MeasuredCHVersion)
	if err := conformance.ValidateSetOperationArtifact(artifact); err != nil {
		t.Fatal(err)
	}
	for _, cell := range artifact.Cells {
		t.Run(strings.ReplaceAll(cell.ID, "/", "_"), func(t *testing.T) {
			results, err := inferQueryResults(conformance.SetOperationFixtureDDL, cell.Query)
			if cell.Chgen == "refuse" {
				if err == nil || !strings.Contains(err.Error(), cell.ChgenError) {
					t.Fatalf("chgen error = %v, want %q", err, cell.ChgenError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(results) != len(cell.Results) {
				t.Fatalf("results = %#v, want %#v", results, cell.Results)
			}
			for index, want := range cell.Results {
				if results[index].SQLName != want.Name || results[index].CHType.String() != want.Type {
					t.Errorf("result %d = %s %s, want %s %s", index, results[index].SQLName, results[index].CHType.String(), want.Name, want.Type)
				}
			}
		})
	}
}

func TestPinnedSetOperationMatrixIsCurrent(t *testing.T) {
	wantArtifact := conformance.CurrentSetOperationArtifact(MeasuredCHVersion)
	want, err := json.MarshalIndent(wantArtifact, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, '\n')
	path := filepath.Clean(setOperationMatrixPath)
	if os.Getenv("CHGEN_UPDATE_SET_OPERATION_MATRIX") == "1" {
		if err := os.WriteFile(path, want, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var gotArtifact conformance.SetOperationArtifact
	if err := json.Unmarshal(got, &gotArtifact); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotArtifact, wantArtifact) {
		t.Fatal("set-operation matrix artifact is stale; run with CHGEN_UPDATE_SET_OPERATION_MATRIX=1")
	}
}
