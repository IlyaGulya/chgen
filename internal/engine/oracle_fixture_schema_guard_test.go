//go:build fuzzoracle

package engine

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// fixtureTypeDifference compares the parsed DDL types with executed server
// answers. The optional mutation is only for the self-test. Production calls
// pass nil.
func fixtureTypeDifference(schema *Schema, results map[string]chResult, mutation func(string, CHType) CHType) error {
	table, ok := schema.Tables["t"]
	if !ok {
		return fmt.Errorf("the fixture DDL has no table t")
	}
	names := make([]string, 0, len(table.Columns))
	for name := range table.Columns {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return fmt.Errorf("the fixture DDL has no columns")
	}
	for _, name := range names {
		column := table.Columns[name]
		parsed := column.Type
		if mutation != nil {
			parsed = mutation(name, parsed)
		}
		result, ok := results[name]
		if !ok {
			return fmt.Errorf("column %s has no server witness", name)
		}
		if result.err != "" {
			return fmt.Errorf("column %s did not execute: %s", name, firstLine(result.err))
		}
		serverType, err := parseCHTypeName(canonicalDecimalSpelling(result.typeName))
		if err != nil {
			return fmt.Errorf("column %s: parse server type %q: %w", name, result.typeName, err)
		}
		parsedType, err := parseCHTypeName(canonicalDecimalSpelling(parsed.String()))
		if err != nil {
			return fmt.Errorf("column %s: parse DDL type %q: %w", name, parsed.String(), err)
		}
		if !reflect.DeepEqual(parsedType, serverType) {
			return fmt.Errorf("column %s type differs: DDL=%s server=%s", name, parsedType.String(), serverType.String())
		}
	}
	return nil
}

func oracleCurrentFixture(t *testing.T) (*chOracle, *Schema) {
	t.Helper()
	oracle := execWitnessFixtureWithDDL(t, oracleSchemaDDL, oracleSeedRow)
	schema, err := schemaFromDDLErr(t, oracleSchemaDDL)
	if err != nil {
		t.Fatalf("build schema from oracleSchemaDDL: %v", err)
	}
	return oracle, schema
}

func fixtureColumnResults(oracle *chOracle, schema *Schema) map[string]chResult {
	table := schema.Tables["t"]
	names := make([]string, 0, len(table.Columns))
	for name := range table.Columns {
		names = append(names, name)
	}
	sort.Strings(names)
	batch := oracle.typeNames(names)
	results := make(map[string]chResult, len(names))
	for index, name := range names {
		results[name] = batch[index]
	}
	return results
}

// TestOracleFixtureDDLTypesMatchTheServer checks every column read from the
// current fixture DDL. typeNames pairs toTypeName(column) with ignore(column), so
// each server answer also has an execution witness over the seeded row.
func TestOracleFixtureDDLTypesMatchTheServer(t *testing.T) {
	oracle, schema := oracleCurrentFixture(t)
	if err := fixtureTypeDifference(schema, fixtureColumnResults(oracle, schema), nil); err != nil {
		t.Fatal(err)
	}
}

// TestOracleFixtureDDLGuardCatchesTheOldNestedShape proves that the guard can
// fail. It derives the injected fault from the current parsed Array(Tuple)
// value and changes only its root constructor to the old bare Nested shape.
func TestOracleFixtureDDLGuardCatchesTheOldNestedShape(t *testing.T) {
	oracle, schema := oracleCurrentFixture(t)
	results := fixtureColumnResults(oracle, schema)
	faultWasInjected := false
	err := fixtureTypeDifference(schema, results, func(name string, parsed CHType) CHType {
		if name != "nst_a" || parsed.Name != "Array" || len(parsed.Params) != 1 || parsed.Params[0].Name != "Tuple" {
			return parsed
		}
		faultWasInjected = true
		return CHType{Name: "Nested"}
	})
	if !faultWasInjected {
		t.Fatal("the current parser did not produce Array(Tuple) for nst_a; the self-test did not run")
	}
	if err == nil {
		t.Fatal("the guard accepted the old bare Nested parse")
	}
	if got := err.Error(); got == "" || !containsAll(got, "nst_a", "Nested", "Array(Tuple") {
		t.Fatalf("the guard did not name the Nested difference: %v", err)
	}
}

func containsAll(value string, parts ...string) bool {
	for _, part := range parts {
		if !strings.Contains(value, part) {
			return false
		}
	}
	return true
}
