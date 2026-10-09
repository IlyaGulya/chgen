package coverage

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/IlyaGulya/chgen/internal/engine"
	"github.com/IlyaGulya/chgen/internal/sqlir"
)

// Compare attaches observations from another frontend for this exact corpus.
// Candidate artifacts are data, never authority to generate or execute SQL.
func Compare(report *Report, data []byte) error {
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return err
	}
	var candidate Report
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&candidate); err != nil {
		return fmt.Errorf("decode candidate report: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("candidate must contain exactly one report")
	}
	if candidate.Version != report.Version || candidate.CorpusSHA256 != report.CorpusSHA256 || candidate.ClickHouseVersion != report.ClickHouseVersion {
		return fmt.Errorf("candidate report has a different corpus or format identity")
	}
	if candidate.Frontend == "" || candidate.ComparedFrontend != "" || len(candidate.Cases) != len(report.Cases) {
		return fmt.Errorf("candidate requires its own frontend identity and the complete uncombined case set")
	}
	byID := make(map[string]Observation)
	for _, cell := range candidate.Cases {
		if _, duplicate := byID[cell.ID]; cell.ID == "" || duplicate {
			return fmt.Errorf("candidate has a missing or repeated case id %q", cell.ID)
		}
		if (cell.IR != nil) != (cell.Stages["lower"].Status == "passed") || cell.IR != nil && (cell.IR.Version != 1 || cell.Stages["parse"].Status != "passed") {
			return fmt.Errorf("candidate case %s has an inconsistent IR observation", cell.ID)
		}
		byID[cell.ID] = cell
	}
	comparable := 0
	for i := range report.Cases {
		local := &report.Cases[i]
		other, ok := byID[local.ID]
		if !ok || local.Scope != other.Scope || local.Source != other.Source {
			return fmt.Errorf("candidate is missing or relabels case %s", local.ID)
		}
		local.Stages["ir_comparison"] = engine.CoverageStage{Status: "not_run"}
		local.Stages["frontend_type_comparison"] = engine.CoverageStage{Status: "not_run"}
		status := "neither_accepted"
		localParsed, otherParsed := local.Stages["parse"].Status == "passed", other.Stages["parse"].Status == "passed"
		switch {
		case localParsed && otherParsed:
			status = "both_accepted"
		case localParsed:
			status = "local_only"
		case otherParsed:
			status = "candidate_only"
		}
		local.Stages["parse_comparison"] = engine.CoverageStage{Status: status}
		if local.IR != nil && other.IR != nil {
			comparable++
			local.Stages["ir_comparison"] = compareObservation(irStructure(local.IR), irStructure(other.IR))
		}
		if len(local.Columns) > 0 && len(other.Columns) > 0 {
			local.Stages["frontend_type_comparison"] = compareObservation(local.Columns, other.Columns)
		}
	}
	if comparable == 0 {
		return fmt.Errorf("no complete IR pairs are comparable; parser acceptance alone is not an equivalence check")
	}
	report.ComparedFrontend = candidate.Frontend
	report.Warning += " Frontend comparison checks complete lowered structures and available result vectors, not SQL equivalence, source positions, or runtime values. Unmodeled structures are not compared."
	report.recount()
	return nil
}

// Source ranges describe provenance, not semantic structure. A frontend that
// cannot supply ranges may still compare its complete syntax tree.
func irStructure(document *sqlir.Document) any {
	data, _ := json.Marshal(document)
	var value any
	_ = json.Unmarshal(data, &value)
	removeIRSpans(value)
	return value
}

func removeIRSpans(value any) {
	switch value := value.(type) {
	case map[string]any:
		delete(value, "span")
		for _, child := range value {
			removeIRSpans(child)
		}
	case []any:
		for _, child := range value {
			removeIRSpans(child)
		}
	}
}

func compareObservation(left, right any) engine.CoverageStage {
	// Comparing the wire representation avoids treating nil and empty omitted
	// slices as different, and gives stable JSON-pointer paths in diagnostics.
	leftJSON, _ := json.Marshal(left)
	rightJSON, _ := json.Marshal(right)
	var leftValue, rightValue any
	_ = json.Unmarshal(leftJSON, &leftValue)
	_ = json.Unmarshal(rightJSON, &rightValue)
	if path := firstDifference(leftValue, rightValue, ""); path != "" {
		return engine.CoverageStage{Status: "mismatch", Message: "frontend observations differ at " + path}
	}
	return engine.CoverageStage{Status: "matched"}
}

func firstDifference(left, right any, path string) string {
	if reflect.DeepEqual(left, right) {
		return ""
	}
	switch value := left.(type) {
	case map[string]any:
		if other, ok := right.(map[string]any); ok {
			keys := maps.Clone(value)
			maps.Copy(keys, other)
			for _, key := range slices.Sorted(maps.Keys(keys)) {
				childPath := path + "/" + strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
				leftChild, leftExists := value[key]
				rightChild, rightExists := other[key]
				if leftExists != rightExists {
					return childPath
				}
				if difference := firstDifference(leftChild, rightChild, childPath); difference != "" {
					return difference
				}
			}
		}
	case []any:
		if other, ok := right.([]any); ok && len(value) == len(other) {
			for i := range value {
				if difference := firstDifference(value[i], other[i], path+"/"+strconv.Itoa(i)); difference != "" {
					return difference
				}
			}
		}
	}
	if path == "" {
		return "/"
	}
	return path
}
