package functionrules_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IlyaGulya/chgen/internal/functionrules"
)

func TestMeasuredScalarSpellingAgainstClickHouse(t *testing.T) {
	endpoint := os.Getenv("CHGEN_ORACLE_URL")
	if endpoint == "" {
		t.Skip("CHGEN_ORACLE_URL is not set")
	}
	type call struct {
		name, argument  string
		caseInsensitive bool
	}
	var calls []call
	for _, name := range []string{"cbrt", "cosh", "erf", "erfc", "exp10", "exp2", "lgamma", "sinh", "tgamma"} {
		calls = append(calls, call{name: name, argument: "toInt32(1)"})
	}
	data, err := os.ReadFile("../../testdata/clickhouse-string-function-rules.json")
	if err != nil {
		t.Fatal(err)
	}
	report, err := functionrules.Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, function := range report.Functions {
		calls = append(calls, call{name: function.Name, argument: "'AbC'", caseInsensitive: function.CaseInsensitive})
	}
	for _, call := range calls {
		name := call.name
		for _, spelling := range []string{name, strings.ToUpper(name), strings.ToUpper(name[:1]) + name[1:]} {
			request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, endpoint, strings.NewReader("SELECT "+spelling+"("+call.argument+")"))
			if err != nil {
				t.Fatal(err)
			}
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(response.Body)
			response.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			if spelling == name || call.caseInsensitive {
				if response.StatusCode != http.StatusOK {
					t.Fatalf("%s: %s", spelling, body)
				}
			} else if response.Header.Get("X-ClickHouse-Exception-Code") != "46" {
				t.Fatalf("%s: expected unknown-function Code 46, got %d: %s", spelling, response.StatusCode, body)
			}
		}
	}
}

func TestMeasuredFunctionsGenerateRegistryCandidates(t *testing.T) {
	endpoint := os.Getenv("CHGEN_FUNCTION_RULES_URL")
	if endpoint == "" {
		t.Skip("CHGEN_FUNCTION_RULES_URL is not set")
	}
	directory := t.TempDir()
	evidence := filepath.Join(directory, "measurements.json")
	command := exec.CommandContext(t.Context(), "go", "run", "../tooling/cmd/functionrules", "-url", endpoint, "-evidence", evidence)
	if data, err := command.CombinedOutput(); err != nil {
		t.Fatalf("measure real column calls: %v\n%s", err, data)
	}
	data, err := os.ReadFile(evidence)
	if err != nil {
		t.Fatal(err)
	}
	var report struct {
		Functions []struct {
			Name  string `json:"name"`
			Cells []struct {
				Input         string `json:"input"`
				Analysis      string `json:"analysis"`
				Execution     string `json:"execution"`
				ExecutionRows int    `json:"execution_rows"`
			} `json:"cells"`
		} `json:"functions"`
	}
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, function := range report.Functions {
		for _, cell := range function.Cells {
			if function.Name == "sin" && cell.Input == "Nullable(Int32)" && cell.Analysis == "Nullable(Float64)" && cell.Execution == cell.Analysis && cell.ExecutionRows >= 4 {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("missing independent nullable analysis/execution witnesses")
	}
	candidate := filepath.Join(directory, "registry.go")
	command = exec.CommandContext(t.Context(), "go", "run", "../tooling/cmd/functionrules", "-evidence", evidence, "-registry", "../engine/registry.go", "-out", candidate)
	if data, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generate registry candidate: %v\n%s", err, data)
	}
	data, err = os.ReadFile(candidate)
	if err != nil || !strings.Contains(string(data), `"sin"`) || !strings.Contains(string(data), "functionrules/") {
		t.Fatalf("candidate has no measured sin specification: %v", err)
	}
	second := filepath.Join(directory, "second.go")
	command = exec.CommandContext(t.Context(), "go", "run", "../tooling/cmd/functionrules", "-evidence", evidence, "-registry", candidate, "-out", second)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("regenerate owned candidate: %v\n%s", err, output)
	}
	regenerated, err := os.ReadFile(second)
	if err != nil || !bytes.Equal(data, regenerated) {
		t.Fatalf("candidate generation is not deterministic: %v", err)
	}
}

func TestMeasuredRulesStayRegenerable(t *testing.T) {
	command := exec.CommandContext(t.Context(), "go", "run", "../tooling/cmd/functionrules", "-evidence", "../../testdata/clickhouse-function-rules.json", "-registry", "../engine/registry.go", "-check")
	if data, err := command.CombinedOutput(); err != nil {
		t.Fatalf("check pinned production rules: %v\n%s", err, data)
	}
	data, err := os.ReadFile("../engine/registry.go")
	if err != nil {
		t.Fatal(err)
	}
	mutated := bytes.Replace(data, []byte("functionrules/numeric-unary-v1/"), []byte("functionrules/numeric-unary-v1/corrupt-"), 1)
	path := filepath.Join(t.TempDir(), "registry.go")
	if err := os.WriteFile(path, mutated, 0o600); err != nil {
		t.Fatal(err)
	}
	command = exec.CommandContext(t.Context(), "go", "run", "../tooling/cmd/functionrules", "-evidence", "../../testdata/clickhouse-function-rules.json", "-registry", path, "-check")
	if data, err := command.CombinedOutput(); err == nil || !strings.Contains(string(data), "STALE") {
		t.Fatalf("stale generated rule was not reported: %v\n%s", err, data)
	}
}

func TestMeasuredFunctionsMatchPinnedEvidence(t *testing.T) {
	endpoint := os.Getenv("CHGEN_FUNCTION_RULES_URL")
	if endpoint == "" {
		t.Skip("CHGEN_FUNCTION_RULES_URL is not set")
	}
	report := filepath.Join(t.TempDir(), "live.json")
	command := exec.CommandContext(t.Context(), "go", "run", "../tooling/cmd/functionrules", "-url", endpoint, "-evidence", "../../testdata/clickhouse-function-rules.json", "-check", "-report", report)
	if data, err := command.CombinedOutput(); err != nil {
		t.Fatalf("re-measure pinned function contracts: %v\n%s", err, data)
	}
	if data, err := os.ReadFile(report); err != nil || len(data) == 0 {
		t.Fatalf("missing live evidence: %v", err)
	}
}

func TestGenerationRefusesToRetainAnUnprovedOwnedRule(t *testing.T) {
	data, err := os.ReadFile("../../testdata/clickhouse-function-rules.json")
	if err != nil {
		t.Fatal(err)
	}
	var report map[string]any
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatal(err)
	}
	function := report["functions"].([]any)[0].(map[string]any)
	cell := function["cells"].([]any)[0].(map[string]any)
	cell["execution"] = "UInt8"
	data, err = json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	evidence := filepath.Join(directory, "changed.json")
	candidate := filepath.Join(directory, "candidate.go")
	if err := os.WriteFile(evidence, data, 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), "go", "run", "../tooling/cmd/functionrules", "-evidence", evidence, "-registry", "../engine/registry.go", "-out", candidate)
	if data, err := command.CombinedOutput(); err == nil || !strings.Contains(string(data), "no longer satisfies") {
		t.Fatalf("unproved generated rule was retained: %v\n%s", err, data)
	}
	if _, err := os.Stat(candidate); !os.IsNotExist(err) {
		t.Fatalf("failed generation published a candidate: %v", err)
	}
}

func TestGenerationRejectsTrailingEvidenceDocuments(t *testing.T) {
	data, err := os.ReadFile("../../testdata/clickhouse-function-rules.json")
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	evidence := filepath.Join(directory, "extra.json")
	if err := os.WriteFile(evidence, append(data, []byte("\n{}")...), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), "go", "run", "../tooling/cmd/functionrules", "-evidence", evidence, "-registry", "../engine/registry.go", "-out", filepath.Join(directory, "candidate.go"))
	if data, err := command.CombinedOutput(); err == nil {
		t.Fatalf("multiple evidence documents were accepted:\n%s", data)
	}
}

func TestGenerationRejectsAnAcceptedOutsideDomain(t *testing.T) {
	data, err := os.ReadFile("../../testdata/clickhouse-function-rules.json")
	if err != nil {
		t.Fatal(err)
	}
	var report map[string]any
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatal(err)
	}
	function := report["functions"].([]any)[0].(map[string]any)
	for _, raw := range function["cells"].([]any) {
		cell := raw.(map[string]any)
		if cell["input"] == "String" {
			cell["analysis"] = "Float64"
			cell["execution"] = "Float64"
			cell["execution_rows"] = 4
			delete(cell, "analysis_code")
			delete(cell, "execution_code")
			break
		}
	}
	data, err = json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	evidence := filepath.Join(directory, "changed.json")
	if err := os.WriteFile(evidence, data, 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), "go", "run", "../tooling/cmd/functionrules", "-evidence", evidence, "-registry", "../engine/registry.go", "-out", filepath.Join(directory, "candidate.go"))
	if data, err := command.CombinedOutput(); err == nil || !strings.Contains(string(data), "no longer satisfies") {
		t.Fatalf("accepted negative domain was ignored: %v\n%s", err, data)
	}
}

func TestEvidenceKeepsActualFixtureValues(t *testing.T) {
	data, err := os.ReadFile("../../testdata/clickhouse-function-rules.json")
	if err != nil {
		t.Fatal(err)
	}
	var report struct {
		Functions []struct {
			Cells []struct {
				Input  string
				Values []string
			}
		}
	}
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatal(err)
	}
	for _, function := range report.Functions {
		for _, cell := range function.Cells {
			if cell.Input == "Nullable(Int32)" && strings.Join(cell.Values, ",") == "1,0,2,NULL" {
				return
			}
		}
	}
	t.Fatal("saved measurements omit the real nullable fixture values")
}

func TestGenerationRejectsUnrelatedServerErrorsAsDomainProof(t *testing.T) {
	data, err := os.ReadFile("../../testdata/clickhouse-function-rules.json")
	if err != nil {
		t.Fatal(err)
	}
	report, err := functionrules.Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	cell := &report.Functions[0].Cells[0]
	cell.Analysis, cell.Execution = "", ""
	cell.AnalysisCode, cell.ExecutionCode, cell.ExecutionRows = 497, 497, 0
	data, err = json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	evidence := filepath.Join(directory, "access-denied.json")
	if err := os.WriteFile(evidence, data, 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), "go", "run", "../tooling/cmd/functionrules", "-evidence", evidence, "-registry", "../engine/registry.go", "-out", filepath.Join(directory, "candidate.go"))
	if data, err := command.CombinedOutput(); err == nil || !strings.Contains(string(data), "no longer satisfies") {
		t.Fatalf("access denial became a type-domain refusal: %v\n%s", err, data)
	}
}

func TestGenerationRefusesMissingOwnedRules(t *testing.T) {
	data, err := os.ReadFile("../../testdata/clickhouse-function-rules.json")
	if err != nil {
		t.Fatal(err)
	}
	report, err := functionrules.Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	report.Functions = report.Functions[:1]
	data, err = json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	evidence := filepath.Join(directory, "partial.json")
	if err := os.WriteFile(evidence, data, 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), "go", "run", "../tooling/cmd/functionrules", "-evidence", evidence, "-registry", "../engine/registry.go", "-out", filepath.Join(directory, "candidate.go"))
	if data, err := command.CombinedOutput(); err == nil || !strings.Contains(string(data), "missing evidence for owned rule") {
		t.Fatalf("partial evidence retained orphaned generated rules: %v\n%s", err, data)
	}
}
