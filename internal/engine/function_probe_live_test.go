//go:build fuzzoracle

package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/IlyaGulya/chgen/internal/conformance"
	"github.com/IlyaGulya/chgen/internal/supportmanifest"
)

func TestFunctionProbeCatalogAgainstClickHouse(t *testing.T) {
	data, err := os.ReadFile(functionProbeCatalogPath)
	if err != nil {
		t.Fatal(err)
	}
	var catalog functionProbeCatalog
	if err := json.Unmarshal(data, &catalog); err != nil {
		t.Fatal(err)
	}
	if err := validateFunctionProbeCatalog(t, catalog); err != nil {
		t.Fatal(err)
	}
	schema, err := schemaFromDDLErr(t, oracleSchemaDDL)
	if err != nil {
		t.Fatal(err)
	}
	oracle := execWitnessFixture(t)
	var cells []conformance.Cell
	var inputs []conformance.Input
	executionTypes := make(map[string]string)

	for _, function := range catalog.Functions {
		function := function
		t.Run(function.Name, func(t *testing.T) {
			for _, probe := range function.Probes {
				probe := probe
				t.Run(probe.ID, func(t *testing.T) {
					analysisType, analysisErr := oracle.exec("SELECT toTypeName(" + probe.SQL + ") FROM t")
					chgenType, chgenErr := chgenInferType(schema, probe.SQL)
					execution := oracle.typeNames([]string{probe.SQL})[0]
					expectation := conformance.ExpectIllegal
					if isLegalFunctionProbeKind(probe.Kind) {
						expectation = conformance.ExpectLegal
					}
					input := conformance.Input{ID: probe.ID, Expression: probe.SQL, Table: "t", Fixture: "current-fixture", Expectation: expectation}
					cell := conformance.Cell{ID: input.ID, Expression: input.Expression, Table: input.Table, Fixture: input.Fixture, Expectation: expectation}
					if chgenErr != nil {
						cell.Chgen.Error = chgenErr.Error()
					} else {
						cell.Chgen = conformance.CanonicalResult(chgenType)
					}
					if analysisErr != nil {
						cell.Analysis = conformance.TypeResult{Error: analysisErr.Error(), ErrorCode: evidenceErrorCode(analysisErr.Error())}
					} else {
						cell.Analysis = conformance.CanonicalResult(strings.TrimSpace(analysisType))
					}
					if execution.err != "" {
						cell.Execution = conformance.ExecutionResult{Error: execution.err, ErrorCode: evidenceErrorCode(execution.err)}
					} else {
						cell.Execution.Ran = true
						executionTypes[probe.ID] = strings.TrimSpace(execution.typeName)
					}
					cells = append(cells, cell)
					inputs = append(inputs, input)
					if isLegalFunctionProbeKind(probe.Kind) {
						if analysisErr != nil {
							t.Errorf("analysis refused a legal probe: %s", firstLine(analysisErr.Error()))
						}
						if probe.ExpectedChgen == "accept" && chgenErr != nil {
							t.Errorf("chgen refused a legal probe: %v", chgenErr)
						}
						if probe.ExpectedChgen == "refuse_unsupported_result" && chgenErr == nil {
							t.Error("chgen accepted a legal server result that has no supported Go type")
						}
						if execution.err != "" {
							t.Errorf("execution refused a legal probe: %s", firstLine(execution.err))
						}
						if probe.ExpectedChgen == "accept" && analysisErr == nil && chgenErr == nil && execution.err == "" {
							if normalizeTypeName(chgenType) != normalizeTypeName(strings.TrimSpace(analysisType)) {
								t.Errorf("family result differs from analysis: chgen=%s analysis=%s", chgenType, strings.TrimSpace(analysisType))
							}
							if normalizeTypeName(chgenType) != normalizeTypeName(strings.TrimSpace(execution.typeName)) {
								t.Errorf("family result differs from execution: chgen=%s execution=%s", chgenType, strings.TrimSpace(execution.typeName))
							}
						}
						return
					}
					if chgenErr == nil {
						t.Error("chgen accepted an illegal probe")
					}
					if execution.err == "" {
						t.Errorf("execution accepted an illegal probe with type %s", strings.TrimSpace(execution.typeName))
					}
				})
			}
		})
	}
	if t.Failed() {
		return
	}
	sort.Slice(cells, func(left, right int) bool { return cells[left].ID < cells[right].ID })
	sort.Slice(inputs, func(left, right int) bool { return inputs[left].ID < inputs[right].ID })
	versionRaw, err := oracle.exec("SELECT version()")
	if err != nil {
		t.Fatal(err)
	}
	serverRun, uptime, err := readServerRun(oracle)
	if err != nil {
		t.Fatal(err)
	}
	columns, err := conformance.FixtureColumnsFromDDL(oracleSchemaDDL)
	if err != nil {
		t.Fatal(err)
	}
	catalogDigest := sha256.Sum256(data)
	evidence, err := supportmanifest.NewFunctionProbeEvidence(
		hex.EncodeToString(catalogDigest[:]),
		conformance.Report{
			Metadata: conformance.Metadata{
				Server:      conformance.ServerInfo{Version: strings.TrimSpace(versionRaw), ServerRun: serverRun, UptimeS: uptime},
				FixtureHash: conformance.StableHash(oracleSchemaDDL), SeedHash: conformance.StableHash(oracleSeedRow),
				MatrixHash: conformance.MatrixHash(inputs), FixtureColumns: columns,
			},
			Cells: cells,
		},
		executionTypes,
	)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := supportmanifest.MarshalFunctionProbeEvidence(evidence)
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("CHGEN_UPDATE_FUNCTION_FAMILY_EVIDENCE") == "1" {
		if err := os.WriteFile(functionProbeEvidencePath, encoded, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := supportmanifest.LoadFunctionEvidence(functionProbeCatalogPath, functionProbeEvidencePath); err != nil {
		t.Fatalf("validate pinned function evidence: %v", err)
	}
}

func evidenceErrorCode(message string) int {
	code, _ := strconv.Atoi(clickHouseErrorCode(message))
	return code
}
