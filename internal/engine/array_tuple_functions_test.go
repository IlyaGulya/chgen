package engine

import "testing"

// TestArrayTupleFunctions covers the measured Array(Tuple(...)) cells
// from the regression. The Nested and explicit Array columns have the same
// type on the server. Each result was confirmed with ignore() on
// ClickHouse 25.8.29.51.
func TestArrayTupleFunctions(t *testing.T) {
	const ddl = `CREATE TABLE probe (
		nst Nested(a Int32, b String),
		at Array(Tuple(a Int32, b String)),
		ai Array(Int32),
		m Map(String, String)
	) ENGINE = Memory`
	schema, err := schemaFromDDLErr(t, ddl)
	if err != nil {
		t.Fatalf("parse probe schema: %v", err)
	}

	for _, testCase := range []struct {
		expression string
		want       string
	}{
		{"reverse(nst)", "Array(Tuple(a Int32, b String))"},
		{"reverse(at)", "Array(Tuple(a Int32, b String))"},
		{"tupleElement(nst, 2)", "Array(String)"},
		{"tupleElement(at, 2)", "Array(String)"},
	} {
		inferred, inferErr := inferTestExprType(t, schema, testCase.expression)
		if inferErr != nil {
			t.Errorf("%s: want %s, got refusal %v", testCase.expression, testCase.want, inferErr)
			continue
		}
		if inferred != testCase.want {
			t.Errorf("%s: want %s, got %s", testCase.expression, testCase.want, inferred)
		}
	}

	for _, expression := range []string{
		"reverse(m)",
		"tupleElement(ai, 1)",
		"tupleElement(m, 1)",
	} {
		if inferred, inferErr := inferTestExprType(t, schema, expression); inferErr == nil {
			t.Errorf("%s: want a refusal, got %s", expression, inferred)
		}
	}
}
