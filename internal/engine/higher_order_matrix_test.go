package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

var higherOrderMatrixPath = moduleRootPath("testdata", "clickhouse-higher-order-matrix.json")

type higherOrderMatrixCell struct {
	ID          string `json:"id"`
	Function    string `json:"function"`
	Form        string `json:"form"`
	Result      string `json:"result"`
	Domain      string `json:"domain"`
	Length      string `json:"length"`
	Accumulator string `json:"accumulator"`
	Scalar      string `json:"scalar"`
	SQL         string `json:"sql"`
	Analysis    string `json:"analysis"`
	Execution   string `json:"execution"`
	Type        string `json:"type,omitempty"`
}

type higherOrderMatrixArtifact struct {
	Version        int                     `json:"version"`
	Cells          []higherOrderMatrixCell `json:"cells"`
	ExecProbeLinks map[string]string       `json:"exec_probe_links"`
}

func higherOrderResultName(result higherOrderArrayResult) string {
	switch result {
	case hofArrayOfBody:
		return "array_of_body"
	case hofFirstArray:
		return "first_array"
	case hofSumOfBody:
		return "sum_of_body"
	case hofBody:
		return "body"
	case hofCount:
		return "count"
	case hofPredicate:
		return "predicate"
	case hofFirstElement:
		return "first_element"
	case hofNullableFirstElement:
		return "nullable_first_element"
	case hofFloat64:
		return "float64"
	case hofArrayOfSum:
		return "array_of_sum"
	case hofArrayOfFirstArray:
		return "array_of_first_array"
	case hofAccumulator:
		return "accumulator"
	default:
		return "unknown"
	}
}

func higherOrderDomainName(domain higherOrderLambdaBodyDomain) string {
	switch domain {
	case hofBodyDomainAny:
		return "any"
	case hofBodyDomainPredicate:
		return "predicate"
	case hofBodyDomainSummable:
		return "summable"
	case hofBodyDomainNumericReduction:
		return "numeric_reduction"
	default:
		return "unknown"
	}
}

func higherOrderRepresentative(name string, rule higherOrderLambdaSignature) (string, string) {
	if rule.accumulator != nil {
		return rule.spelling + "((acc, x) -> acc, a, toInt64(0))", "Int64"
	}
	body := "x -> x"
	if rule.bodyDomain == hofBodyDomainPredicate {
		body = "x -> x > 0"
	}
	sql := rule.spelling + "(" + body + ", a)"
	if rule.scalarArgument != nil {
		sql = rule.spelling + "(" + body + ", i32, a)"
	}
	switch rule.result {
	case hofArrayOfBody, hofFirstArray:
		return sql, "Array(Int32)"
	case hofSumOfBody:
		return sql, "Int64"
	case hofBody, hofFirstElement:
		return sql, "Int32"
	case hofCount:
		return sql, "UInt32"
	case hofPredicate:
		return sql, "UInt8"
	case hofNullableFirstElement:
		return sql, "Nullable(Int32)"
	case hofFloat64:
		return sql, "Float64"
	case hofArrayOfSum:
		return sql, "Array(Int64)"
	case hofArrayOfFirstArray:
		return sql, "Array(Array(Int32))"
	default:
		return sql, ""
	}
}

func buildHigherOrderMatrix() higherOrderMatrixArtifact {
	names := make([]string, 0, len(higherOrderArrayFunctions))
	for name := range higherOrderArrayFunctions {
		names = append(names, name)
	}
	sort.Strings(names)
	artifact := higherOrderMatrixArtifact{Version: 1, ExecProbeLinks: make(map[string]string)}
	for _, name := range names {
		rule := higherOrderArrayFunctions[name]
		sql, resultType := higherOrderRepresentative(name, rule)
		base := higherOrderMatrixCell{
			ID: "function/" + name + "/lambda", Function: rule.spelling, Form: "lambda",
			Result: higherOrderResultName(rule.result), Domain: higherOrderDomainName(rule.bodyDomain),
			Length: "equal_at_execution", Accumulator: "none", SQL: sql,
			Scalar:   "none",
			Analysis: "accept", Execution: "accept", Type: resultType,
		}
		if rule.accumulator != nil {
			base.Accumulator = "exact_body_type"
		}
		if rule.scalarArgument != nil {
			base.Scalar = rule.scalarArgument.role + "_before_arrays"
		}
		artifact.Cells = append(artifact.Cells, base)
		if rule.allowNoLambda {
			artifact.Cells = append(artifact.Cells, higherOrderMatrixCell{
				ID: "function/" + name + "/no_lambda", Function: rule.spelling, Form: "no_lambda",
				Result: base.Result, Domain: base.Domain, Length: "one_array", Accumulator: "none",
				SQL: rule.spelling + "(a)", Analysis: "accept", Execution: "accept", Type: resultType, Scalar: base.Scalar,
			})
			if rule.scalarArgument != nil {
				artifact.Cells[len(artifact.Cells)-1].SQL = rule.spelling + "(i32, a)"
			}
		}
		if rule.scalarArgument != nil {
			artifact.Cells = append(artifact.Cells, higherOrderMatrixCell{
				ID: "function/" + name + "/bool_limit", Function: rule.spelling, Form: "no_lambda",
				Result: base.Result, Domain: base.Domain, Length: "one_array", Accumulator: "none",
				SQL: rule.spelling + "(flag, a)", Analysis: "accept", Execution: "accept", Type: resultType,
				Scalar: "bool_limit_before_arrays",
			})
		}
		artifact.Cells = append(artifact.Cells, higherOrderMatrixCell{
			ID: "function/" + name + "/wrong_case", Function: rule.spelling, Form: "wrong_case",
			Result: base.Result, Domain: base.Domain, Length: "not_reached", Accumulator: base.Accumulator,
			SQL: strings.ToUpper(rule.spelling) + strings.TrimPrefix(sql, rule.spelling), Analysis: "refuse", Execution: "refuse",
			Scalar: base.Scalar,
		})
		if rule.accumulator == nil {
			linkedSQL := rule.spelling + "((x, y) -> x, a)"
			if rule.scalarArgument != nil {
				linkedSQL = rule.spelling + "((x, y) -> x, i32, a)"
			}
			artifact.Cells = append(artifact.Cells, higherOrderMatrixCell{
				ID: "function/" + name + "/linked_arity", Function: rule.spelling, Form: "linked_arity",
				Result: base.Result, Domain: base.Domain, Length: "not_reached", Accumulator: "none",
				SQL: linkedSQL, Analysis: "refuse", Execution: "refuse", Scalar: base.Scalar,
			})
		}
		if rule.bodyDomain != hofBodyDomainAny {
			artifact.Cells = append(artifact.Cells, higherOrderMatrixCell{
				ID: "function/" + name + "/body_domain", Function: rule.spelling, Form: "lambda",
				Result: base.Result, Domain: "illegal_complement", Length: "not_reached", Accumulator: base.Accumulator,
				SQL: rule.spelling + "(x -> x, n)", Analysis: "refuse", Execution: "refuse",
				Scalar: base.Scalar,
			})
		}
	}
	artifact.Cells = append(artifact.Cells, higherOrderMatrixCell{
		ID: "policy/unequal_lengths", Function: "arrayMap", Form: "lambda", Result: "array_of_body",
		Domain: "any", Length: "unequal_at_execution", Accumulator: "none",
		SQL: "arrayMap((x, y) -> x + y, a, short)", Analysis: "accept", Execution: "refuse", Type: "Array(Int64)",
		Scalar: "none",
	})
	for _, name := range []string{"arraypartialsort", "arraypartialreversesort"} {
		rule := higherOrderArrayFunctions[name]
		artifact.Cells = append(artifact.Cells, higherOrderMatrixCell{
			ID: "policy/" + name + "/unequal_lengths", Function: rule.spelling, Form: "lambda",
			Result: "first_array", Domain: "any", Length: "unequal_at_execution", Accumulator: "none",
			SQL: rule.spelling + "((x, y) -> x + y, i32, a, short)", Analysis: "accept", Execution: "refuse",
			Type: "Array(Int32)", Scalar: "limit_before_arrays",
		})
	}
	links := map[string]string{
		"function/arraymap/lambda":                   "probe_hof_multi_array",
		"function/arrayfill/lambda":                  "probe_hof_fill",
		"function/arraysum/lambda":                   "probe_hof_sum_int64",
		"function/arraymin/lambda":                   "probe_hof_min",
		"function/arrayfirstindex/lambda":            "probe_hof_first_index",
		"function/arrayexists/lambda":                "probe_hof_predicate",
		"function/arrayfirst/lambda":                 "probe_hof_first",
		"function/arrayfirstornull/lambda":           "probe_hof_first_or_null",
		"function/arrayavg/lambda":                   "probe_hof_avg",
		"function/arraycumsum/lambda":                "probe_hof_cumsum",
		"function/arraysplit/lambda":                 "probe_hof_split",
		"function/arrayfold/lambda":                  "probe_hof_fold_bool",
		"function/arraypartialreversesort/no_lambda": "probe_hof_partial_sort",
	}
	for cellID, probeID := range links {
		artifact.ExecProbeLinks[cellID] = probeID
	}
	return artifact
}

func validateHigherOrderMatrix(artifact higherOrderMatrixArtifact) error {
	if artifact.Version != 1 || len(artifact.Cells) == 0 {
		return fmt.Errorf("higher-order matrix has no current cells")
	}
	seen := make(map[string]bool, len(artifact.Cells))
	functions := make(map[string]bool)
	forms := make(map[string]bool)
	results := make(map[string]bool)
	domains := make(map[string]bool)
	lengths := make(map[string]bool)
	accumulators := make(map[string]bool)
	scalars := make(map[string]bool)
	for _, cell := range artifact.Cells {
		if cell.ID == "" || seen[cell.ID] {
			return fmt.Errorf("higher-order matrix has an empty or duplicate ID %q", cell.ID)
		}
		seen[cell.ID] = true
		if cell.SQL == "" || (cell.Analysis != "accept" && cell.Analysis != "refuse") ||
			(cell.Execution != "accept" && cell.Execution != "refuse") {
			return fmt.Errorf("higher-order matrix cell %s is not executable", cell.ID)
		}
		functions[cell.Function] = true
		forms[cell.Form] = true
		results[cell.Result] = true
		domains[cell.Domain] = true
		lengths[cell.Length] = true
		accumulators[cell.Accumulator] = true
		scalars[cell.Scalar] = true
	}
	for name, axis := range map[string]map[string]bool{
		"function": functions, "form": forms, "result": results, "domain": domains,
		"length": lengths, "accumulator": accumulators,
		"scalar": scalars,
	} {
		if len(axis) < 2 {
			return fmt.Errorf("higher-order matrix has no %s complement", name)
		}
	}
	for _, rule := range higherOrderArrayFunctions {
		if !functions[rule.spelling] {
			return fmt.Errorf("higher-order matrix misses function %s", rule.spelling)
		}
	}
	for _, id := range []string{
		"policy/arraypartialsort/unequal_lengths",
		"policy/arraypartialreversesort/unequal_lengths",
	} {
		if !seen[id] {
			return fmt.Errorf("higher-order matrix misses scalar length policy %s", id)
		}
	}
	for cellID, probeID := range artifact.ExecProbeLinks {
		if !seen[cellID] || strings.TrimSpace(probeID) == "" {
			return fmt.Errorf("higher-order matrix has an invalid exec probe link for %s", cellID)
		}
	}
	for _, result := range []string{"array_of_body", "first_array", "sum_of_body", "body", "count", "predicate",
		"first_element", "nullable_first_element", "float64", "array_of_sum", "array_of_first_array", "accumulator"} {
		linked := false
		for cellID := range artifact.ExecProbeLinks {
			for _, cell := range artifact.Cells {
				if cell.ID == cellID && cell.Result == result {
					linked = true
				}
			}
		}
		if !linked {
			return fmt.Errorf("higher-order matrix result %s has no generated value probe", result)
		}
	}
	return nil
}

func TestHigherOrderMatrixAgainstChgen(t *testing.T) {
	artifact := buildHigherOrderMatrix()
	if err := validateHigherOrderMatrix(artifact); err != nil {
		t.Fatal(err)
	}
	for _, cell := range artifact.Cells {
		got, err := InferExpressionType(higherOrderSignatureDDL, "t", cell.SQL)
		if cell.Analysis == "refuse" {
			if err == nil {
				t.Errorf("cell %s accepted as %s", cell.ID, got.String())
			} else if strings.HasSuffix(cell.ID, "/linked_arity") && cell.Scalar == "limit_before_arrays" &&
				!strings.Contains(err.Error(), "has 2 lambda parameters and 1 array arguments") {
				t.Errorf("cell %s error = %v, want the linked lambda and Array arity refusal", cell.ID, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("cell %s error = %v", cell.ID, err)
		} else if got.String() != cell.Type {
			t.Errorf("cell %s type = %s, want %s", cell.ID, got.String(), cell.Type)
		}
	}
}

func TestHigherOrderMatrixMutationsRefuse(t *testing.T) {
	base := buildHigherOrderMatrix()
	mutations := map[string]func(*higherOrderMatrixArtifact){
		"duplicate":   func(value *higherOrderMatrixArtifact) { value.Cells[1].ID = value.Cells[0].ID },
		"expectation": func(value *higherOrderMatrixArtifact) { value.Cells[0].Analysis = "maybe" },
		"sql":         func(value *higherOrderMatrixArtifact) { value.Cells[0].SQL = "" },
		"function closure": func(value *higherOrderMatrixArtifact) {
			for index := range value.Cells {
				if value.Cells[index].Function == "arrayAll" {
					value.Cells[index].Function = "arrayMap"
				}
			}
		},
		"probe link": func(value *higherOrderMatrixArtifact) { delete(value.ExecProbeLinks, "function/arraymap/lambda") },
		"scalar length policy": func(value *higherOrderMatrixArtifact) {
			for index, cell := range value.Cells {
				if cell.ID == "policy/arraypartialsort/unequal_lengths" {
					value.Cells = append(value.Cells[:index], value.Cells[index+1:]...)
					return
				}
			}
		},
	}
	for name, mutate := range mutations {
		copyArtifact := base
		copyArtifact.Cells = append([]higherOrderMatrixCell(nil), base.Cells...)
		copyArtifact.ExecProbeLinks = make(map[string]string, len(base.ExecProbeLinks))
		for cellID, probeID := range base.ExecProbeLinks {
			copyArtifact.ExecProbeLinks[cellID] = probeID
		}
		mutate(&copyArtifact)
		if err := validateHigherOrderMatrix(copyArtifact); err == nil {
			t.Errorf("mutation %s passed validation", name)
		}
	}
}

func TestPinnedHigherOrderMatrixIsCurrent(t *testing.T) {
	want := buildHigherOrderMatrix()
	data, err := json.MarshalIndent(want, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	path := filepath.Clean(higherOrderMatrixPath)
	if os.Getenv("CHGEN_UPDATE_HIGHER_ORDER_MATRIX") == "1" {
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var gotArtifact higherOrderMatrixArtifact
	if err := json.Unmarshal(got, &gotArtifact); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotArtifact, want) {
		t.Fatal("higher-order matrix artifact is stale")
	}
}
