package engine

import (
	"strings"
	"testing"

	clickhouse "github.com/AfterShip/clickhouse-sql-parser/parser"
)

func TestWindowValidationRefusesInvalidSpecs(t *testing.T) {
	schema := schemaFromDDL(t, `CREATE TABLE probe (
		i32 Int32,
		grp UInt8,
		s String,
		dec Decimal(18, 4),
			dt64 DateTime64(3),
			lci LowCardinality(Int32)
	) ENGINE = Memory`)
	cases := []struct {
		expression string
		message    string
	}{
		{"sum(i32) OVER (PARTITION BY missing ORDER BY i32)", "missing"},
		{"sum(i32) OVER (PARTITION BY grp ORDER BY missing)", "missing"},
		{"sum(i32) OVER missing", "window missing"},
		{"sum(i32) OVER (ORDER BY i32 ROWS BETWEEN UNBOUNDED FOLLOWING AND CURRENT ROW)", "UNBOUNDED FOLLOWING"},
		{"sum(i32) OVER (ORDER BY i32 ROWS BETWEEN CURRENT ROW AND UNBOUNDED PRECEDING)", "UNBOUNDED PRECEDING"},
		{"sum(i32) OVER (ORDER BY i32 ROWS BETWEEN 2 FOLLOWING AND 1 FOLLOWING)", "frame start"},
		{"sum(i32) OVER (ORDER BY i32 ROWS 1 FOLLOWING)", "frame start"},
		{"sum(i32) OVER (ORDER BY i32 ROWS 2147483647 PRECEDING)", "offset"},
		{"sum(i32) OVER (RANGE BETWEEN 1 PRECEDING AND CURRENT ROW)", "exactly one ORDER BY"},
		{"sum(i32) OVER (ORDER BY grp, i32 RANGE BETWEEN 1 PRECEDING AND CURRENT ROW)", "exactly one ORDER BY"},
		{"sum(i32) OVER (ORDER BY s RANGE BETWEEN 1 PRECEDING AND CURRENT ROW)", "ORDER BY type String"},
		{"sum(i32) OVER (ORDER BY dec RANGE BETWEEN 1 PRECEDING AND CURRENT ROW)", "ORDER BY type Decimal"},
		{"sum(i32) OVER (ORDER BY dt64 RANGE BETWEEN 1 PRECEDING AND CURRENT ROW)", "ORDER BY type DateTime64"},
		{"sum(i32) OVER (ORDER BY lci RANGE BETWEEN 1 PRECEDING AND CURRENT ROW)", "LowCardinality"},
	}
	for _, testCase := range cases {
		t.Run(testCase.expression, func(t *testing.T) {
			_, err := inferTestExprType(t, schema, testCase.expression)
			if err == nil {
				t.Fatal("inference accepted an invalid window specification")
			}
			if !strings.Contains(err.Error(), testCase.message) {
				t.Fatalf("wrong refusal: %v; want text %q", err, testCase.message)
			}
		})
	}
}

func TestWindowValidationKeepsValidSpecs(t *testing.T) {
	schema := schemaFromDDL(t, `CREATE TABLE probe (
		i32 Int32,
		grp UInt8,
		d Date,
		ni32 Nullable(Int32)
	) ENGINE = Memory`)
	for _, expression := range []string{
		"sum(i32) OVER ()",
		"sum(i32) OVER (PARTITION BY grp ORDER BY i32 ROWS BETWEEN 1 PRECEDING AND CURRENT ROW)",
		"sum(i32) OVER (ORDER BY i32 RANGE BETWEEN 1 PRECEDING AND CURRENT ROW)",
		"sum(i32) OVER (ORDER BY d RANGE BETWEEN 1 PRECEDING AND CURRENT ROW)",
		"sum(i32) OVER (ORDER BY ni32 RANGE BETWEEN 1 PRECEDING AND CURRENT ROW)",
		"rank() OVER (ORDER BY i32 ROWS BETWEEN 1 PRECEDING AND CURRENT ROW)",
		"lagInFrame(i32) OVER (ORDER BY i32 ROWS BETWEEN CURRENT ROW AND CURRENT ROW)",
	} {
		if _, err := inferTestExprType(t, schema, expression); err != nil {
			t.Errorf("inference refused valid window specification %s: %v", expression, err)
		}
	}
}

func TestWindowValidationResolvesNamedWindows(t *testing.T) {
	schema := schemaFromDDL(t, "CREATE TABLE probe (i32 Int32, grp UInt8) ENGINE = Memory")
	for _, query := range []string{
		"SELECT sum(i32) OVER w AS x FROM probe WINDOW w AS (PARTITION BY grp ORDER BY i32)",
		"SELECT sum(i32) OVER w2 AS x FROM probe WINDOW w1 AS (PARTITION BY grp), w2 AS (w1 ORDER BY i32)",
		"SELECT sum(i32) OVER w2 AS x FROM probe WINDOW w1 AS (ORDER BY i32), w2 AS (w1 ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW)",
		"SELECT sum(i32) OVER W AS x FROM probe WINDOW W AS (PARTITION BY grp), w AS (ORDER BY i32)",
		"SELECT sum(i32) OVER w2 AS x FROM probe WINDOW W AS (PARTITION BY grp), w AS (ORDER BY i32), w2 AS (W ORDER BY i32)",
	} {
		if _, err := inferWindowQueryType(t, schema, query); err != nil {
			t.Errorf("inference refused valid named window: %v", err)
		}
	}
	if _, err := inferWindowQueryType(t, schema,
		"SELECT sum(i32) OVER w AS x FROM probe WINDOW w AS (missing ORDER BY i32)"); err == nil {
		t.Fatal("inference accepted a named window with an unknown parent")
	}
	for _, query := range []string{
		"SELECT sum(i32) OVER w AS x FROM probe WINDOW w AS (ORDER BY i32), w AS (PARTITION BY grp)",
		"SELECT sum(i32) OVER w1 AS x FROM probe WINDOW w1 AS (w2 ORDER BY i32), w2 AS (w1)",
		"SELECT sum(i32) OVER w2 AS x FROM probe WINDOW w1 AS (ORDER BY i32), w2 AS (w1 PARTITION BY grp)",
		"SELECT sum(i32) OVER w2 AS x FROM probe WINDOW w1 AS (ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW), w2 AS (w1 ORDER BY i32)",
		"SELECT sum(i32) OVER W AS x FROM probe WINDOW w AS (ORDER BY i32)",
	} {
		if _, err := inferWindowQueryType(t, schema, query); err == nil {
			t.Errorf("inference accepted an invalid named window graph: %s", query)
		}
	}
}

func TestWindowValidationAppliesFunctionFramePolicies(t *testing.T) {
	schema := schemaFromDDL(t, "CREATE TABLE probe (i32 Int32) ENGINE = Memory")
	cases := []struct {
		expression string
		message    string
	}{
		{"lag(i32) OVER (ORDER BY i32 ROWS BETWEEN CURRENT ROW AND CURRENT ROW)", "does not allow an explicit frame"},
		{"lead(i32) OVER (ORDER BY i32 ROWS BETWEEN CURRENT ROW AND CURRENT ROW)", "does not allow an explicit frame"},
		{"percentRank() OVER (ORDER BY i32 ROWS BETWEEN CURRENT ROW AND CURRENT ROW)", "does not allow an explicit frame"},
		{"ntile(2) OVER ()", "needs ORDER BY"},
		{"ntile(2) OVER (ORDER BY i32 ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW)", "full partition frame"},
	}
	for _, testCase := range cases {
		window := parseWindowFunctionForTest(t, testCase.expression)
		err := validateWindowFunction(window, queryScope{tables: []scopedTable{{table: schema.Tables["probe"], alias: "probe"}}})
		if err == nil || !strings.Contains(err.Error(), testCase.message) {
			t.Errorf("validation of %s = %v, want text %q", testCase.expression, err, testCase.message)
		}
	}
}

func TestWindowAliasRosterUsesExactPlacementAndResult(t *testing.T) {
	schema := schemaFromDDL(t, "CREATE TABLE probe (i32 Int32) ENGINE = Memory")
	cases := map[string]string{
		"denseRank() OVER (ORDER BY i32)":      "UInt64",
		"denseRank(i32) OVER (ORDER BY i32)":   "UInt64",
		"percentRank() OVER (ORDER BY i32)":    "Float64",
		"percentRank(i32) OVER (ORDER BY i32)": "Float64",
		"percent_rank() OVER (ORDER BY i32)":   "Float64",
	}
	for expression, expected := range cases {
		actual, err := inferTestExprType(t, schema, expression)
		if err != nil {
			t.Errorf("infer %s: %v", expression, err)
		} else if actual != expected {
			t.Errorf("infer %s = %s, want %s", expression, actual, expected)
		}
	}
	for _, expression := range []string{"denseRank()", "percentRank()", "percent_rank()"} {
		if _, err := inferTestExprType(t, schema, expression); err == nil {
			t.Errorf("inference accepted bare window alias %s", expression)
		}
	}
}

func TestWindowValueRosterUsesMeasuredSignatures(t *testing.T) {
	schema := schemaFromDDL(t, "CREATE TABLE probe (i32 Int32, u32 UInt32, f32 Float32, ni32 Nullable(Int32), tup Tuple(Int32, String), arr Array(Int32)) ENGINE = Memory")
	cases := map[string]string{
		"lag(i32) OVER (ORDER BY i32)":                     "Int32",
		"lead(ni32, 2) OVER (ORDER BY i32)":                "Nullable(Int32)",
		"nth_value(i32, 2) OVER (ORDER BY i32)":            "Int32",
		"ntile(2) OVER (ORDER BY i32)":                     "UInt64",
		"lag(tup) OVER (ORDER BY i32)":                     "Tuple(Int32, String)",
		"nth_value(arr, 2) OVER (ORDER BY i32)":            "Array(Int32)",
		"firstValueRespectNulls(ni32) OVER (ORDER BY i32)": "Nullable(Int32)",
		"last_value_respect_nulls(ni32)":                   "Nullable(Int32)",
		"lag(i32, u32) OVER (ORDER BY i32)":                "Int32",
		"nth_value(i32, i32) OVER (ORDER BY i32)":          "Int32",
	}
	for expression, expected := range cases {
		actual, err := inferTestExprType(t, schema, expression)
		if err != nil || actual != expected {
			t.Errorf("infer %s = %s, %v; want %s", expression, actual, err, expected)
		}
	}
	for _, expression := range []string{
		"lag(i32, -1) OVER (ORDER BY i32)",
		"nth_value(i32, 0) OVER (ORDER BY i32)",
		"ntile(0) OVER (ORDER BY i32)",
		"lag(i32, f32) OVER (ORDER BY i32)",
		"nth_value(i32, f32) OVER (ORDER BY i32)",
		"lag(i32, ni32) OVER (ORDER BY i32)",
	} {
		if _, err := inferTestExprType(t, schema, expression); err == nil {
			t.Errorf("inference accepted invalid window call %s", expression)
		}
	}
}

func inferWindowQueryType(t *testing.T, schema *Schema, sql string) (CHType, error) {
	t.Helper()
	statements, err := clickhouse.NewParser(sql).ParseStmts()
	if err != nil {
		return CHType{}, err
	}
	query := statements[0].(*clickhouse.SelectQuery)
	scope, _, err := resolveScope(query, schema)
	if err != nil {
		return CHType{}, err
	}
	return inferExprType(query.SelectItems[0].Expr, scope)
}

func parseWindowFunctionForTest(t *testing.T, expression string) *clickhouse.WindowFunctionExpr {
	t.Helper()
	statements, err := clickhouse.NewParser("SELECT " + expression + " FROM probe").ParseStmts()
	if err != nil {
		t.Fatal(err)
	}
	query := statements[0].(*clickhouse.SelectQuery)
	parsed := unwrapColumnExpr(query.SelectItems[0].Expr)
	return parsed.(*clickhouse.WindowFunctionExpr)
}
