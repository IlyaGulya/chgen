package functionrules_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestStringRulesStayRegenerableAndRefuseUnprovedEvidence(t *testing.T) {
	command := exec.CommandContext(t.Context(), "go", "run", "../tooling/cmd/functionrules", "-evidence", "../../testdata/clickhouse-string-function-rules.json", "-registry", "../engine/registry.go", "-check")
	if data, err := command.CombinedOutput(); err != nil {
		t.Fatalf("pinned string regeneration: %v\n%s", err, data)
	}
	for _, mutation := range []string{"missing-function", "inconsistent-result", "unrelated-server-error"} {
		t.Run(mutation, func(t *testing.T) {
			data, err := readExpandedEvidence("../../testdata/clickhouse-string-function-rules.json")
			if err != nil {
				t.Fatal(err)
			}
			var report map[string]any
			if err := json.Unmarshal(data, &report); err != nil {
				t.Fatal(err)
			}
			functions := report["functions"].([]any)
			function := functions[0].(map[string]any)
			cells := function["cells"].([]any)
			switch mutation {
			case "missing-function":
				report["functions"] = functions[:1]
			case "inconsistent-result":
				cells[0].(map[string]any)["execution"] = "UInt8"
			case "unrelated-server-error":
				for _, item := range cells {
					cell := item.(map[string]any)
					if cell["id"] == "FixedString(8)" {
						delete(cell, "analysis")
						delete(cell, "execution")
						delete(cell, "execution_rows")
						cell["analysis_code"] = 497
						cell["execution_code"] = 497
					}
				}
			}
			data, err = json.Marshal(report)
			if err != nil {
				t.Fatal(err)
			}
			evidence := filepath.Join(t.TempDir(), "invalid.json")
			output := filepath.Join(t.TempDir(), "registry.go")
			if err := os.WriteFile(evidence, data, 0o600); err != nil {
				t.Fatal(err)
			}
			command := exec.CommandContext(t.Context(), "go", "run", "../tooling/cmd/functionrules", "-evidence", evidence, "-registry", "../engine/registry.go", "-out", output)
			if data, err := command.CombinedOutput(); err == nil {
				t.Fatalf("unproved string rules retained: %s", data)
			}
			if _, err := os.Stat(output); !os.IsNotExist(err) {
				t.Fatalf("refused generation wrote output: %v", err)
			}
		})
	}
}

func TestStringProfileRefusesUnsafeRecipesBeforeConnecting(t *testing.T) {
	command := exec.CommandContext(t.Context(), "go", "run", "../tooling/cmd/functionrules", "-profile", "string-unary-v1", "-url", "http://127.0.0.1:1", "-functions", "sleep", "-evidence", filepath.Join(t.TempDir(), "unsafe.json"))
	if data, err := command.CombinedOutput(); err == nil || !strings.Contains(string(data), "unknown or repeated safe recipe") {
		t.Fatalf("unsafe discovery became execution: %v\n%s", err, data)
	}
}

func TestStringProfileMeasuresAndGeneratesRules(t *testing.T) {
	endpoint := os.Getenv("CHGEN_FUNCTION_RULES_URL")
	if endpoint == "" {
		t.Skip("CHGEN_FUNCTION_RULES_URL is not set")
	}
	evidence := filepath.Join(t.TempDir(), "strings.json")
	command := exec.CommandContext(t.Context(), "go", "run", "../tooling/cmd/functionrules", "-profile", "string-unary-v1", "-url", endpoint, "-evidence", evidence)
	if data, err := command.CombinedOutput(); err != nil {
		t.Fatalf("measure string profile: %v\n%s", err, data)
	}
	candidate := filepath.Join(t.TempDir(), "registry.go")
	command = exec.CommandContext(t.Context(), "go", "run", "../tooling/cmd/functionrules", "-evidence", evidence, "-registry", "../engine/registry.go", "-out", candidate)
	if data, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generate string rules: %v\n%s", err, data)
	}
	data, err := os.ReadFile(candidate)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `exactSpelling: "lowerUTF8"`) {
		t.Fatal("generated string rule lost exact spelling")
	}
	command = exec.CommandContext(t.Context(), "go", "run", "../tooling/cmd/functionrules", "-evidence", evidence, "-registry", candidate, "-check")
	if data, err := command.CombinedOutput(); err != nil {
		t.Fatalf("string regeneration is not deterministic: %v\n%s", err, data)
	}
}
