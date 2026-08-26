//go:build fuzzoracle

package engine

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"
)

type classSweepAxis struct {
	name         string
	valueArgs    []string
	fallback     string
	requiredFact functionFact
}

type classSweepCell struct {
	function string
	class    functionWrapperClass
	axis     string
	argument string
	outcome  string
	domain   *argumentDomain
	facts    functionFact
}

func TestFunctionClassMembersExplainOutcomeSplits(t *testing.T) {
	if *gridURL == "" {
		t.Skip("-chgen-grid-url is not set; start a disposable ClickHouse to run the class sweep")
	}
	oracle := &chOracle{url: *gridURL, client: &http.Client{Timeout: 120 * time.Second}}
	const database = "chgen_probe_class_sweep"
	if _, err := oracle.adminExec("DROP DATABASE IF EXISTS " + database); err != nil {
		t.Fatalf("drop stale run database: %v", err)
	}
	if _, err := oracle.adminExec("CREATE DATABASE " + database); err != nil {
		t.Fatalf("create run database: %v", err)
	}
	oracle.database = database
	t.Cleanup(func() { _, _ = oracle.adminExec("DROP DATABASE IF EXISTS " + database) })

	ddl := strings.Replace(oracleSchemaDDL, "CREATE TABLE t (", "CREATE TABLE g (", 1)
	seed := strings.Replace(oracleSeedRow, "INSERT INTO t ", "INSERT INTO g ", 1)
	if _, err := oracle.exec(ddl); err != nil {
		t.Fatalf("create class-sweep fixture: %v", err)
	}
	if _, err := oracle.exec(seed); err != nil {
		t.Fatalf("seed class-sweep fixture: %v", err)
	}

	axes := []classSweepAxis{
		{name: "dynamic-first", valueArgs: []string{"dyn"}, fallback: "i32", requiredFact: functionFactDynamicForcesNullable},
		{name: "incomparable-pair", valueArgs: []string{"dec", "s"}, fallback: "i32", requiredFact: functionFactComparesArgPair},
	}
	var cells []classSweepCell
	for _, axis := range axes {
		expressions, metadata := buildClassSweepExpressions(axis)
		answers := gridServerTypes(oracle, expressions)
		for index, answer := range answers {
			cell := metadata[index]
			if answer.err != "" {
				cell.outcome = "REFUSAL"
			} else {
				cell.outcome = classSweepWrapperOutcome(answer.typeName)
			}
			cells = append(cells, cell)
		}
	}

	for _, problem := range auditClassSweepCells(cells, axes) {
		t.Error(problem)
	}
}

func buildClassSweepExpressions(axis classSweepAxis) ([]string, []classSweepCell) {
	names := make([]string, 0, len(functionRegistry))
	for name := range functionRegistry {
		names = append(names, name)
	}
	sort.Strings(names)
	var expressions []string
	var cells []classSweepCell
	for _, name := range names {
		spec := functionRegistry[name]
		if spec.gen == nil || spec.gen.template != "" || spec.gen.minArity == 0 {
			continue
		}
		expr, ok := renderClassSweepExpression(*spec.gen, axis.valueArgs, axis.fallback)
		if !ok {
			continue
		}
		expressions = append(expressions, expr)
		cells = append(cells, classSweepCell{
			function: name,
			class:    spec.class,
			axis:     axis.name,
			argument: strings.Join(axis.valueArgs, ", "),
			domain:   spec.domain,
			facts:    spec.facts,
		})
	}
	return expressions, cells
}

func renderClassSweepExpression(spec genSpec, values []string, fallback string) (string, bool) {
	args := make([]string, spec.minArity)
	valueIndex := 0
	for index := range args {
		sortIndex := index
		if sortIndex >= len(spec.argSorts) {
			sortIndex = len(spec.argSorts) - 1
		}
		if sortIndex < 0 {
			return "", false
		}
		sort := spec.argSorts[sortIndex]
		switch sort {
		case argSortValue:
			if valueIndex < len(values) {
				args[index] = values[valueIndex]
			} else {
				args[index] = fallback
			}
			valueIndex++
		case argSortPredicate:
			args[index] = "i32 = 3"
		case argSortConstString:
			args[index] = "'day'"
		case argSortConstInt:
			args[index] = "1"
		case argSortLambda:
			args[index] = "x -> x"
		case argSortTypeName:
			args[index] = "'Int32'"
		default:
			return "", false
		}
	}
	if valueIndex < len(values) {
		return "", false
	}
	expr := spec.spelling + "(" + strings.Join(args, ", ") + ")"
	if spec.place == placementWindow {
		expr += " OVER ()"
	}
	return expr, true
}

func classSweepWrapperOutcome(typeName string) string {
	normalized := normalizeTypeName(typeName)
	for _, wrapper := range []string{"Nullable(", "LowCardinality(", "SimpleAggregateFunction("} {
		if strings.HasPrefix(normalized, wrapper) {
			return strings.TrimSuffix(wrapper, "(")
		}
	}
	return "BARE"
}

func auditClassSweepCells(cells []classSweepCell, axes []classSweepAxis) []string {
	required := make(map[string]functionFact, len(axes))
	for _, axis := range axes {
		required[axis.name] = axis.requiredFact
	}
	var problems []string
	for leftIndex, left := range cells {
		for _, right := range cells[leftIndex+1:] {
			if left.class != right.class || left.axis != right.axis || left.outcome == right.outcome {
				continue
			}
			fact := required[left.axis]
			if left.facts&fact != right.facts&fact {
				continue
			}
			if left.outcome == "REFUSAL" || right.outcome == "REFUSAL" {
				if !functionHasMeasuredFact(left.function, fact) && !functionHasMeasuredFact(right.function, fact) {
					continue
				}
			}
			problems = append(problems, fmt.Sprintf(
				"wrapper class %d members %s and %s disagree for argument %s on axis %s: %s versus %s; add a measured spec fact or correct the class",
				left.class, left.function, right.function, left.argument, left.axis, left.outcome, right.outcome,
			))
		}
	}
	sort.Strings(problems)
	return problems
}

func functionHasMeasuredFact(name string, fact functionFact) bool {
	for _, item := range functionFactEvidenceCatalog {
		if item.fact != fact {
			continue
		}
		for _, measured := range item.functions {
			if strings.EqualFold(name, measured) {
				return true
			}
		}
	}
	return false
}

func TestFunctionClassSweepMarkRemovalSelfTest(t *testing.T) {
	axes := []classSweepAxis{
		{name: "dynamic-first", requiredFact: functionFactDynamicForcesNullable},
		{name: "incomparable-pair", requiredFact: functionFactComparesArgPair},
	}
	cells := []classSweepCell{
		{function: "equals", class: wrapperTransparent, axis: "dynamic-first", argument: "dyn", outcome: "Nullable", facts: functionFactDynamicForcesNullable},
		{function: "tostring", class: wrapperTransparent, axis: "dynamic-first", argument: "dyn", outcome: "BARE"},
		{function: "equals", class: wrapperTransparent, axis: "incomparable-pair", argument: "dec, s", outcome: "REFUSAL", facts: functionFactComparesArgPair},
		{function: "concat", class: wrapperTransparent, axis: "incomparable-pair", argument: "dec, s", outcome: "BARE"},
	}
	if problems := auditClassSweepCells(cells, axes); len(problems) != 0 {
		t.Fatalf("marked self-test cells disagree: %v", problems)
	}
	cells[0].facts = 0
	problems := auditClassSweepCells(cells, axes)
	joined := strings.Join(problems, "\n")
	if !strings.Contains(joined, "equals and tostring") || !strings.Contains(joined, "argument dyn") {
		t.Errorf("removing the Dynamic mark did not name the separating members and argument:\n%s", joined)
	}
	cells[0].facts = functionFactDynamicForcesNullable
	cells[2].facts = 0
	problems = auditClassSweepCells(cells, axes)
	joined = strings.Join(problems, "\n")
	if !strings.Contains(joined, "concat and equals") && !strings.Contains(joined, "equals and concat") {
		t.Errorf("removing the pair mark did not name the separating members:\n%s", joined)
	}
}
