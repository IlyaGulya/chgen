package engine

import (
	"testing"

	clickhouse "github.com/AfterShip/clickhouse-sql-parser/parser"
)

// This file pins the LowCardinality grid of the logic operators.
//
// The logic operators AND, OR and XOR REMOVE a LowCardinality wrapper,
// and they remove it always: at every arity, and whether one operand or
// every operand carries the wrapper. NOT KEEPS the wrapper, and so does
// every predicate operator around them. chgen kept the wrapper for AND
// and OR, which was a silently wrong type.
//
// Each expected type was measured on ClickHouse 25.8.29.51 (image
// clickhouse/clickhouse-server:25.8, disposable container) with
// toTypeName over REAL table columns, never over literals, because the
// server folds constants. The same expressions were also executed, thus
// the answers are not analysis-only artifacts. The transport rule
// itself lives in logicOperatorTransport in wrapper_transport.go, and
// that comment holds the full raw table.
//
// BOTH SIDES of the grid are pinned on purpose. A test that pinned only
// the cells that lose the wrapper would let a later blanket change
// remove the wrapper everywhere and still pass.
//
// The base type is UInt8 for every cell here: empty() is a predicate,
// and the predicate family always answers UInt8, even where an operand
// carries a Bool base. There is no chgen/server base difference left to
// track, so want and server agree in every row below; the server value
// is kept as a second column because it is what was actually measured.

// logicOperatorTestSchema gives a schema with TWO LowCardinality columns
// of each kind, so that a case can put a LowCardinality wrapper on both
// operands at once. The shared wrapperTestSchema has one of each, which
// cannot express the "two LowCardinality operands" half of the grid.
func logicOperatorTestSchema(t *testing.T) *Schema {
	t.Helper()
	schema, err := schemaFromDDLErr(t, `CREATE TABLE t (
    s    String,
    u8   UInt8,
    lc   LowCardinality(String),
    lc2  LowCardinality(String),
    lcn  LowCardinality(Nullable(String)),
    lcn2 LowCardinality(Nullable(String))
) ENGINE = MergeTree ORDER BY tuple();`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	return schema
}

// inferLogicOperatorType parses "SELECT <expr> FROM t" and returns the
// inferred type as a string. It is a local copy of the shared helper so
// that this file states its own schema.
func inferLogicOperatorType(t *testing.T, schema *Schema, exprSQL string) string {
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
	inferred, err := inferExprType(selectQuery.SelectItems[0].Expr, scope)
	if err != nil {
		t.Fatalf("inferExprType(%q): %v", exprSQL, err)
	}
	return inferred.String()
}

// TestLogicOperatorLowCardinalityGrid pins the measured grid: the cells
// that LOSE the LowCardinality wrapper and the cells that KEEP it.
func TestLogicOperatorLowCardinalityGrid(t *testing.T) {
	schema := logicOperatorTestSchema(t)
	cases := []struct {
		expr   string
		want   string
		server string
	}{
		// The operand. empty(concat('', lc)) is the way to get a
		// LowCardinality(UInt8) value: the server refuses a
		// LowCardinality(UInt8) COLUMN as a suspicious type.
		{"empty(concat('', lc))", "LowCardinality(UInt8)", "LowCardinality(UInt8)"},
		{"empty(concat('', lcn))", "LowCardinality(Nullable(UInt8))", "LowCardinality(Nullable(UInt8))"},

		// AND removes the wrapper. One LowCardinality operand, in
		// either position, and two LowCardinality operands.
		{"empty(concat('', lc)) AND empty('abc')", "UInt8", "UInt8"},
		{"empty('abc') AND empty(concat('', lc))", "UInt8", "UInt8"},
		{"empty(concat('', lc)) AND empty(concat('', lc2))", "UInt8", "UInt8"},

		// OR removes the wrapper, in the same three shapes. The second
		// of these is the cell that the ticket reported.
		{"empty(concat('', lc)) OR empty('abc')", "UInt8", "UInt8"},
		{"empty('abc') OR empty(concat('', lc))", "UInt8", "UInt8"},
		{"empty(concat('', lc)) OR empty(concat('', lc2))", "UInt8", "UInt8"},

		// Nullable is a SEPARATE axis and it survives the removal, thus
		// the operators remove LowCardinality alone.
		{"empty(concat('', lcn)) AND empty('abc')", "Nullable(UInt8)", "Nullable(UInt8)"},
		{"empty(concat('', lcn)) OR empty(concat('', lcn2))", "Nullable(UInt8)", "Nullable(UInt8)"},

		// A non-LowCardinality operand pair is unchanged by the rule.
		{"empty(s) AND empty('abc')", "UInt8", "UInt8"},

		// NOT KEEPS the wrapper. It is the near neighbour that must not
		// move with AND and OR.
		{"NOT empty(concat('', lc))", "LowCardinality(UInt8)", "LowCardinality(UInt8)"},
		{"NOT NOT empty(concat('', lc))", "LowCardinality(UInt8)", "LowCardinality(UInt8)"},
		{"NOT empty(concat('', lcn))", "LowCardinality(Nullable(UInt8))", "LowCardinality(Nullable(UInt8))"},

		// The predicate operators are the contrast. Each KEEPS the
		// wrapper under the usual "one LowCardinality operand and every
		// other operand constant" rule, exactly where the logic
		// operators remove it.
		{"empty(concat('', lc)) = empty('abc')", "LowCardinality(UInt8)", "LowCardinality(UInt8)"},
		{"empty(concat('', lc)) != empty('abc')", "LowCardinality(UInt8)", "LowCardinality(UInt8)"},
		{"lc = 'a'", "LowCardinality(UInt8)", "LowCardinality(UInt8)"},
		{"lc < 'a'", "LowCardinality(UInt8)", "LowCardinality(UInt8)"},
		{"lc IN ('a')", "LowCardinality(UInt8)", "LowCardinality(UInt8)"},
		{"lc LIKE '%a%'", "LowCardinality(UInt8)", "LowCardinality(UInt8)"},
		{"lc ILIKE '%a%'", "LowCardinality(UInt8)", "LowCardinality(UInt8)"},

		// Two LowCardinality operands break the "all others constant"
		// rule, thus a comparison drops the wrapper there as well. The
		// server agrees, which is why this rule stays for comparisons.
		{"lc = lc2", "UInt8", "UInt8"},
		{"lc = s", "UInt8", "UInt8"},
	}
	for _, testCase := range cases {
		t.Run(testCase.expr, func(t *testing.T) {
			got := inferLogicOperatorType(t, schema, testCase.expr)
			if got != testCase.want {
				t.Errorf(
					"inferExprType(%q) = %s, want %s (ClickHouse 25.8.29.51 answers %s)",
					testCase.expr, got, testCase.want, testCase.server,
				)
			}
		})
	}
}

// TestLogicOperatorFunctionFormInfers pins that chgen now INFERS the
// FUNCTION spelling of and, or and xor. The regression gave the three names a
// functionRegistry entry (inferLogicOperatorFunctionType), so a call of
// the function form no longer refuses; it answers the SAME measured type
// that the infix form of AND and OR already gave, and it gives xor its
// first type rule of any kind, since xor has no infix spelling at all
// (measured: "1 xor 0" is Code: 62, a parse error).
//
// This test replaces TestLogicOperatorFunctionFormStillRefuses, which
// pinned the refusal that was the state before the regression. Each expected
// type was measured on ClickHouse 25.8.29.51 with toTypeName over real
// table columns, never over literals, because the server folds constants.
func TestLogicOperatorFunctionFormInfers(t *testing.T) {
	schema := logicOperatorTestSchema(t)
	cases := []struct {
		expr string
		want string
	}{
		// The LowCardinality wrapper is dropped, exactly as the infix
		// form drops it, whether one operand carries it or both do.
		{"and(empty(concat('', lc)), empty('abc'))", "UInt8"},
		{"or(empty(concat('', lc)), empty('abc'))", "UInt8"},
		{"xor(empty(concat('', lc)), empty('abc'))", "UInt8"},
		{"and(empty(concat('', lc)), empty(concat('', lc2)))", "UInt8"},
		{"or(empty(concat('', lc)), empty(concat('', lc2)))", "UInt8"},
		{"xor(empty(concat('', lc)), empty(concat('', lc2)))", "UInt8"},
		// Nullable is a separate axis and it survives the removal.
		{"xor(empty(concat('', lcn)), empty(concat('', lcn2)))", "Nullable(UInt8)"},
		{"and(empty(concat('', lcn)), empty('abc'))", "Nullable(UInt8)"},
		// A three-argument call of and/or/xor.
		{"and(u8, u8, u8)", "UInt8"},
	}
	for _, testCase := range cases {
		t.Run(testCase.expr, func(t *testing.T) {
			got := inferLogicOperatorType(t, schema, testCase.expr)
			if got != testCase.want {
				t.Errorf("inferExprType(%q) = %s, want %s", testCase.expr, got, testCase.want)
			}
		})
	}
}
