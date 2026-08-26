package engine

import (
	"strings"
	"testing"

	clickhouse "github.com/AfterShip/clickhouse-sql-parser/parser"
)

// These tests pin the rule that an argument refusal propagates through
// the outer operator. Each refusal below was measured on ClickHouse
// 25.8.29.51 (image clickhouse/clickhouse-server:25.8, disposable
// container) with SELECT toTypeName(<expression>) FROM probe on real
// table columns, never on constants: the server folds a constant, so a
// domain measured over a literal reports the wrong answer.
//
// The rule that these tests protect: an explicit refusal is better than
// a silently wrong answer. Before this rule the LIKE, comparison and
// boolean operators had a FIXED result type and never looked at whether
// the operand itself could be typed, thus "empty(e8) OR true" gave Bool
// and "trim(e8) LIKE '%a%'" gave UInt8 while ClickHouse answers
// Code: 43 for both.
func refusalPropagationSchema(t *testing.T) *Schema {
	t.Helper()
	// The legacy ParseSchema front door is gone; schemaFromDDL routes the
	// same DDL through the catalog pipeline.
	return schemaFromDDL(t, `CREATE TABLE t (
    i32 Int32, u8 UInt8, b Bool, s String, d Date, dt DateTime,
    ni32 Nullable(Int32), ns Nullable(String),
    arr_i Array(Int32), lc LowCardinality(String),
    e8 Enum8('a' = 1, 'zz' = 2), uid UUID, tup Tuple(Int32, String)
) ENGINE = MergeTree ORDER BY tuple();`)
}

// inferRefusalPropagationType returns the inferred type, or the error,
// without failing the test. inferCHTypeString cannot serve here, because
// it calls t.Fatalf on a refusal and a refusal is what these tests want.
func inferRefusalPropagationType(t *testing.T, schema *Schema, exprSQL string) (string, error) {
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
		t.Fatalf("resolveScope(%q): %v", exprSQL, err)
	}
	inferred, inferErr := inferExprType(selectQuery.SelectItems[0].Expr, scope)
	if inferErr != nil {
		return "", inferErr
	}
	return inferred.String(), nil
}

// TestOuterOperatorPropagatesArgumentRefusal is the regression test for
// the ticket. Every expression here makes ClickHouse answer Code: 43,
// thus chgen must refuse instead of giving a fixed result type.
func TestOuterOperatorPropagatesArgumentRefusal(t *testing.T) {
	schema := refusalPropagationSchema(t)
	// The inner refusals, for the record. empty and length refuse an
	// Enum8 and an Int32; trim refuses an Enum8; avg refuses a String.
	cases := []struct {
		expr string
		// want names the substring that the refusal must mention, so
		// the message keeps pointing at the real cause.
		want string
	}{
		// Boolean operators, on both operand sides.
		{"empty(e8) OR true", "function empty"},
		{"empty(e8) AND true", "function empty"},
		{"true OR empty(e8)", "function empty"},
		{"true AND empty(e8)", "function empty"},

		// Comparison operators.
		{"empty(e8) = 1", "function empty"},
		{"trim(e8) = 'a'", "function trim"},
		{"trim(e8) != 'a'", "function trim"},
		{"avg(s) > 1", "function avg"},
		{"avg(s) <= 1", "function avg"},
		{"1 < length(i32)", "function length"},

		// The LIKE family. This is the UInt8 case from the ticket.
		{"trim(e8) LIKE '%a%'", "function trim"},
		{"trim(e8) NOT LIKE '%a%'", "function trim"},
		{"trim(e8) ILIKE '%a%'", "function trim"},
		{"trim(e8) NOT ILIKE '%a%'", "function trim"},
		{"s LIKE trim(e8)", "function trim"},

		// IN and NOT IN.
		{"trim(e8) IN ('a')", "function trim"},
		{"trim(e8) NOT IN ('a')", "function trim"},

		// The CASE condition. It never reaches the result type, but an
		// illegal condition still makes the whole CASE fail.
		{"CASE WHEN empty(e8) THEN 1 ELSE 2 END", "function empty"},
		{"CASE WHEN i32 = 1 THEN 1 WHEN empty(e8) THEN 2 ELSE 3 END", "function empty"},
		// The operand form: the operand and the match are part of the
		// condition, not of the result.
		{"CASE trim(e8) WHEN 'a' THEN 1 ELSE 2 END", "function trim"},
		{"CASE s WHEN trim(e8) THEN 1 ELSE 2 END", "function trim"},

		// A refusal nested two operators deep still reaches the top.
		{"(trim(e8) LIKE '%a%') OR b", "function trim"},
		{"NOT (empty(e8) OR true)", "function empty"},
	}
	for _, testCase := range cases {
		got, err := inferRefusalPropagationType(t, schema, testCase.expr)
		if err == nil {
			t.Errorf("type of %q = %s, want a refusal; ClickHouse answers Code: 43 for this expression", testCase.expr, got)
			continue
		}
		if !strings.Contains(err.Error(), testCase.want) {
			t.Errorf("refusal for %q = %v, want a message that names %q", testCase.expr, err, testCase.want)
		}
	}
}

// TestOuterOperatorKeepsPlaceholderOperand pins the other half of the
// rule. A bare positional placeholder has no result type by
// construction: that is "not known yet", not "impossible". The fixed
// result must survive it, or every predicate that binds a parameter
// stops working.
func TestOuterOperatorKeepsPlaceholderOperand(t *testing.T) {
	schema := refusalPropagationSchema(t)
	cases := []struct{ expr, want string }{
		{"i32 = ?", "UInt8"},
		{"? = i32", "UInt8"},
		{"i32 != ?", "UInt8"},
		{"i32 > ?", "UInt8"},
		{"s LIKE ?", "UInt8"},
		{"s NOT LIKE ?", "UInt8"},
		{"s ILIKE ?", "UInt8"},
		{"b OR ?", "Bool"},
		{"? OR b", "Bool"},
		{"b AND ?", "Bool"},
		{"i32 IN (?)", "UInt8"},
		{"i32 NOT IN (?)", "UInt8"},
		{"countIf(i32 = ?)", "UInt64"},
		{"CASE WHEN i32 = ? THEN 1 ELSE 2 END", "UInt8"},
		{"CASE WHEN ? THEN 1 ELSE 2 END", "UInt8"},
		// A Nullable operand beside a placeholder still contributes its
		// wrapper, thus the placeholder does not erase the model.
		{"ni32 = ?", "Nullable(UInt8)"},
		{"ns LIKE ?", "Nullable(UInt8)"},
	}
	for _, testCase := range cases {
		got, err := inferRefusalPropagationType(t, schema, testCase.expr)
		if err != nil {
			t.Errorf("type of %q = error %v, want %s; a placeholder must not block a fixed result", testCase.expr, err, testCase.want)
			continue
		}
		if got != testCase.want {
			t.Errorf("type of %q = %s, want %s", testCase.expr, got, testCase.want)
		}
	}
}
