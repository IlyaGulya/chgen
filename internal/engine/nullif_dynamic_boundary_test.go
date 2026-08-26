package engine

import (
	"strings"
	"testing"
)

const nullIfDynamicBoundarySchema = `
CREATE TABLE probe (
    i32 Int32,
    i64 Int64,
    s String,
    d Date,
    nd Nullable(Date),
    b Bool,
    variant Variant(Int32, String),
    dyn Dynamic,
    js JSON
) ENGINE = MergeTree ORDER BY tuple()
`

func nullIfDynamicTestSchema(t *testing.T) *Schema {
	t.Helper()
	schema, err := schemaFromDDLErr(t, nullIfDynamicBoundarySchema)
	if err != nil {
		t.Fatalf("parse the Dynamic nullIf schema: %v", err)
	}
	return schema
}

// TestNullIfRefusesAFirstArgumentThatCannotBecomeNullable checks the exact
// result boundary. nullIf can return NULL, so its first argument must be a
// type that ClickHouse permits inside Nullable. Dynamic and Variant cannot
// be inside Nullable and must refuse before a parent call gives them a type.
func TestNullIfRefusesAFirstArgumentThatCannotBecomeNullable(t *testing.T) {
	schema := nullIfDynamicTestSchema(t)
	for _, expression := range []string{
		"nullIf(dyn, nd)",
		"nullIf(dyn, i32)",
		"nullIf(dyn, dyn)",
		"nullIf(dyn, 3)",
		"nullIf(variant, variant)",
		"ifNull(nullIf(dyn, nd), dyn)",
	} {
		if inferred, err := inferTestExprType(t, schema, expression); err == nil {
			t.Errorf("%s: want a refusal, got %s", expression, inferred)
		} else if !strings.Contains(err.Error(), "cannot return NULL") {
			t.Errorf("%s: want the Nullable result refusal, got %v", expression, err)
		}
	}
}

// TestNullIfKeepsDynamicInTheComparisonPosition checks the accepted side.
// The real dyn fixture column holds Int32, so an Int32 first argument can
// compare with it and the result is Nullable(Int32). JSON can become
// Nullable and keeps the exact JSON pair.
func TestNullIfKeepsDynamicInTheComparisonPosition(t *testing.T) {
	schema := nullIfDynamicTestSchema(t)
	for _, testCase := range []struct {
		expression string
		want       string
	}{
		{"nullIf(i32, dyn)", "Nullable(Int32)"},
		{"nullIf(i32, i64)", "Nullable(Int32)"},
		{"nullIf(js, js)", "Nullable(JSON)"},
	} {
		inferred, err := inferTestExprType(t, schema, testCase.expression)
		if err != nil {
			t.Errorf("%s: want %s, got refusal %v", testCase.expression, testCase.want, err)
		} else if inferred != testCase.want {
			t.Errorf("%s: want %s, got %s", testCase.expression, testCase.want, inferred)
		}
	}
}
