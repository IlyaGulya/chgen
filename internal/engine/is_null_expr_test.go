package engine

import (
	"fmt"
	"strings"
	"testing"
)

const isNullExprTestDDL = `CREATE TABLE probe (
    id UInt64,
    plain Int32,
    nullable Nullable(Int32),
    low_cardinality LowCardinality(String),
    low_cardinality_nullable LowCardinality(Nullable(String)),
    kind LowCardinality(String),
    available_at Nullable(DateTime64(3, 'UTC')),
    version UInt64,
    bad Enum8('a' = 1)
) ENGINE = MergeTree ORDER BY tuple()`

func TestIsNullExprMeasuredType(t *testing.T) {
	schema := schemaFromDDL(t, isNullExprTestDDL)
	accepted := []string{
		"plain IS NULL",
		"nullable IS NULL",
		"low_cardinality IS NULL",
		"low_cardinality_nullable IS NULL",
		"plain IS NOT NULL",
		"nullable IS NOT NULL",
		"(nullable + 1) IS NULL",
		"(nullable + 1) IS NOT NULL",
		"1 IS NOT NULL",
		"NULL IS NULL",
		"NULL IS NOT NULL",
	}
	for _, expression := range accepted {
		got, err := inferTestExprType(t, schema, expression)
		if err != nil {
			t.Errorf("inference refused %s: %v", expression, err)
			continue
		}
		if got != "UInt8" {
			t.Errorf("type of %s = %s, want UInt8", expression, got)
		}
	}
	results, err := inferQueryResults(isNullExprTestDDL, "SELECT nullable AS alias_value, alias_value IS NULL AS result FROM probe")
	if err != nil {
		t.Errorf("alias predicate: %v", err)
	} else if got := results[1].CHType.String(); got != "UInt8" {
		t.Errorf("alias predicate type = %s, want UInt8", got)
	}

	for _, testCase := range []struct{ expression, want string }{
		{expression: "trim(bad) IS NULL", want: "function"},
		{expression: "trim(bad) IS NOT NULL", want: "function"},
		{expression: "unknown_function(plain) IS NULL", want: "function"},
		{expression: "unknown_function(plain) IS NOT NULL", want: "function"},
		{expression: "(? + missing) IS NULL", want: "positional placeholder"},
		{expression: "(missing + ?) IS NULL", want: "column"},
		{expression: "(? + unknown_function(plain)) IS NULL", want: "positional placeholder"},
		{expression: "(unknown_function(plain) + ?) IS NULL", want: "function"},
	} {
		if _, err := inferTestExprType(t, schema, testCase.expression); err == nil {
			t.Errorf("inference accepted %s", testCase.expression)
		} else if !strings.Contains(err.Error(), testCase.want) {
			t.Errorf("refusal for %s = %v, want the inner %s cause", testCase.expression, err, testCase.want)
		}
	}
}

func TestIsNullExprKeepsBarePlaceholder(t *testing.T) {
	schema := schemaFromDDL(t, isNullExprTestDDL)
	for _, expression := range []string{"? IS NULL", "? IS NOT NULL", "(?) IS NULL", "((?)) IS NOT NULL"} {
		got, err := inferTestExprType(t, schema, expression)
		if err != nil {
			t.Errorf("inference refused %s: %v", expression, err)
		} else if got != "UInt8" {
			t.Errorf("type of %s = %s, want UInt8", expression, got)
		}
	}
	queries, err := parseQueriesWithSchema(t, `-- name: IsNullParameter :one
-- param: X *int32
SELECT chgen.arg('X') IS NULL AS missing`, schema)
	if err != nil {
		t.Fatal(err)
	}
	if len(queries) != 1 || len(queries[0].Results) != 1 || queries[0].Results[0].CHType.String() != "UInt8" || queries[0].Results[0].GoType != "uint8" {
		t.Fatalf("generated parameter predicate results = %+v, want UInt8/uint8", queries)
	}
	generated, err := Generate("querygen", queries)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"X *int32", "Missing uint8", "chgenNullableParam(arg.X)"} {
		if !strings.Contains(string(generated), want) {
			t.Errorf("generated parameter predicate does not contain %q", want)
		}
	}
}

func TestIsNullExprRefusesInvalidNamedParameterComposites(t *testing.T) {
	schema := schemaFromDDL(t, isNullExprTestDDL)
	for index, expression := range []string{
		"(chgen.arg('X') + missing) IS NULL",
		"(missing + chgen.arg('X')) IS NULL",
		"(chgen.arg('X') + unknown_function(plain)) IS NULL",
		"(unknown_function(plain) + chgen.arg('X')) IS NULL",
	} {
		source := fmt.Sprintf("-- name: BadIsNull%d :one\n-- param: X int32\nSELECT %s AS result FROM probe", index, expression)
		if _, err := parseQueriesWithSchema(t, source, schema); err == nil {
			t.Errorf("generated query accepted %s", expression)
		}
	}
}

func TestIsNullExprNestedWhereShape(t *testing.T) {
	schema := schemaFromDDL(t, isNullExprTestDDL)
	source := `-- name: ListSelectedRows :many
-- param: Cutoff time.Time
SELECT id, available_at
FROM
(
    SELECT
        id,
        argMax(kind, version) AS kind,
        argMax(available_at, version) AS available_at
    FROM probe
    GROUP BY id
) AS current_state
WHERE kind IN ('a', 'b')
  AND (available_at IS NULL OR available_at <= chgen.arg('Cutoff'))
ORDER BY id ASC
LIMIT chgen.arg('Limit')`
	queries, err := parseQueriesWithSchema(t, source, schema)
	if err != nil {
		t.Fatal(err)
	}
	if len(queries) != 1 {
		t.Fatalf("queries = %d, want 1", len(queries))
	}
	query := queries[0]
	if len(query.Results) != 2 || query.Results[0].CHType.String() != "UInt64" || query.Results[1].CHType.String() != "Nullable(DateTime64(3, 'UTC'))" {
		t.Errorf("results = %+v, want UInt64 and Nullable(DateTime64(3, 'UTC'))", query.Results)
	}
	generated, err := Generate("querygen", queries)
	if err != nil {
		t.Fatal(err)
	}
	text := string(generated)
	for _, want := range []string{"ListSelectedRows", "Cutoff time.Time", "Limit  uint64", "AvailableAt *time.Time"} {
		if !strings.Contains(text, want) {
			t.Errorf("generated output does not contain %q:\n%s", want, text)
		}
	}
}

func TestIsNullExprContractMutations(t *testing.T) {
	schema := schemaFromDDL(t, isNullExprTestDDL)
	original := measuredIsNullPredicateRule
	check := func() error {
		for _, expression := range []string{"nullable IS NULL", "low_cardinality IS NULL", "nullable IS NOT NULL", "? IS NULL"} {
			got, err := inferTestExprType(t, schema, expression)
			if err != nil {
				return err
			}
			if got != "UInt8" {
				return fmt.Errorf("type of %s = %s, want UInt8", expression, got)
			}
		}
		if _, err := inferTestExprType(t, schema, "trim(bad) IS NULL"); err == nil {
			return fmt.Errorf("invalid inner expression was accepted")
		}
		for _, expression := range []string{"(? + missing) IS NULL", "(missing + ?) IS NULL", "(? + unknown_function(plain)) IS NULL", "(unknown_function(plain) + ?) IS NULL"} {
			if _, err := inferTestExprType(t, schema, expression); err == nil {
				return fmt.Errorf("composite placeholder expression %s was accepted", expression)
			}
		}
		return nil
	}
	if err := check(); err != nil {
		t.Fatalf("production contract: %v", err)
	}
	mutations := map[string]func(*isNullPredicateRule){
		"wrong result":          func(rule *isNullPredicateRule) { rule.resultName = "Bool" },
		"missing inner check":   func(rule *isNullPredicateRule) { rule.validateOperand = false },
		"placeholder rejection": func(rule *isNullPredicateRule) { rule.acceptPlaceholder = false },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			mutated := original
			mutate(&mutated)
			measuredIsNullPredicateRule = mutated
			t.Cleanup(func() { measuredIsNullPredicateRule = original })
			if err := check(); err == nil {
				t.Fatal("contract accepted the rule mutation")
			}
		})
	}
}
