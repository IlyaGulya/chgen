package engine

import (
	"strings"
	"testing"
)

// These tests pin the function names that the audit of the regression
// corrected. Every result type below was measured on ClickHouse
// 25.8.29.51 against real table columns.
//
// The three defects had one shape: a type rule named a function that the
// server does not have, thus the rule could only give a type to a call
// that always answers Code: 46 (UNKNOWN_FUNCTION).
//
//	toHex           does not exist; the real name is hex
//	greaterOrEqual  does not exist; the real name is greaterOrEquals
//	lessOrEqual     does not exist; the real name is lessOrEquals
//
// Measured for hex, which keeps the wrappers of its argument:
//
//	hex(s)   -> String
//	hex(ns)  -> Nullable(String)
//	hex(lc)  -> LowCardinality(String)
//
// Measured for the comparisons, which match greater and less exactly:
//
//	greaterOrEquals(i32, i64)  -> UInt8
//	greaterOrEquals(ni32, i64) -> Nullable(UInt8)
//	lessOrEquals(lcn, s)       -> Nullable(UInt8)
func hexTestSchema(t *testing.T) *Schema {
	t.Helper()
	// The legacy ParseSchema front door is gone; schemaFromDDL routes the
	// same DDL through the catalog pipeline.
	return schemaFromDDL(t, `CREATE TABLE probe
(
    i32 Int32,
    i64 Int64,
    ni32 Nullable(Int32),
    s String,
    ns Nullable(String),
    lc LowCardinality(String),
    lcn LowCardinality(Nullable(String))
) ENGINE = MergeTree ORDER BY i32;`)
}

// TestHexHasAMeasuredRule proves that hex now types, and that it carries
// the Nullable and LowCardinality wrappers of its argument. Before the
// fix the rule was spelled toHex, thus every one of these calls was a
// refusal.
func TestHexHasAMeasuredRule(t *testing.T) {
	schema := hexTestSchema(t)
	query := Query{
		Name:    "ReadHex",
		Command: CommandMany,
		SQL:     "SELECT hex(s) AS a, hex(ns) AS b, hex(lc) AS c FROM probe",
	}
	if err := resolveQuery(&query, schema); err != nil {
		t.Fatalf("resolveQuery() error = %v", err)
	}
	want := []string{"string", "*string", "string"}
	if len(query.Results) != len(want) {
		t.Fatalf("got %d results, want %d", len(query.Results), len(want))
	}
	for i, expected := range want {
		if got := query.Results[i].GoType; got != expected {
			t.Errorf("result %d GoType = %q, want %q", i, got, expected)
		}
	}
}

// TestToHexIsRefused proves the removal. ClickHouse has no toHex, thus
// chgen must refuse the call instead of giving it a String type that the
// server would never produce.
func TestToHexIsRefused(t *testing.T) {
	schema := hexTestSchema(t)
	query := Query{
		Name:    "ReadToHex",
		Command: CommandMany,
		SQL:     "SELECT toHex(s) AS a FROM probe",
	}
	err := resolveQuery(&query, schema)
	if err == nil {
		t.Fatalf("resolveQuery() typed toHex(s), but ClickHouse has no toHex function")
	}
	if !strings.Contains(err.Error(), "toHex") {
		t.Errorf("refusal does not name the function: %v", err)
	}
}

// TestComparisonFunctionCallsType proves the corrected spellings type,
// and that they keep a Nullable argument Nullable.
func TestComparisonFunctionCallsType(t *testing.T) {
	schema := hexTestSchema(t)
	query := Query{
		Name:    "ReadCompare",
		Command: CommandMany,
		SQL: "SELECT greaterOrEquals(i32, i64) AS a, lessOrEquals(i32, i64) AS b, " +
			"greaterOrEquals(ni32, i64) AS c, lessOrEquals(ni32, i64) AS d FROM probe",
	}
	if err := resolveQuery(&query, schema); err != nil {
		t.Fatalf("resolveQuery() error = %v", err)
	}
	// The comparison functions are the predicate family: ClickHouse
	// answers plain UInt8, which the Go generator maps to uint8, not
	// bool.
	want := []string{"uint8", "uint8", "*uint8", "*uint8"}
	if len(query.Results) != len(want) {
		t.Fatalf("got %d results, want %d", len(query.Results), len(want))
	}
	for i, expected := range want {
		if got := query.Results[i].GoType; got != expected {
			t.Errorf("result %d GoType = %q, want %q", i, got, expected)
		}
	}
}

// TestMisspelledComparisonNamesAreRefused proves the correction. The
// names without the final s do not exist on the server, thus a rule for
// them could only type a call that always fails.
func TestMisspelledComparisonNamesAreRefused(t *testing.T) {
	for _, name := range []string{"greaterOrEqual", "lessOrEqual"} {
		query := Query{
			Name:    "ReadBad",
			Command: CommandMany,
			SQL:     "SELECT " + name + "(i32, i64) AS a FROM probe",
		}
		if err := resolveQuery(&query, hexTestSchema(t)); err == nil {
			t.Errorf("resolveQuery() typed %s(i32, i64), but ClickHouse has no such function", name)
		}
	}
}
