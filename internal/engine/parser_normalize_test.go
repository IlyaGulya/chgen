package engine

import (
	"strings"
	"testing"

	clickhouse "github.com/AfterShip/clickhouse-sql-parser/parser"
)

// These are the six forms that the former parser fork accepted. The added
// parentheses affect only the parser copy. Each replacement keeps the source
// length and line breaks.
func TestCasePrefixNormalizationMatchesFormerForkForms(t *testing.T) {
	cases := []struct {
		sql  string
		want string
	}{
		{"SELECT CASE -2.5 WHEN 1 THEN 5 ELSE 7 END", "SELECT CASE(-2.5)WHEN 1 THEN 5 ELSE 7 END"},
		{"SELECT CASE -1 WHEN 1 THEN 5 ELSE 7 END", "SELECT CASE(-1)WHEN 1 THEN 5 ELSE 7 END"},
		{"SELECT CASE -x WHEN 1 THEN 5 ELSE 7 END", "SELECT CASE(-x)WHEN 1 THEN 5 ELSE 7 END"},
		{"SELECT CASE +1 WHEN 1 THEN 5 ELSE 7 END", "SELECT CASE(+1)WHEN 1 THEN 5 ELSE 7 END"},
		{"SELECT CASE -toInt32(1) WHEN 1 THEN 5 ELSE 7 END", "SELECT CASE(-toInt32(1))WHEN 1 THEN 5 ELSE 7 END"},
		{"SELECT CASE NOT true WHEN 1 THEN 5 ELSE 7 END", "SELECT CASE(NOT true)WHEN 1 THEN 5 ELSE 7 END"},
	}
	for _, testCase := range cases {
		t.Run(testCase.sql, func(t *testing.T) {
			got, unsafe, _ := normalizeCasePrefixOperands(testCase.sql)
			if got != testCase.want {
				t.Fatalf("normalized SQL = %q, want %q", got, testCase.want)
			}
			if unsafe {
				t.Fatal("safe normalization was marked unsafe")
			}
			if len(got) != len(testCase.sql) || strings.Count(got, "\n") != strings.Count(testCase.sql, "\n") {
				t.Fatal("normalization changed source offsets")
			}
			statements, err := clickhouse.NewParser(got).ParseStmts()
			if err != nil {
				t.Fatalf("parse normalized CASE: %v", err)
			}
			query := statements[0].(*clickhouse.SelectQuery)
			caseExpr := query.SelectItems[0].Expr.(*clickhouse.CaseExpr)
			if int(caseExpr.Pos()) != strings.Index(testCase.sql, "CASE") {
				t.Fatalf("CASE position = %d", caseExpr.Pos())
			}
			if int(caseExpr.Whens[0].Pos()) != strings.Index(testCase.sql, "WHEN") {
				t.Fatalf("WHEN position = %d", caseExpr.Whens[0].Pos())
			}
		})
	}
}

// The expected results were recorded with the former fork before the direct
// upstream dependency replaced it. The result branches use a real column, so
// constant folding cannot hide a type difference.
func TestCasePrefixNormalizationKeepsFormerForkInference(t *testing.T) {
	schema := schemaFromDDL(t, "CREATE TABLE probe (x Int32, b Bool) ENGINE = Memory")
	cases := []struct {
		expr    string
		want    string
		wantErr string
	}{
		{expr: "CASE -2.5 WHEN 1 THEN x ELSE x END", want: "Int32"},
		{expr: "CASE -1 WHEN 1 THEN x ELSE x END", want: "Int32"},
		{expr: "CASE -x WHEN 1 THEN x ELSE x END", want: "Int32"},
		{expr: "CASE +1 WHEN 1 THEN x ELSE x END", wantErr: `unary operator "+"`},
		{expr: "CASE -toInt32(1) WHEN 1 THEN x ELSE x END", want: "Int32"},
		{expr: "CASE NOT b WHEN true THEN x ELSE x END", want: "Int32"},
	}
	for _, testCase := range cases {
		t.Run(testCase.expr, func(t *testing.T) {
			statements, err := parseChgenStatements("SELECT "+testCase.expr+" FROM probe", CommandMany)
			if err != nil {
				if testCase.wantErr != "" && strings.Contains(err.Error(), testCase.wantErr) {
					return
				}
				t.Fatalf("parse CASE: %v", err)
			}
			query := statements[0].(*clickhouse.SelectQuery)
			scope, _, err := resolveScope(query, schema)
			if err != nil {
				t.Fatalf("resolve CASE scope: %v", err)
			}
			got, err := inferExprType(query.SelectItems[0].Expr, scope)
			if testCase.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), testCase.wantErr) {
					t.Fatalf("inference error = %v, want %q", err, testCase.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("infer CASE: %v", err)
			}
			if got.String() != testCase.want {
				t.Fatalf("inferred type = %s, want %s", got, testCase.want)
			}
		})
	}
}

func TestCasePrefixNormalizationKeepsOtherCaseAndQuotedForms(t *testing.T) {
	cases := []string{
		"SELECT CASE",
		"SELECT CASE - 2.5",
		"SELECT CASE + 1",
		"SELECT CASE, other FROM t",
		"SELECT CASE AS c FROM t",
		"SELECT CASE FROM t",
		"SELECT a FROM t WHERE CASE > 0",
		"SELECT 'CASE -1 WHEN 1'",
		"SELECT 1 -- CASE -1 WHEN 1",
	}
	for _, sql := range cases {
		if got, _, _ := normalizeCasePrefixOperands(sql); got != sql {
			t.Errorf("normalized SQL = %q, want unchanged %q", got, sql)
		}
	}
}

func TestCasePrefixNormalizationRefusesOffsetChangingWhitespace(t *testing.T) {
	sql := "SELECT CASE\n-1\nWHEN 1 THEN 5 ELSE 7 END"
	if got, unsafe, _ := normalizeCasePrefixOperands(sql); got != sql || !unsafe {
		t.Fatalf("normalized SQL = %q, want unchanged %q", got, sql)
	}
	if _, err := parseChgenStatements(sql, CommandMany); err == nil ||
		!strings.Contains(err.Error(), "cannot be parsed without changing source locations") {
		t.Fatal("expected an explicit parser refusal when safe normalization is not possible")
	}
}
