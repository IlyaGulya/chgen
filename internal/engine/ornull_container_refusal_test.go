package engine

import (
	"testing"

	clickhouse "github.com/AfterShip/clickhouse-sql-parser/parser"
)

// The -OrNull combinator puts the aggregate result inside Nullable, thus
// it REFUSES an aggregate whose result cannot go inside Nullable. The
// rule is on the SHAPE of the result, never on the name of the
// aggregate, and this test pins BOTH halves of it. A test that pinned
// only the refusals would let a blanket refusal pass, and a test that
// pinned only the answers would let the old silently wrong type return.
//
// Every expected value below was measured on ClickHouse 25.8.29.51
// against real columns of a real table, never over literals, because the
// server folds constants. The refusals are Code 43.
func TestOrNullRefusesAResultThatCannotBeNullable(t *testing.T) {
	schema, err := schemaFromDDLErr(t, `CREATE TABLE t (
    s   String,
    i32 Int32,
    m   Map(String, Int32)
) ENGINE = MergeTree ORDER BY tuple();`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	cases := []struct {
		expr string
		want string
	}{
		// The result is a container, thus the server refuses.
		{"groupArrayOrNull(i32)", ""},
		{"groupUniqArrayOrNull(s)", ""},
		{"quantilesOrNull(0.5)(i32)", ""},
		{"sumMapOrNull(m)", ""},
		// The result is a scalar, thus Nullable goes on.
		{"sumOrNull(i32)", "Nullable(Int64)"},
		{"maxOrNull(s)", "Nullable(String)"},
		{"avgOrNull(i32)", "Nullable(Float64)"},
		{"uniqOrNull(s)", "Nullable(UInt64)"},
		{"countOrNull(i32)", "Nullable(UInt64)"},
		// The plain form of a refused case still answers, so the
		// refusal belongs to the combinator and not to the aggregate.
		{"groupUniqArray(s)", "Array(String)"},
		{"groupArray(i32)", "Array(Int32)"},
	}
	for _, testCase := range cases {
		got, err := inferOrNullTestType(t, schema, testCase.expr)
		if testCase.want == "" {
			if err == nil {
				t.Errorf("%s = %s, want a refusal", testCase.expr, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s gave error %v, want %s", testCase.expr, err, testCase.want)
			continue
		}
		if got != testCase.want {
			t.Errorf("%s = %s, want %s", testCase.expr, got, testCase.want)
		}
	}
}

// inferOrNullTestType parses "SELECT <expr> FROM t" and gives the
// inferred type as a string, or the error that chgen reports.
func inferOrNullTestType(t *testing.T, schema *Schema, exprSQL string) (string, error) {
	t.Helper()
	statements, err := clickhouse.NewParser("SELECT " + exprSQL + " FROM t").ParseStmts()
	if err != nil {
		t.Fatalf("parse %q: %v", exprSQL, err)
	}
	selectQuery, ok := statements[0].(*clickhouse.SelectQuery)
	if !ok {
		t.Fatalf("parse %q: not a SELECT", exprSQL)
	}
	scope, _, err := resolveScope(selectQuery, schema)
	if err != nil {
		return "", err
	}
	inferred, err := inferExprType(selectQuery.SelectItems[0].Expr, scope)
	if err != nil {
		return "", err
	}
	return inferred.String(), nil
}
