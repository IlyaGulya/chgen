package functionrules

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
)

// This is the exact cell diff of run 38044757394 against the ARM evidence.
// Neither witness is rewritten. It is a restriction, not a wrong-type waiver:
// the generated resolver refuses these input products on BOTH builds.
//
//go:embed evidence/build-variants-v1.json
var buildVariantsData []byte

var buildVariants = sync.OnceValues(loadBuildVariants)

// BuildVariantEvidence returns a freshly decoded, validated witness for reports.
// Callers cannot mutate the cached policy used by the inference registry.
func BuildVariantEvidence() (Comparison, error) { return loadBuildVariants() }

func loadBuildVariants() (Comparison, error) {
	var comparison Comparison
	if err := json.Unmarshal(buildVariantsData, &comparison); err != nil {
		return comparison, err
	}
	if comparison.Format != 1 || comparison.Profile != Profile || comparison.Expected.Version != SemanticsVersion || comparison.Actual.Version != SemanticsVersion || comparison.Expected.PlanDigest != comparison.Actual.PlanDigest || len(comparison.Metadata) != 0 {
		return comparison, fmt.Errorf("invalid build-variant provenance")
	}
	if comparison.Expected.Source.Version != Version || comparison.Actual.Source.Version != Version || comparison.Expected.Source.Revision == 0 || comparison.Expected.Source.Revision != comparison.Actual.Source.Revision || comparison.Expected.Source.BuildID == "" || comparison.Actual.Source.BuildID == "" || len(comparison.Cells) == 0 {
		return comparison, fmt.Errorf("incomplete build-variant provenance")
	}
	seen := make(map[string]bool)
	for _, difference := range comparison.Cells {
		key := difference.Function + "/" + difference.Expected.ID
		if seen[key] || reflect.DeepEqual(difference.Expected, difference.Actual) {
			return comparison, fmt.Errorf("duplicate or unchanged build-variant cell %s", key)
		}
		seen[key] = true
		var recipe *probe
		for _, item := range profilePlan(comparison.Profile, difference.Function) {
			if item.id == difference.Expected.ID {
				recipe = &item
				break
			}
		}
		if recipe == nil || !slices.Contains(names, difference.Function) {
			return comparison, fmt.Errorf("foreign build-variant recipe")
		}
		for _, cell := range []Cell{difference.Expected, difference.Actual} {
			if cell.ID != recipe.id || cell.Input != recipe.input || cell.Expression != recipe.expression || !slices.Equal(cell.Values, recipe.values) || cell.AnalysisCode != 0 || cell.ExecutionCode != 0 || cell.Analysis != cell.Execution || cell.ExecutionRows != len(recipe.values) {
				return comparison, fmt.Errorf("invalid build-variant cell %s/%s", difference.Function, cell.ID)
			}
		}
	}
	return comparison, nil
}

func knownExcludedVariant(comparison Comparison) bool {
	witness, err := buildVariants()
	if err != nil || len(comparison.Metadata) != 0 {
		return false
	}
	if comparison.Profile != witness.Profile || comparison.Expected.PlanDigest != witness.Expected.PlanDigest || comparison.Actual.PlanDigest != witness.Actual.PlanDigest {
		return false
	}
	if comparison.Expected.SemanticDigest == witness.Expected.SemanticDigest && comparison.Actual.SemanticDigest == witness.Actual.SemanticDigest {
		return reflect.DeepEqual(comparison.Cells, witness.Cells)
	}
	if comparison.Expected.SemanticDigest != witness.Actual.SemanticDigest || comparison.Actual.SemanticDigest != witness.Expected.SemanticDigest || len(comparison.Cells) != len(witness.Cells) {
		return false
	}
	for i, cell := range comparison.Cells {
		other := witness.Cells[i]
		if cell.Function != other.Function || !reflect.DeepEqual(cell.Expected, other.Actual) || !reflect.DeepEqual(cell.Actual, other.Expected) {
			return false
		}
	}
	return true
}

func excludedCell(function string, cell Cell) bool {
	witness, err := buildVariants()
	if err != nil {
		return false
	}
	for _, difference := range witness.Cells {
		if difference.Function == function && difference.Expected.ID == cell.ID {
			return reflect.DeepEqual(cell, difference.Expected) || reflect.DeepEqual(cell, difference.Actual)
		}
	}
	return false
}

func nonPortableInputs(function Function) ([]string, error) {
	witness, err := buildVariants()
	if err != nil {
		return nil, err
	}
	var inputs []string
	for _, difference := range witness.Cells {
		if difference.Function != function.Name {
			continue
		}
		found := false
		for _, cell := range function.Cells {
			if cell.ID == difference.Expected.ID {
				if !excludedCell(function.Name, cell) {
					return nil, fmt.Errorf("unexplained build-variant cell %s/%s", function.Name, cell.ID)
				}
				found = true
			}
		}
		if !found {
			return nil, fmt.Errorf("missing build-variant witness")
		}
		inputs = append(inputs, difference.Expected.Input)
	}
	// A primitive restriction also excludes its wrappers. An aggregate marker
	// restriction applies to every aggregate name, not just fixture anyLast.
	var reduced []string
	for _, input := range inputs {
		if strings.Contains(input, "Float32") && slices.Contains(inputs, "Float32") {
			input = "Float32"
		}
		if !slices.Contains(reduced, input) {
			reduced = append(reduced, input)
		}
	}
	slices.Sort(reduced)
	return reduced, nil
}
