package supportmanifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"

	"github.com/IlyaGulya/chgen/internal/conformance"
)

// FunctionProbeEvidenceFormatVersion identifies the live function evidence format.
const FunctionProbeEvidenceFormatVersion = 2

const (
	clickHouseRefusalClass    = "clickhouse_refused"
	canonicalTypeRefusalClass = "canonical_type_parse_refused"
)

// FunctionMeasurement is one live function support claim.
type FunctionMeasurement struct {
	Family      string
	Evidence    string
	TestWitness string
}

// FunctionProbeEvidence binds live outcomes to one exact probe catalog.
type FunctionProbeEvidence struct {
	FormatVersion  int                 `json:"format_version"`
	CatalogSHA256  string              `json:"catalog_sha256"`
	Report         FunctionProbeReport `json:"report"`
	ExecutionTypes map[string]string   `json:"execution_types"`
}

// FunctionProbeReport is a measured report without server diagnostic prose.
type FunctionProbeReport struct {
	Metadata conformance.Metadata `json:"metadata"`
	Cells    []FunctionProbeCell  `json:"cells"`
}

// FunctionProbeCell records the facts that identify one function probe.
type FunctionProbeCell struct {
	ID          string                       `json:"id"`
	Expression  string                       `json:"expression"`
	Table       string                       `json:"table,omitempty"`
	Fixture     string                       `json:"fixture,omitempty"`
	Expectation conformance.Expectation      `json:"expectation,omitempty"`
	Chgen       conformance.TypeResult       `json:"chgen"`
	Analysis    FunctionProbeAnalysisResult  `json:"server_analysis"`
	Execution   FunctionProbeExecutionResult `json:"server_execution"`
	Native      conformance.NativeResult     `json:"native"`
}

// FunctionProbeAnalysisResult records a type or a bounded refusal class.
type FunctionProbeAnalysisResult struct {
	Raw          string            `json:"raw,omitempty"`
	Canonical    *conformance.Type `json:"canonical,omitempty"`
	RefusalClass string            `json:"refusal_class,omitempty"`
	ErrorCode    int               `json:"error_code,omitempty"`
}

// FunctionProbeExecutionResult records execution or a bounded refusal class.
type FunctionProbeExecutionResult struct {
	Ran          bool   `json:"ran"`
	RefusalClass string `json:"refusal_class,omitempty"`
	ErrorCode    int    `json:"error_code,omitempty"`
}

// NewFunctionProbeEvidence removes server diagnostic prose from a live report.
func NewFunctionProbeEvidence(catalogSHA256 string, report conformance.Report, executionTypes map[string]string) (FunctionProbeEvidence, error) {
	evidence := FunctionProbeEvidence{
		FormatVersion: FunctionProbeEvidenceFormatVersion, CatalogSHA256: catalogSHA256,
		Report: FunctionProbeReport{Metadata: report.Metadata}, ExecutionTypes: executionTypes,
	}
	for _, cell := range report.Cells {
		normalized := FunctionProbeCell{
			ID: cell.ID, Expression: cell.Expression, Table: cell.Table, Fixture: cell.Fixture,
			Expectation: cell.Expectation, Chgen: cell.Chgen, Native: cell.Native,
		}
		var err error
		normalized.Analysis, err = normalizeAnalysisResult(cell.Analysis)
		if err != nil {
			return FunctionProbeEvidence{}, fmt.Errorf("normalize function evidence cell %s analysis: %w", cell.ID, err)
		}
		normalized.Execution, err = normalizeExecutionResult(cell.Execution)
		if err != nil {
			return FunctionProbeEvidence{}, fmt.Errorf("normalize function evidence cell %s execution: %w", cell.ID, err)
		}
		evidence.Report.Cells = append(evidence.Report.Cells, normalized)
	}
	if err := validateNormalizedReport(evidence.Report); err != nil {
		return FunctionProbeEvidence{}, err
	}
	return evidence, nil
}

type evidenceCatalog struct {
	CHVersion   string `json:"clickhouse_version"`
	FixtureHash string `json:"fixture_hash"`
	Functions   []struct {
		Name   string `json:"name"`
		Family string `json:"family"`
		Probes []struct {
			ID            string `json:"id"`
			Kind          string `json:"kind"`
			SQL           string `json:"sql"`
			ExpectedChgen string `json:"expected_chgen"`
		} `json:"probes"`
	} `json:"functions"`
}

func normalizeAnalysisResult(result conformance.TypeResult) (FunctionProbeAnalysisResult, error) {
	normalized := FunctionProbeAnalysisResult{Raw: result.Raw, Canonical: result.Canonical, ErrorCode: result.ErrorCode}
	if len(result.Results) != 0 {
		return FunctionProbeAnalysisResult{}, fmt.Errorf("function analysis has a result vector")
	}
	if result.Error == "" {
		return normalized, nil
	}
	if result.ErrorCode > 0 {
		normalized.RefusalClass = clickHouseRefusalClass
		return normalized, nil
	}
	if result.Raw != "" {
		normalized.RefusalClass = canonicalTypeRefusalClass
		return normalized, nil
	}
	return FunctionProbeAnalysisResult{}, fmt.Errorf("analysis refusal has no stable code or raw type")
}

func normalizeExecutionResult(result conformance.ExecutionResult) (FunctionProbeExecutionResult, error) {
	normalized := FunctionProbeExecutionResult{Ran: result.Ran, ErrorCode: result.ErrorCode}
	if result.Error == "" {
		return normalized, nil
	}
	if result.ErrorCode <= 0 {
		return FunctionProbeExecutionResult{}, fmt.Errorf("execution refusal has no stable code")
	}
	normalized.RefusalClass = clickHouseRefusalClass
	return normalized, nil
}

func validateNormalizedReport(report FunctionProbeReport) error {
	converted := conformance.Report{Metadata: report.Metadata}
	for _, cell := range report.Cells {
		if err := validateAnalysisResult(cell.Analysis); err != nil {
			return fmt.Errorf("function evidence cell %s analysis: %w", cell.ID, err)
		}
		if err := validateExecutionResult(cell.Execution); err != nil {
			return fmt.Errorf("function evidence cell %s execution: %w", cell.ID, err)
		}
		converted.Cells = append(converted.Cells, conformance.Cell{
			ID: cell.ID, Expression: cell.Expression, Table: cell.Table, Fixture: cell.Fixture,
			Expectation: cell.Expectation, Chgen: cell.Chgen, Analysis: conformance.TypeResult{
				Raw: cell.Analysis.Raw, Canonical: cell.Analysis.Canonical,
				Error: cell.Analysis.RefusalClass, ErrorCode: cell.Analysis.ErrorCode,
			}, Execution: conformance.ExecutionResult{
				Ran: cell.Execution.Ran, Error: cell.Execution.RefusalClass, ErrorCode: cell.Execution.ErrorCode,
			}, Native: cell.Native,
		})
	}
	return conformance.ValidateReport(converted)
}

func validateAnalysisResult(result FunctionProbeAnalysisResult) error {
	switch result.RefusalClass {
	case "":
		if result.ErrorCode != 0 {
			return fmt.Errorf("analysis code has no refusal class")
		}
	case clickHouseRefusalClass:
		if result.ErrorCode <= 0 || result.Canonical != nil {
			return fmt.Errorf("ClickHouse analysis refusal has invalid facts")
		}
	case canonicalTypeRefusalClass:
		if result.ErrorCode != 0 || result.Raw == "" || result.Canonical != nil {
			return fmt.Errorf("canonical type refusal has invalid facts")
		}
	default:
		return fmt.Errorf("analysis has unknown refusal class %q", result.RefusalClass)
	}
	return nil
}

func validateExecutionResult(result FunctionProbeExecutionResult) error {
	if result.Ran {
		if result.RefusalClass != "" || result.ErrorCode != 0 {
			return fmt.Errorf("successful execution has refusal facts")
		}
		return nil
	}
	if result.RefusalClass != clickHouseRefusalClass || result.ErrorCode <= 0 {
		return fmt.Errorf("execution refusal has invalid facts")
	}
	return nil
}

// LoadFunctionEvidence validates the live artifact and returns measured functions.
func LoadFunctionEvidence(catalogPath, evidencePath string) (map[string]FunctionMeasurement, error) {
	catalogBytes, err := os.ReadFile(catalogPath)
	if err != nil {
		return nil, err
	}
	var catalog evidenceCatalog
	if err := json.Unmarshal(catalogBytes, &catalog); err != nil {
		return nil, fmt.Errorf("decode %s: %w", catalogPath, err)
	}
	var evidence FunctionProbeEvidence
	if err := loadJSON(evidencePath, &evidence); err != nil {
		return nil, err
	}
	if evidence.FormatVersion != FunctionProbeEvidenceFormatVersion {
		return nil, fmt.Errorf("function evidence format version is %d, want %d", evidence.FormatVersion, FunctionProbeEvidenceFormatVersion)
	}
	catalogDigest := sha256.Sum256(catalogBytes)
	if evidence.CatalogSHA256 != hex.EncodeToString(catalogDigest[:]) {
		return nil, fmt.Errorf("function evidence catalog digest is stale")
	}
	if evidence.Report.Metadata.Server.ServerRun <= 0 || evidence.Report.Metadata.Server.Version != catalog.CHVersion {
		return nil, fmt.Errorf("function evidence has an invalid server identity")
	}
	if evidence.Report.Metadata.FixtureHash != catalog.FixtureHash {
		return nil, fmt.Errorf("function evidence fixture identity is stale")
	}
	if err := validateNormalizedReport(evidence.Report); err != nil {
		return nil, fmt.Errorf("validate function evidence report: %w", err)
	}
	cells := make(map[string]FunctionProbeCell, len(evidence.Report.Cells))
	for _, cell := range evidence.Report.Cells {
		cells[cell.ID] = cell
	}
	measurements := make(map[string]FunctionMeasurement, len(catalog.Functions))
	expectedCells := 0
	expectedExecutionTypes := 0
	for _, function := range catalog.Functions {
		if function.Name == "" || function.Family == "" || len(function.Probes) == 0 {
			return nil, fmt.Errorf("function probe catalog has an incomplete family")
		}
		for _, probe := range function.Probes {
			expectedCells++
			if strings.HasPrefix(probe.Kind, "legal") {
				expectedExecutionTypes++
			}
			cell, found := cells[probe.ID]
			if !found || cell.Expression != probe.SQL {
				return nil, fmt.Errorf("function evidence has no exact cell %s", probe.ID)
			}
			if err := validateFunctionEvidenceCell(probe.Kind, probe.ExpectedChgen, cell, evidence.ExecutionTypes[probe.ID]); err != nil {
				return nil, fmt.Errorf("function evidence cell %s: %w", probe.ID, err)
			}
		}
		measurements[strings.ToLower(function.Name)] = FunctionMeasurement{
			Family:      function.Family,
			Evidence:    "clickhouse-function-probe-evidence/sha256:" + evidenceDigest(evidence),
			TestWitness: "internal/engine/function_probe_live_test.go:TestFunctionProbeCatalogAgainstClickHouse",
		}
	}
	if len(cells) != expectedCells || len(evidence.ExecutionTypes) != expectedExecutionTypes {
		return nil, fmt.Errorf("function evidence covers %d cells and %d execution types, want %d and %d",
			len(cells), len(evidence.ExecutionTypes), expectedCells, expectedExecutionTypes)
	}
	return measurements, nil
}

func validateFunctionEvidenceCell(kind, expectedChgen string, cell FunctionProbeCell, executionType string) error {
	legal := strings.HasPrefix(kind, "legal")
	if legal {
		if cell.Expectation != conformance.ExpectLegal ||
			(cell.Analysis.Canonical == nil && cell.Analysis.Raw == "") || !cell.Execution.Ran || executionType == "" {
			return fmt.Errorf("legal cell has an incomplete server witness")
		}
		if expectedChgen == "accept" {
			if cell.Chgen.Canonical == nil || cell.Analysis.Canonical == nil || !reflect.DeepEqual(*cell.Chgen.Canonical, *cell.Analysis.Canonical) {
				return fmt.Errorf("legal cell has a chgen and analysis type mismatch")
			}
			execution, err := conformance.ParseType(executionType)
			if err != nil || !reflect.DeepEqual(execution, *cell.Analysis.Canonical) {
				return fmt.Errorf("legal cell has an execution type mismatch")
			}
		} else if expectedChgen == "refuse_unsupported_result" {
			if cell.Chgen.Error == "" {
				return fmt.Errorf("unsupported legal result was not refused")
			}
		} else {
			return fmt.Errorf("legal cell has unknown chgen expectation %q", expectedChgen)
		}
		return nil
	}
	if cell.Expectation != conformance.ExpectIllegal || cell.Chgen.Error == "" || cell.Execution.RefusalClass == "" || cell.Execution.Ran || executionType != "" {
		return fmt.Errorf("illegal cell does not have two refusals")
	}
	return nil
}

func evidenceDigest(evidence FunctionProbeEvidence) string {
	copy := evidence
	copy.Report.Metadata.Server.UptimeS = 0
	data, _ := json.Marshal(copy)
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

// MarshalFunctionProbeEvidence returns stable live evidence bytes.
func MarshalFunctionProbeEvidence(evidence FunctionProbeEvidence) ([]byte, error) {
	if evidence.FormatVersion != FunctionProbeEvidenceFormatVersion {
		return nil, fmt.Errorf("function evidence format version is %d, want %d", evidence.FormatVersion, FunctionProbeEvidenceFormatVersion)
	}
	if err := validateNormalizedReport(evidence.Report); err != nil {
		return nil, err
	}
	sort.Slice(evidence.Report.Cells, func(left, right int) bool { return evidence.Report.Cells[left].ID < evidence.Report.Cells[right].ID })
	data, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
