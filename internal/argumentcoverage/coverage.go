// Package argumentcoverage reports bounded measured support without changing
// the independent inference, measurement or oracle policies.
package argumentcoverage

import (
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/IlyaGulya/chgen"
	"github.com/IlyaGulya/chgen/internal/functionrules"
)

// ArgumentReport describes finite measured recipes, not a promise about every
// overload of a function. Existing live comparisons remain independent gates.
type ArgumentReport struct {
	Format         int                       `json:"format"`
	Scope          string                    `json:"scope"`
	Evidence       []functionrules.Identity  `json:"evidence"`
	Total          int                       `json:"total"`
	Counts         map[string]int            `json:"counts"`
	FunctionCounts map[string]map[string]int `json:"function_counts"`
	Cells          []ArgumentCell            `json:"cells"`
	WrapperGrid    *WrapperReport            `json:"wrapper_grid,omitempty"`
	BuildVariants  *functionrules.Comparison `json:"build_variants,omitempty"`
}

type ArgumentCell struct {
	Profile         string                        `json:"profile"`
	Function        string                        `json:"function"`
	ID              string                        `json:"id"`
	Input           string                        `json:"input"`
	Expression      string                        `json:"expression"`
	Status          string                        `json:"status"`
	ChgenType       string                        `json:"chgen_type,omitempty"`
	Diagnostic      string                        `json:"diagnostic,omitempty"`
	Analysis        string                        `json:"server_analysis,omitempty"`
	Execution       string                        `json:"server_execution,omitempty"`
	AnalysisCode    int                           `json:"analysis_code,omitzero"`
	ExecutionCode   int                           `json:"execution_code,omitzero"`
	BuildDifference *functionrules.CellDifference `json:"build_difference,omitempty"`
}

// ArgumentCoverage re-evaluates every validated recipe through the public API.
// It retains wrong-type findings rather than disguising them as unsupported.
func ArgumentCoverage(reports ...functionrules.Report) (ArgumentReport, error) {
	result := ArgumentReport{Format: 1, Scope: "finite numeric/string measurement recipes; not all overloads or all registered functions", Counts: map[string]int{}, FunctionCounts: map[string]map[string]int{}, Cells: []ArgumentCell{}}
	if len(reports) == 0 {
		return result, fmt.Errorf("argument coverage needs measurement evidence")
	}
	variants, err := functionrules.BuildVariantEvidence()
	if err != nil {
		return result, err
	}
	result.BuildVariants = &variants
	seen := map[string]bool{}
	for _, report := range reports {
		data, err := json.Marshal(report)
		if err != nil {
			return result, err
		}
		if _, err := functionrules.Decode(data); err != nil {
			return result, err
		}
		roster, err := functionrules.RecipeNames(report.Profile)
		if err != nil {
			return result, err
		}
		if len(report.Functions) != len(roster) {
			return result, fmt.Errorf("incomplete coverage roster for %s", report.Profile)
		}
		if seen[report.Profile] {
			return result, fmt.Errorf("duplicate coverage profile %s", report.Profile)
		}
		seen[report.Profile] = true
		result.Evidence = append(result.Evidence, functionrules.Compare(report, report).Expected)
		for _, function := range report.Functions {
			result.FunctionCounts[function.Name] = map[string]int{}
			for _, cell := range function.Cells {
				item := ArgumentCell{Profile: report.Profile, Function: function.Name, ID: cell.ID, Input: cell.Input, Expression: cell.Expression, Analysis: cell.Analysis, Execution: cell.Execution, AnalysisCode: cell.AnalysisCode, ExecutionCode: cell.ExecutionCode}
				got, inferErr := chgen.InferExpressionType("CREATE TABLE t (c "+cell.Input+") ENGINE=Memory", "t", cell.Expression)
				if inferErr != nil {
					item.Diagnostic = inferErr.Error()
				} else {
					item.ChgenType = got.String()
				}
				switch {
				case inferErr != nil && cell.AnalysisCode != 0:
					item.Status = "both_refuse"
				case inferErr != nil && cell.ExecutionCode != 0:
					item.Status = "chgen_refuses_execution_refuses"
				case inferErr != nil:
					item.Status = "chgen_refuses_server_accepts"
				case cell.AnalysisCode != 0:
					item.Status = "chgen_accepts_server_refuses"
				case item.ChgenType != cell.Analysis:
					item.Status = "type_mismatch"
				case cell.ExecutionCode != 0:
					item.Status = "execution_refusal"
				case cell.Analysis != cell.Execution:
					item.Status = "analysis_execution_mismatch"
				default:
					item.Status = "supported"
				}
				if report.Profile == variants.Profile {
					for _, difference := range variants.Cells {
						if difference.Function == function.Name && (reflect.DeepEqual(difference.Expected, cell) || reflect.DeepEqual(difference.Actual, cell)) {
							item.BuildDifference = &difference
							break
						}
					}
					if inferErr != nil && item.BuildDifference != nil {
						item.Status = "build_dependent_refusal"
					} else if item.BuildDifference != nil {
						item.Status = "build_dependent_acceptance"
					}
				}
				result.Cells = append(result.Cells, item)
				result.Counts[item.Status]++
				result.FunctionCounts[function.Name][item.Status]++
			}
		}
	}
	result.Total = len(result.Cells)
	return result, nil
}

// Consistent is not a waiver: known build differences are safe only when the
// public resolver refuses those calls. Any accepted wrong type remains a failure.
func (r ArgumentReport) Consistent() bool {
	for _, status := range []string{"type_mismatch", "chgen_accepts_server_refuses", "execution_refusal", "analysis_execution_mismatch", "build_dependent_acceptance"} {
		if r.Counts[status] != 0 {
			return false
		}
	}
	if r.WrapperGrid != nil {
		for _, status := range []string{"MISMATCH", "CHGEN_TYPES_SERVER_REFUSES", "CHGEN_TYPES_SERVER_REFUSES_PAIR"} {
			if r.WrapperGrid.Counts[status] != 0 {
				return false
			}
		}
	}
	return true
}
