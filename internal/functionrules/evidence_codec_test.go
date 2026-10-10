package functionrules_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IlyaGulya/chgen/internal/functionrules"
)

// Mutation tests operate on expanded semantic witnesses, independently of
// their storage format. Assertions and corrupted cells stay unchanged.
func readExpandedEvidence(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	report, err := functionrules.Decode(data)
	if err != nil {
		return nil, err
	}
	return json.MarshalIndent(report, "", "  ")
}

func TestEvidenceConversionKeepsEveryWitnessAndGeneratedRule(t *testing.T) {
	// Independent byte identities of the original expanded, observed reports.
	// These are deliberately not recomputed through the codec under test.
	for _, fixture := range []struct{ name, sha256 string }{
		{"clickhouse-function-rules.json", "e2bc458a05a99414d010d2faadb233784b0e6d2978a9b173364c109ec0e3955b"},
		{"clickhouse-string-function-rules.json", "44f08bbe775fc5eddefa53a127905b4c3b14546d919c5a1a6126325452b24a5f"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			original := filepath.Join("../../testdata", fixture.name)
			compact := filepath.Join(t.TempDir(), "compact.json")
			command := exec.CommandContext(t.Context(), "go", "run", "../tooling/cmd/functionrules", "-compact", "-evidence", original, "-out", compact)
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("compact saved evidence: %v\n%s", err, output)
			}
			full := filepath.Join(t.TempDir(), "expanded.json")
			command = exec.CommandContext(t.Context(), "go", "run", "../tooling/cmd/functionrules", "-expand", "-evidence", compact, "-out", full)
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("restore diagnostic evidence: %v\n%s", err, output)
			}
			after, err := os.ReadFile(full)
			if err != nil {
				t.Fatal(err)
			}
			if fmt.Sprintf("%x", sha256.Sum256(after)) != fixture.sha256 {
				t.Fatal("conversion changed an original measurement")
			}
			packed, err := os.ReadFile(compact)
			if err != nil {
				t.Fatal(err)
			}
			if len(packed)*50 >= len(after) {
				t.Fatalf("insufficient reduction: %d -> %d", len(after), len(packed))
			}
			second := filepath.Join(t.TempDir(), "second.json")
			command = exec.CommandContext(t.Context(), "go", "run", "../tooling/cmd/functionrules", "-compact", "-evidence", full, "-out", second)
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("repeat conversion: %v\n%s", err, output)
			}
			repeated, err := os.ReadFile(second)
			if err != nil || !bytes.Equal(packed, repeated) {
				t.Fatalf("conversion is not deterministic: %v", err)
			}
			command = exec.CommandContext(t.Context(), "go", "run", "../tooling/cmd/functionrules", "-evidence", compact, "-registry", "../engine/registry.go", "-check")
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("compact evidence cannot check original rules: %v\n%s", err, output)
			}
		})
	}
}

func TestCompactEvidenceRejectsNullOutcomeInsteadOfTreatingItAsZero(t *testing.T) {
	compact := filepath.Join(t.TempDir(), "compact.json")
	command := exec.CommandContext(t.Context(), "go", "run", "../tooling/cmd/functionrules", "-compact", "-evidence", "../../testdata/clickhouse-string-function-rules.json", "-out", compact)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("compact: %v\n%s", err, output)
	}
	data, err := os.ReadFile(compact)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	function := document["functions"].([]any)[0].(map[string]any)
	indexes := function["outcomes"].([]any)
	if indexes[0] != float64(0) {
		t.Fatal("first witnessed outcome must use index zero")
	}
	indexes[0] = nil
	data, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	corrupt := filepath.Join(t.TempDir(), "corrupt.json")
	if err := os.WriteFile(corrupt, data, 0o600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "expanded.json")
	command = exec.CommandContext(t.Context(), "go", "run", "../tooling/cmd/functionrules", "-expand", "-evidence", corrupt, "-out", output)
	if text, err := command.CombinedOutput(); err == nil || !strings.Contains(string(text), "null outcome") {
		t.Fatalf("null witness reference accepted: %v\n%s", err, text)
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("invalid evidence published output: %v", err)
	}
}

func TestCompactEvidenceValidatesThePlanAndEveryOutcomeBeyondItsChecksum(t *testing.T) {
	packed, err := os.ReadFile("../../testdata/clickhouse-string-function-rules.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []string{"plan-version", "plan-digest", "truncated", "negative-index", "large-index", "duplicate-function", "orphan-outcome", "execution-rows", "unknown-field", "checksum", "trailing-document"} {
		t.Run(mutation, func(t *testing.T) {
			var document map[string]json.RawMessage
			if err := json.Unmarshal(packed, &document); err != nil {
				t.Fatal(err)
			}
			var functions []struct {
				Name            string `json:"name"`
				CaseInsensitive bool   `json:"case_insensitive"`
				Outcomes        []int  `json:"outcomes"`
			}
			if err := json.Unmarshal(document["functions"], &functions); err != nil {
				t.Fatal(err)
			}
			var outcomes []json.RawMessage
			if err := json.Unmarshal(document["outcomes"], &outcomes); err != nil {
				t.Fatal(err)
			}
			want := ""
			switch mutation {
			case "plan-version":
				document["plan_version"] = json.RawMessage(`"future-plan"`)
				want = "plan version"
			case "plan-digest":
				document["plan_digest"] = json.RawMessage(`"wrong"`)
				want = "plan digest"
			case "truncated":
				functions[0].Outcomes = functions[0].Outcomes[:1]
				want = "incomplete probe matrix"
			case "negative-index":
				functions[0].Outcomes[0] = -1
				want = "invalid outcome index"
			case "large-index":
				functions[0].Outcomes[0] = len(outcomes)
				want = "invalid outcome index"
			case "duplicate-function":
				functions[1].Name = functions[0].Name
				want = "duplicate function recipe"
			case "orphan-outcome":
				outcomes = append(outcomes, outcomes[0])
				want = "unused or duplicate"
			case "execution-rows":
				outcomes[0] = bytes.Replace(outcomes[0], []byte(`"execution_rows":4`), []byte(`"execution_rows":3`), 1)
				want = "missing execution row witnesses"
			case "unknown-field":
				document["unwitnessed"] = json.RawMessage(`true`)
				want = "unknown field"
			case "checksum":
				document["checksum"] = json.RawMessage(`"wrong"`)
				want = "checksum mismatch"
			case "trailing-document":
				want = "exactly one JSON document"
			}
			document["functions"], err = json.Marshal(functions)
			if err != nil {
				t.Fatal(err)
			}
			document["outcomes"], err = json.Marshal(outcomes)
			if err != nil {
				t.Fatal(err)
			}
			if mutation != "checksum" {
				resignEvidence(t, document)
			}
			data, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			if mutation == "trailing-document" {
				data = append(data, []byte("\n{}")...)
			}
			input := filepath.Join(t.TempDir(), "invalid.json")
			if err := os.WriteFile(input, data, 0o600); err != nil {
				t.Fatal(err)
			}
			output := filepath.Join(t.TempDir(), "expanded.json")
			command := exec.CommandContext(t.Context(), "go", "run", "../tooling/cmd/functionrules", "-expand", "-evidence", input, "-out", output)
			if text, err := command.CombinedOutput(); err == nil || !strings.Contains(string(text), want) {
				t.Fatalf("%s: %v\n%s", mutation, err, text)
			}
			if _, err := os.Stat(output); !os.IsNotExist(err) {
				t.Fatalf("invalid evidence published output: %v", err)
			}
		})
	}
}

// Recompute a checksum independently of the codec so malformed but correctly
// checksummed plans must reach the structural and semantic validation gates.
func resignEvidence(t *testing.T, document map[string]json.RawMessage) {
	t.Helper()
	payload := struct {
		Format      json.RawMessage `json:"format"`
		Profile     json.RawMessage `json:"profile"`
		Source      json.RawMessage `json:"source"`
		PlanVersion json.RawMessage `json:"plan_version"`
		PlanDigest  json.RawMessage `json:"plan_digest"`
		Outcomes    json.RawMessage `json:"outcomes"`
		Functions   json.RawMessage `json:"functions"`
	}{document["format"], document["profile"], document["source"], document["plan_version"], document["plan_digest"], document["outcomes"], document["functions"]}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	document["checksum"], err = json.Marshal(fmt.Sprintf("%x", sha256.Sum256(encoded)))
	if err != nil {
		t.Fatal(err)
	}
}

func TestMeasurementWritesCompactEvidenceButKeepsExpandedLiveDiagnostics(t *testing.T) {
	endpoint := os.Getenv("CHGEN_FUNCTION_RULES_URL")
	if endpoint == "" {
		t.Skip("CHGEN_FUNCTION_RULES_URL is not set")
	}
	evidence := filepath.Join(t.TempDir(), "measured.json")
	command := exec.CommandContext(t.Context(), "go", "run", "../tooling/cmd/functionrules", "-url", endpoint, "-functions", "sin", "-evidence", evidence)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("measure: %v\n%s", err, output)
	}
	data, err := os.ReadFile(evidence)
	if err != nil {
		t.Fatal(err)
	}
	var header struct {
		Format int `json:"format"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		t.Fatal(err)
	}
	if header.Format != 2 {
		t.Fatalf("new measurements should use compact format 2, got %d", header.Format)
	}
	live := filepath.Join(t.TempDir(), "actual.json")
	command = exec.CommandContext(t.Context(), "go", "run", "../tooling/cmd/functionrules", "-url", endpoint, "-check", "-evidence", evidence, "-report", live)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("verify compact evidence live: %v\n%s", err, output)
	}
	data, err = os.ReadFile(live)
	if err != nil {
		t.Fatal(err)
	}
	var actual functionrules.Report
	if err := json.Unmarshal(data, &actual); err != nil {
		t.Fatal(err)
	}
	if actual.Format != 1 || len(actual.Functions) != 1 || actual.Functions[0].Name != "sin" {
		t.Fatalf("live diagnostic is not expanded: %+v", actual)
	}
	cell := actual.Functions[0].Cells[0]
	if cell.Input != "Int8" || cell.Expression != "sin(c)" || strings.Join(cell.Values, ",") != "1,0,2,0" || cell.Analysis != "Float64" || cell.Execution != "Float64" || cell.ExecutionRows != 4 {
		t.Fatalf("diagnostic lost a measured witness: %+v", cell)
	}
}
