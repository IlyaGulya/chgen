package functionrules

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"reflect"

	"github.com/IlyaGulya/chgen/internal/apiinventory"
)

// SemanticsVersion versions the interpretation of a measurement, independently
// of the server build that witnessed it. Plan identity includes SQL and values.
const SemanticsVersion = "functionrules-semantics-v1"

type Identity struct {
	Version        string              `json:"semantics_version"`
	PlanDigest     string              `json:"plan_digest"`
	SemanticDigest string              `json:"semantic_digest"`
	Source         apiinventory.Source `json:"source"`
}

type CellDifference struct {
	Function string `json:"function"`
	Expected Cell   `json:"expected"`
	Actual   Cell   `json:"actual"`
}

type Comparison struct {
	Format   int              `json:"format"`
	Profile  string           `json:"profile"`
	Expected Identity         `json:"expected"`
	Actual   Identity         `json:"actual"`
	Metadata []string         `json:"metadata,omitempty"`
	Policy   string           `json:"policy,omitempty"`
	Cells    []CellDifference `json:"cells"`
}

func digest(value any) string {
	data, _ := json.Marshal(value)
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func planDigest(report Report) string {
	var plans []Cell
	for _, function := range report.Functions {
		for _, cell := range function.Cells {
			plans = append(plans, Cell{ID: cell.ID, Input: cell.Input, Expression: cell.Expression, Values: cell.Values})
		}
	}
	return digest(struct {
		Version, Profile string
		Plan             []Cell
	}{SemanticsVersion, report.Profile, plans})
}

func identity(report Report) Identity {
	semantic := report
	semantic.Source.BuildID = ""
	return Identity{Version: SemanticsVersion, PlanDigest: planDigest(report), SemanticDigest: digest(struct {
		Version string
		Report  Report
	}{SemanticsVersion, semantic}), Source: report.Source}
}

// Compare keeps build provenance separate from semantic equivalence. Every
// changed witness is reported; no type or error-code drift is normalized away.
func Compare(expected, actual Report) Comparison {
	result := Comparison{Format: 1, Profile: expected.Profile, Expected: identity(expected), Actual: identity(actual), Cells: []CellDifference{}}
	if expected.Profile != actual.Profile || expected.Format != actual.Format || expected.Source.Version != actual.Source.Version || expected.Source.Revision != actual.Source.Revision {
		result.Metadata = append(result.Metadata, "measurement format, profile, server version or revision changed")
	}
	actualFunctions := make(map[string]Function)
	for _, function := range actual.Functions {
		actualFunctions[function.Name] = function
	}
	for _, function := range expected.Functions {
		other, found := actualFunctions[function.Name]
		delete(actualFunctions, function.Name)
		if !found || function.CaseInsensitive != other.CaseInsensitive {
			result.Metadata = append(result.Metadata, "function metadata changed: "+function.Name)
		}
		cells := make(map[string]Cell)
		for _, cell := range other.Cells {
			cells[cell.ID] = cell
		}
		for _, cell := range function.Cells {
			if !reflect.DeepEqual(cell, cells[cell.ID]) {
				result.Cells = append(result.Cells, CellDifference{function.Name, cell, cells[cell.ID]})
			}
			delete(cells, cell.ID)
		}
		if len(cells) != 0 {
			result.Metadata = append(result.Metadata, "unexpected cells: "+function.Name)
		}
	}
	if len(actualFunctions) != 0 {
		result.Metadata = append(result.Metadata, "unexpected function roster")
	}
	if knownExcludedVariant(result) {
		result.Policy = "excluded-build-dependent-inputs-v1"
	}
	return result
}

func (c Comparison) Matched() bool {
	return len(c.Metadata) == 0 && (len(c.Cells) == 0 || c.Policy == "excluded-build-dependent-inputs-v1" && knownExcludedVariant(c))
}

// Explain renders all cells, not just the first mismatch, for CI job logs.
func (c Comparison) Explain(w io.Writer) {
	if c.Policy != "" {
		fmt.Fprintf(w, "KNOWN %d build-dependent cells; excluded from offline support (%s)\n", len(c.Cells), c.Policy)
	}
	fmt.Fprintf(w, "profile=%s semantics=%s\nexpected build=%s semantic_digest=%s plan_digest=%s\nactual build=%s semantic_digest=%s plan_digest=%s\n", c.Profile, SemanticsVersion, c.Expected.Source.BuildID, c.Expected.SemanticDigest, c.Expected.PlanDigest, c.Actual.Source.BuildID, c.Actual.SemanticDigest, c.Actual.PlanDigest)
	for _, message := range c.Metadata {
		fmt.Fprintln(w, message)
	}
	for _, cell := range c.Cells {
		fmt.Fprintf(w, "%s [%s] %s: expected analysis=%s execution=%s codes=%d/%d rows=%d; actual analysis=%s execution=%s codes=%d/%d rows=%d\n", cell.Function, cell.Expected.ID, cell.Expected.Expression, cell.Expected.Analysis, cell.Expected.Execution, cell.Expected.AnalysisCode, cell.Expected.ExecutionCode, cell.Expected.ExecutionRows, cell.Actual.Analysis, cell.Actual.Execution, cell.Actual.AnalysisCode, cell.Actual.ExecutionCode, cell.Actual.ExecutionRows)
	}
}
