package main_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/IlyaGulya/chgen/internal/argumentcoverage"
)

func TestArgumentCoverageCLIReportsBothProfilesAndWrapperGrid(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "functionrules")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, ".")
	if data, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, data)
	}
	command := exec.CommandContext(t.Context(), binary, "-argument-coverage")
	command.Dir = "../../../.."
	data, err := command.Output()
	if err != nil {
		t.Fatalf("coverage command: %v", err)
	}
	var report argumentcoverage.ArgumentReport
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Evidence) != 2 || report.WrapperGrid == nil || report.WrapperGrid.Total != 10629 || report.Counts["build_dependent_refusal"] != 21 {
		t.Fatalf("incomplete product report")
	}
	command = exec.CommandContext(t.Context(), binary, "-argument-coverage", "-wrapper-grid", "missing-grid")
	command.Dir = "../../../.."
	if err := command.Run(); err == nil {
		t.Fatal("missing evidence must fail, not omit coverage")
	}
	grid := filepath.Join(t.TempDir(), "grid.golden")
	// A separate fixture, never the tracked golden: an independently observed
	// mismatch must be visible in JSON and must still make the command fail.
	if err := os.WriteFile(grid, []byte("# clickhouse_version\t25.8.29.51\n# cells\t1\n# verdict\tMISMATCH\t1\nfn/reverse/saf/s\tMISMATCH\tSimpleAggregateFunction(anyLast, String)\t\tString\treverse(c_saf_s)\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	command = exec.CommandContext(t.Context(), binary, "-argument-coverage", "-wrapper-grid", grid)
	command.Dir = "../../../.."
	data, err = command.Output()
	if err == nil {
		t.Fatal("known mismatch was waived")
	}
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("failure must retain readable report: %v", err)
	}
	if report.WrapperGrid.Counts["MISMATCH"] != 1 {
		t.Fatal("mismatch was hidden")
	}
}
