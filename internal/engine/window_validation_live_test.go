//go:build fuzzoracle

package engine

import (
	"testing"
)

func TestWindowValidationAgainstClickHouse(t *testing.T) {
	schema, err := schemaFromDDLErr(t, oracleSchemaDDL)
	if err != nil {
		t.Fatal(err)
	}
	oracle := execWitnessFixture(t)
	cases := []struct {
		expression string
		legal      bool
	}{
		{"sum(i32) OVER (ORDER BY i32 ROWS BETWEEN 1 PRECEDING AND CURRENT ROW)", true},
		{"sum(i32) OVER (ORDER BY i32 RANGE BETWEEN 1 PRECEDING AND CURRENT ROW)", true},
		{"sum(i32) OVER (ORDER BY ni32 RANGE BETWEEN 1 PRECEDING AND CURRENT ROW)", true},
		{"rank() OVER (ORDER BY i32 ROWS BETWEEN 1 PRECEDING AND CURRENT ROW)", true},
		{"lagInFrame(i32, 0) OVER (ORDER BY i32 ROWS BETWEEN CURRENT ROW AND CURRENT ROW)", true},
		{"lagInFrame(i32, 9223372036854775807) OVER (ORDER BY i32)", true},
		{"sum(i32) OVER (PARTITION BY missing ORDER BY i32)", false},
		{"sum(i32) OVER (PARTITION BY u8 ORDER BY missing)", false},
		{"sum(i32) OVER missing", false},
		{"sum(i32) OVER (ORDER BY i32 ROWS BETWEEN UNBOUNDED FOLLOWING AND CURRENT ROW)", false},
		{"sum(i32) OVER (ORDER BY i32 ROWS BETWEEN 2 FOLLOWING AND 1 FOLLOWING)", false},
		{"sum(i32) OVER (RANGE BETWEEN 1 PRECEDING AND CURRENT ROW)", false},
		{"sum(i32) OVER (ORDER BY s RANGE BETWEEN 1 PRECEDING AND CURRENT ROW)", false},
		{"sum(i32) OVER (ORDER BY CAST(i32 AS LowCardinality(Int32)) RANGE BETWEEN 1 PRECEDING AND CURRENT ROW)", false},
		{"sum(i32) OVER (ORDER BY i32 GROUPS BETWEEN 1 PRECEDING AND CURRENT ROW)", false},
		{"lagInFrame(i32, 1.5) OVER (ORDER BY i32)", false},
		{"leadInFrame(i32, -1) OVER (ORDER BY i32)", false},
		{"lagInFrame(i32, 9223372036854775808) OVER (ORDER BY i32)", false},
	}
	for _, testCase := range cases {
		t.Run(testCase.expression, func(t *testing.T) {
			_, analysisErr := oracle.exec("SELECT toTypeName(" + testCase.expression + ") FROM t")
			execution := oracle.typeNames([]string{testCase.expression})[0]
			_, chgenErr := chgenInferType(schema, testCase.expression)
			if testCase.legal {
				if analysisErr != nil || execution.err != "" || chgenErr != nil {
					t.Fatalf("legal window differs: analysis=%v execution=%s chgen=%v", analysisErr, execution.err, chgenErr)
				}
				return
			}
			// toTypeName can answer a type for a window that real execution
			// refuses. Keep that disagreement as evidence. The execution
			// witness and chgen must both refuse the illegal cell.
			if execution.err == "" || chgenErr == nil {
				t.Fatalf("illegal window differs: analysis=%v execution=%s chgen=%v", analysisErr, execution.err, chgenErr)
			}
		})
	}
}

func TestWindowFunctionFramePolicyAgainstClickHouse(t *testing.T) {
	schema, err := schemaFromDDLErr(t, oracleSchemaDDL)
	if err != nil {
		t.Fatal(err)
	}
	oracle := execWitnessFixture(t)
	scope := queryScope{tables: []scopedTable{{table: schema.Tables["t"], alias: "t"}}}
	cases := []struct {
		expression string
		legal      bool
	}{
		{"lag(i32) OVER (ORDER BY i32)", true},
		{"lag(i32) OVER (ORDER BY i32 ROWS BETWEEN CURRENT ROW AND CURRENT ROW)", false},
		{"lead(i32) OVER (ORDER BY i32)", true},
		{"lead(i32) OVER (ORDER BY i32 ROWS BETWEEN CURRENT ROW AND CURRENT ROW)", false},
		{"percentRank() OVER (ORDER BY i32)", true},
		{"percentRank() OVER (ORDER BY i32 ROWS BETWEEN CURRENT ROW AND CURRENT ROW)", false},
		{"ntile(2) OVER (ORDER BY i32)", true},
		{"ntile(2) OVER ()", false},
		{"ntile(2) OVER (ORDER BY i32 ROWS BETWEEN UNBOUNDED PRECEDING AND UNBOUNDED FOLLOWING)", true},
		{"ntile(2) OVER (ORDER BY i32 RANGE BETWEEN UNBOUNDED PRECEDING AND UNBOUNDED FOLLOWING)", true},
		{"ntile(2) OVER (ORDER BY i32 ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW)", false},
		{"ntile(2) OVER (ORDER BY i32 RANGE BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW)", false},
	}
	for _, testCase := range cases {
		t.Run(testCase.expression, func(t *testing.T) {
			_, analysisErr := oracle.exec("SELECT toTypeName(" + testCase.expression + ") FROM t")
			execution := oracle.typeNames([]string{testCase.expression})[0]
			validationErr := validateWindowFunction(parseWindowFunctionForTest(t, testCase.expression), scope)
			if testCase.legal {
				if analysisErr != nil || execution.err != "" || validationErr != nil {
					t.Fatalf("legal policy differs: analysis=%v execution=%s validation=%v", analysisErr, execution.err, validationErr)
				}
				return
			}
			if execution.err == "" || validationErr == nil {
				t.Fatalf("illegal policy differs: analysis=%v execution=%s validation=%v", analysisErr, execution.err, validationErr)
			}
		})
	}
}

func TestWindowAliasRosterAgainstClickHouse(t *testing.T) {
	schema, err := schemaFromDDLErr(t, oracleSchemaDDL)
	if err != nil {
		t.Fatal(err)
	}
	oracle := execWitnessFixture(t)
	cases := map[string]string{
		"denseRank() OVER (ORDER BY i32)":      "UInt64",
		"denseRank(i32) OVER (ORDER BY i32)":   "UInt64",
		"percentRank() OVER (ORDER BY i32)":    "Float64",
		"percentRank(i32) OVER (ORDER BY i32)": "Float64",
		"percent_rank() OVER (ORDER BY i32)":   "Float64",
	}
	for expression, expected := range cases {
		t.Run(expression, func(t *testing.T) {
			analysis, analysisErr := oracle.exec("SELECT toTypeName(" + expression + ") FROM t")
			execution := oracle.typeNames([]string{expression})[0]
			inferred, chgenErr := chgenInferType(schema, expression)
			if analysisErr != nil || execution.err != "" || chgenErr != nil {
				t.Fatalf("window alias differs: analysis=%v execution=%s chgen=%v", analysisErr, execution.err, chgenErr)
			}
			if analysis != expected || execution.typeName != expected || inferred != expected {
				t.Fatalf("window alias types: analysis=%s execution=%s chgen=%s, want %s", analysis, execution.typeName, inferred, expected)
			}
		})
	}
	for _, expression := range []string{"denseRank()", "percentRank()", "percent_rank()"} {
		t.Run("bare-"+expression, func(t *testing.T) {
			_, analysisErr := oracle.exec("SELECT toTypeName(" + expression + ") FROM t")
			execution := oracle.typeNames([]string{expression})[0]
			_, chgenErr := chgenInferType(schema, expression)
			if analysisErr == nil || execution.err == "" || chgenErr == nil {
				t.Fatalf("bare window alias differs: analysis=%v execution=%s chgen=%v", analysisErr, execution.err, chgenErr)
			}
		})
	}
}

func TestWindowValueRosterAgainstClickHouse(t *testing.T) {
	schema, err := schemaFromDDLErr(t, oracleSchemaDDL)
	if err != nil {
		t.Fatal(err)
	}
	oracle := execWitnessFixture(t)
	cases := map[string]string{
		"lag(i32) OVER (ORDER BY i32)":      "Int32",
		"lead(ni32, 2) OVER (ORDER BY i32)": "Nullable(Int32)",
		"nth_value(arr_i, 2) OVER (ORDER BY i32 ROWS BETWEEN UNBOUNDED PRECEDING AND UNBOUNDED FOLLOWING)": "Array(Int32)",
		"ntile(2) OVER (ORDER BY i32)":                     "UInt64",
		"firstValueRespectNulls(ni32) OVER (ORDER BY i32)": "Nullable(Int32)",
		"last_value_respect_nulls(ni32)":                   "Nullable(Int32)",
		"lag(i32, u32) OVER (ORDER BY i32)":                "Int32",
		"nth_value(i32, u32) OVER (ORDER BY i32 ROWS BETWEEN UNBOUNDED PRECEDING AND UNBOUNDED FOLLOWING)": "Int32",
	}
	for expression, expected := range cases {
		t.Run(expression, func(t *testing.T) {
			analysis, analysisErr := oracle.exec("SELECT toTypeName(" + expression + ") FROM t")
			execution := oracle.typeNames([]string{expression})[0]
			inferred, chgenErr := chgenInferType(schema, expression)
			if analysisErr != nil || execution.err != "" || chgenErr != nil || analysis != expected || execution.typeName != expected || inferred != expected {
				t.Fatalf("window value roster: analysis=%s/%v execution=%s/%s chgen=%s/%v want=%s", analysis, analysisErr, execution.typeName, execution.err, inferred, chgenErr, expected)
			}
		})
	}
	for _, expression := range []string{
		"lag(i32, f32) OVER (ORDER BY i32)",
		"nth_value(i32, f64) OVER (ORDER BY i32)",
		"lag(i32, ni32) OVER (ORDER BY i32)",
	} {
		t.Run("invalid-"+expression, func(t *testing.T) {
			_, analysisErr := oracle.exec("SELECT toTypeName(" + expression + ") FROM t")
			execution := oracle.typeNames([]string{expression})[0]
			_, chgenErr := chgenInferType(schema, expression)
			if analysisErr == nil || execution.err == "" || chgenErr == nil {
				t.Fatalf("float offset differs: analysis=%v execution=%s chgen=%v", analysisErr, execution.err, chgenErr)
			}
		})
	}
}

func TestNamedWindowValidationAgainstClickHouse(t *testing.T) {
	schema, err := schemaFromDDLErr(t, oracleSchemaDDL)
	if err != nil {
		t.Fatal(err)
	}
	oracle := execWitnessFixture(t)
	type namedWindowCase struct {
		expression string
		clause     string
	}
	valid := []namedWindowCase{
		{"sum(i32) OVER w", "WINDOW w AS (PARTITION BY u8 ORDER BY i32)"},
		{"sum(i32) OVER w2", "WINDOW w1 AS (PARTITION BY u8), w2 AS (w1 ORDER BY i32)"},
		{"sum(i32) OVER w2", "WINDOW w1 AS (ORDER BY i32), w2 AS (w1 ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW)"},
		{"sum(i32) OVER W", "WINDOW W AS (PARTITION BY u8), w AS (ORDER BY i32)"},
		{"sum(i32) OVER w2", "WINDOW W AS (PARTITION BY u8), w AS (ORDER BY i32), w2 AS (W ORDER BY i32)"},
	}
	for _, testCase := range valid {
		t.Run(testCase.clause, func(t *testing.T) {
			if _, err := oracle.exec("SELECT toTypeName(" + testCase.expression + ") FROM t " + testCase.clause); err != nil {
				t.Fatalf("analysis refused a valid named window: %v", err)
			}
			if _, err := oracle.exec("SELECT " + testCase.expression + " FROM t " + testCase.clause + " FORMAT Null"); err != nil {
				t.Fatalf("execution refused a valid named window: %v", err)
			}
			query := "SELECT " + testCase.expression + " AS x FROM t " + testCase.clause
			if _, err := inferWindowQueryType(t, schema, query); err != nil {
				t.Fatalf("chgen refused a valid named window: %v", err)
			}
		})
	}

	invalid := []namedWindowCase{
		{"sum(i32) OVER missing", ""},
		{"sum(i32) OVER w2", "WINDOW w1 AS (ORDER BY i32), w2 AS (w1 PARTITION BY u8)"},
		{"sum(i32) OVER w2", "WINDOW w1 AS (ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW), w2 AS (w1 ORDER BY i32)"},
		{"sum(i32) OVER W", "WINDOW w AS (ORDER BY i32)"},
	}
	for _, testCase := range invalid {
		t.Run(testCase.expression+testCase.clause, func(t *testing.T) {
			_, analysisErr := oracle.exec("SELECT toTypeName(" + testCase.expression + ") FROM t " + testCase.clause)
			_, executionErr := oracle.exec("SELECT " + testCase.expression + " FROM t " + testCase.clause + " FORMAT Null")
			query := "SELECT " + testCase.expression + " AS x FROM t " + testCase.clause
			_, chgenErr := inferWindowQueryType(t, schema, query)
			if executionErr == nil || chgenErr == nil {
				t.Fatalf("invalid named window differs: analysis=%v execution=%v chgen=%v", analysisErr, executionErr, chgenErr)
			}
		})
	}
}
