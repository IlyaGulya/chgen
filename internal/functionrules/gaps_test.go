package functionrules_test

import (
	"encoding/json"
	"os/exec"
	"testing"

	"github.com/IlyaGulya/chgen/internal/supportmanifest"
)

func TestFunctionGapCommandListsUnmeasuredNames(t *testing.T) {
	command := exec.CommandContext(t.Context(), "go", "run", "../tooling/cmd/functionrules", "-gaps", "-inventory", "../../testdata/clickhouse-api-inventory.json", "-manifest", "../../testdata/clickhouse-support-manifest.json")
	data, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("list function gaps: %v\n%s", err, data)
	}
	var report struct {
		Source struct {
			Version string `json:"version"`
		} `json:"source"`
		Gaps []struct {
			Name   string                 `json:"name"`
			Status supportmanifest.Status `json:"status"`
		} `json:"gaps"`
	}
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatal(err)
	}
	if report.Source.Version != "25.8.29.51" {
		t.Fatalf("wrong source: %s", report.Source.Version)
	}
	found := false
	for _, gap := range report.Gaps {
		if gap.Name == "acos" {
			t.Fatal("measured acos was listed as a gap")
		}
		if gap.Name == "sleep" {
			found = gap.Status == supportmanifest.NotMeasured
		}
	}
	if !found {
		t.Fatal("unmeasured sleep missing from gap report")
	}
}
