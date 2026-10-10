package functionrules_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IlyaGulya/chgen/internal/functionrules"
)

func TestMeasurementCheckExplainsEveryChangedCell(t *testing.T) {
	data, err := os.ReadFile("../../testdata/clickhouse-function-rules.json")
	if err != nil {
		t.Fatal(err)
	}
	report, err := functionrules.Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	for i := range report.Functions {
		if report.Functions[i].Name == "exp" {
			for j := range report.Functions[i].Cells {
				cell := &report.Functions[i].Cells[j]
				if cell.Input == "Float32" {
					cell.Analysis, cell.Execution = "Float32", "Float32"
				}
			}
		}
	}
	report.Source.BuildID = "other-observed-build"
	directory := t.TempDir()
	actual := filepath.Join(directory, "actual.json")
	data, err = json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(actual, data, 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), "go", "run", "../tooling/cmd/functionrules", "-check", "-evidence", "../../testdata/clickhouse-function-rules.json", "-compare", actual)
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatal("changed measurement passed")
	}
	for _, want := range []string{"exp", "Float32", "expected analysis=Float64 execution=Float64", "actual analysis=Float32 execution=Float32", "other-observed-build", "semantic_digest", "plan_digest"} {
		if !strings.Contains(string(output), want) {
			t.Errorf("missing %q in diagnostic:\n%s", want, output)
		}
	}
}

func TestKnownBuildVariantsGenerateTheSameRestrictedRules(t *testing.T) {
	data, err := os.ReadFile("../../testdata/clickhouse-function-rules.json")
	if err != nil {
		t.Fatal(err)
	}
	expected, err := functionrules.Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := functionrules.Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	witnessData, err := os.ReadFile("evidence/build-variants-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var witness functionrules.Comparison
	if err := json.Unmarshal(witnessData, &witness); err != nil {
		t.Fatal(err)
	}
	actual.Source = witness.Actual.Source
	for i := range actual.Functions {
		for j := range actual.Functions[i].Cells {
			for _, difference := range witness.Cells {
				if actual.Functions[i].Name == difference.Function && actual.Functions[i].Cells[j].ID == difference.Actual.ID {
					actual.Functions[i].Cells[j] = difference.Actual
				}
			}
		}
	}
	comparison := functionrules.Compare(expected, actual)
	if !comparison.Matched() || len(comparison.Cells) != 21 || comparison.Policy == "" {
		t.Fatalf("known variants must stay visible and excluded: %+v", comparison)
	}
	if !functionrules.Compare(actual, expected).Matched() {
		t.Fatal("known build comparison depends on which witness was pinned")
	}
	source, err := os.ReadFile("../engine/registry.go")
	if err != nil {
		t.Fatal(err)
	}
	arm, _, err := functionrules.Generate(source, expected)
	if err != nil {
		t.Fatal(err)
	}
	amd, _, err := functionrules.Generate(source, actual)
	if err != nil {
		t.Fatal(err)
	}
	if string(arm) != string(amd) {
		t.Fatal("build identity or excluded cells changed portable generated rules")
	}
	directory := t.TempDir()
	actualPath := filepath.Join(directory, "actual.json")
	actualData, err := json.Marshal(actual)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(actualPath, actualData, 0o600); err != nil {
		t.Fatal(err)
	}
	reportPath := filepath.Join(directory, "comparison")
	command := exec.CommandContext(t.Context(), "go", "run", "../tooling/cmd/functionrules", "-check", "-evidence", "../../testdata/clickhouse-function-rules.json", "-compare", actualPath, "-report", reportPath)
	output, err := command.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "PORTABLE") || !strings.Contains(string(output), "KNOWN 21") {
		t.Fatalf("known differences must not be presented as identical measurements: %v\n%s", err, output)
	}
	diff, err := os.ReadFile(reportPath + ".diff.json")
	if err != nil {
		t.Fatal(err)
	}
	var diagnostic functionrules.Comparison
	if err := json.Unmarshal(diff, &diagnostic); err != nil {
		t.Fatal(err)
	}
	if len(diagnostic.Cells) != 21 || diagnostic.Policy == "" || diagnostic.Expected.Source.BuildID == diagnostic.Actual.Source.BuildID {
		t.Fatal("structured artifact lost differences or provenance")
	}
}

func TestBuildProvenanceDoesNotChangeSemanticIdentity(t *testing.T) {
	data, err := os.ReadFile("../../testdata/clickhouse-function-rules.json")
	if err != nil {
		t.Fatal(err)
	}
	expected, err := functionrules.Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := functionrules.Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	actual.Source.BuildID = "independent-build-with-identical-witnesses"
	comparison := functionrules.Compare(expected, actual)
	if !comparison.Matched() || comparison.Expected.SemanticDigest != comparison.Actual.SemanticDigest || comparison.Expected.Source.BuildID == comparison.Actual.Source.BuildID {
		t.Fatal("semantic equivalence erased provenance or required build identity")
	}
	actual.Functions[0].CaseInsensitive = !actual.Functions[0].CaseInsensitive
	if functionrules.Compare(expected, actual).Matched() {
		t.Fatal("case policy drift passed as a build-only difference")
	}
}
