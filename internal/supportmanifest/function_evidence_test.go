package supportmanifest

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	testFunctionCatalogPath  = "../../testdata/clickhouse-function-probes.json"
	testFunctionEvidencePath = "../../testdata/clickhouse-function-probe-evidence.json"
)

func TestPinnedFunctionEvidenceIsValid(t *testing.T) {
	measurements, err := LoadFunctionEvidence(testFunctionCatalogPath, testFunctionEvidencePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(measurements) != 236 {
		t.Fatalf("function evidence has %d measured functions, want 236", len(measurements))
	}
	data, err := os.ReadFile(testFunctionEvidencePath)
	if err != nil {
		t.Fatal(err)
	}
	var evidence FunctionProbeEvidence
	if err := json.Unmarshal(data, &evidence); err != nil {
		t.Fatal(err)
	}
	canonical, err := MarshalFunctionProbeEvidence(evidence)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, canonical) {
		t.Fatal("function evidence is not in its canonical form")
	}
}

func TestFunctionEvidenceRejectsEveryWitnessMutation(t *testing.T) {
	data, err := os.ReadFile(testFunctionEvidencePath)
	if err != nil {
		t.Fatal(err)
	}
	var original FunctionProbeEvidence
	if err := json.Unmarshal(data, &original); err != nil {
		t.Fatal(err)
	}
	tests := map[string]func(*FunctionProbeEvidence){
		"catalog digest": func(value *FunctionProbeEvidence) { value.CatalogSHA256 = "changed" },
		"server run":     func(value *FunctionProbeEvidence) { value.Report.Metadata.Server.ServerRun = 0 },
		"missing cell":   func(value *FunctionProbeEvidence) { value.Report.Cells = value.Report.Cells[1:] },
		"legal refusal": func(value *FunctionProbeEvidence) {
			for index := range value.Report.Cells {
				cell := &value.Report.Cells[index]
				if cell.Expectation == "legal" {
					cell.Execution.Ran = false
					cell.Execution.RefusalClass = clickHouseRefusalClass
					cell.Execution.ErrorCode = 43
					return
				}
			}
		},
		"illegal acceptance": func(value *FunctionProbeEvidence) {
			for index := range value.Report.Cells {
				cell := &value.Report.Cells[index]
				if cell.Expectation == "illegal" {
					cell.Execution.Ran = true
					cell.Execution.RefusalClass = ""
					cell.Execution.ErrorCode = 0
					return
				}
			}
		},
		"execution type": func(value *FunctionProbeEvidence) {
			for _, cell := range value.Report.Cells {
				if cell.Expectation == "legal" && cell.Chgen.Canonical != nil {
					value.ExecutionTypes[cell.ID] = "String"
					return
				}
			}
		},
		"unknown refusal class": func(value *FunctionProbeEvidence) {
			for index := range value.Report.Cells {
				cell := &value.Report.Cells[index]
				if cell.Execution.RefusalClass != "" {
					cell.Execution.RefusalClass = "copied server prose"
					return
				}
			}
		},
		"missing refusal code": func(value *FunctionProbeEvidence) {
			for index := range value.Report.Cells {
				cell := &value.Report.Cells[index]
				if cell.Execution.RefusalClass != "" {
					cell.Execution.ErrorCode = 0
					return
				}
			}
		},
		"unknown analysis refusal class": func(value *FunctionProbeEvidence) {
			for index := range value.Report.Cells {
				cell := &value.Report.Cells[index]
				if cell.Analysis.RefusalClass != "" {
					cell.Analysis.RefusalClass = "copied server prose"
					return
				}
			}
		},
		"missing analysis refusal code": func(value *FunctionProbeEvidence) {
			for index := range value.Report.Cells {
				cell := &value.Report.Cells[index]
				if cell.Analysis.RefusalClass == clickHouseRefusalClass {
					cell.Analysis.ErrorCode = 0
					return
				}
			}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			var candidate FunctionProbeEvidence
			if err := json.Unmarshal(data, &candidate); err != nil {
				t.Fatal(err)
			}
			mutate(&candidate)
			encoded, err := json.Marshal(candidate)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "evidence.json")
			if err := os.WriteFile(path, encoded, 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadFunctionEvidence(testFunctionCatalogPath, path); err == nil {
				t.Fatal("function evidence accepted a witness mutation")
			}
		})
	}
}

func TestFunctionEvidenceDigestBindsRefusalCodes(t *testing.T) {
	data, err := os.ReadFile(testFunctionEvidencePath)
	if err != nil {
		t.Fatal(err)
	}
	var original FunctionProbeEvidence
	if err := json.Unmarshal(data, &original); err != nil {
		t.Fatal(err)
	}
	candidate := original
	candidate.Report.Cells = append([]FunctionProbeCell(nil), original.Report.Cells...)
	changed := false
	for index := range candidate.Report.Cells {
		cell := &candidate.Report.Cells[index]
		if cell.Execution.RefusalClass == clickHouseRefusalClass {
			cell.Execution.ErrorCode++
			changed = true
			break
		}
	}
	if !changed {
		t.Fatal("function evidence has no refusal code to mutate")
	}
	if evidenceDigest(original) == evidenceDigest(candidate) {
		t.Fatal("function evidence digest did not bind a refusal code")
	}
}

func TestFunctionEvidenceRejectsLegacyServerErrorText(t *testing.T) {
	data, err := os.ReadFile(testFunctionEvidencePath)
	if err != nil {
		t.Fatal(err)
	}
	mutated := bytes.Replace(data, []byte(`"refusal_class": "clickhouse_refused"`),
		[]byte(`"error": "Code: 43. DB::Exception: copied prose"`), 1)
	if bytes.Equal(mutated, data) {
		t.Fatal("function evidence has no refusal class to mutate")
	}
	path := filepath.Join(t.TempDir(), "evidence.json")
	if err := os.WriteFile(path, mutated, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFunctionEvidence(testFunctionCatalogPath, path); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("legacy server error text was not refused: %v", err)
	}
}
