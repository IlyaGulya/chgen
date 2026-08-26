package engine

import (
	"strings"
	"testing"

	clickhouse "github.com/AfterShip/clickhouse-sql-parser/parser"
)

// The tests in this file pin the operators that came to the historic
// fallback of inferBinaryOperationType. That fallback said: a Float operand
// gives Float64, and if not, the type of the left operand wins. It is a
// guess, not a measured rule, and it gave a silently wrong type to every
// operator that came to it.
//
// Every expectation below was measured on ClickHouse 25.8.29.51 against the
// real columns of the oracle table, never over literals alone, because
// ClickHouse folds constants. Each cell has two witnesses: toTypeName,
// which answers from analysis, and SELECT ... LIMIT 1, which executes. The
// two witnesses agreed in every cell.
//
// The full survey, with the operator list and every measured cell, is in
// docs/binary-operator-fallback-survey.md.

// fallbackSurveySchema holds one column of each operand family that the
// survey measured.
func fallbackSurveySchema(t *testing.T) *Schema {
	t.Helper()
	schema, err := schemaFromDDLErr(t, `CREATE TABLE t (
    i8   Int8, i32 Int32, i64 Int64, i128 Int128,
    u8   UInt8, f64 Float64, dec Decimal(18, 4), b Bool,
    s    String, fs FixedString(8),
    d    Date, dt DateTime, dt64 DateTime64(3),
    ns   Nullable(String), lc LowCardinality(String),
    lcn  LowCardinality(Nullable(String)),
    e8   Enum8('a' = 1, 'zz' = 2), e16 Enum16('x' = 1, 'yy' = 2),
    uid  UUID, ip4 IPv4, ip6 IPv6,
    arr_i Array(Int32), arr_s Array(String),
    m    Map(String, Int64), tup Tuple(Int32, String)
) ENGINE = MergeTree ORDER BY tuple();`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	return schema
}

// TestRegexpOperatorResultType pins the measured rule of REGEXP.
//
// REGEXP is the match function. Its base result is UInt8 and no operand
// base type survives, exactly as with the LIKE family. The Nullable and
// LowCardinality wrappers of the operand move into the result.
//
// Before this rule the fallback answered Enum8('a' = 1, 'zz' = 2) for
// "e8 REGEXP 'a'", which is a wrong type for a legal expression: the
// generated Go would read a UInt8 result into an enum field.
func TestRegexpOperatorResultType(t *testing.T) {
	schema := fallbackSurveySchema(t)
	cases := []struct {
		expr string
		want string
	}{
		// The server answers UInt8 for every legal operand, thus no
		// operand base type survives. The Enum rows are the defect
		// that this test exists for.
		{"e8 REGEXP 'a'", "UInt8"},
		{"e16 REGEXP 'a'", "UInt8"},
		{"s REGEXP 'a'", "UInt8"},
		{"fs REGEXP 'a'", "UInt8"},
		// The wrappers move from the operand into the result.
		{"lc REGEXP 'a'", "LowCardinality(UInt8)"},
		{"ns REGEXP 'a'", "Nullable(UInt8)"},
		{"lcn REGEXP 'a'", "LowCardinality(Nullable(UInt8))"},
	}
	for _, testCase := range cases {
		t.Run(testCase.expr, func(t *testing.T) {
			if got := inferCHTypeString(t, schema, testCase.expr); got != testCase.want {
				t.Errorf("inferExprType(%q) = %q, want %q", testCase.expr, got, testCase.want)
			}
		})
	}
}

// TestRegexpOperatorRefusesIllegalOperand pins the refusals of REGEXP.
//
// The server rejects each operand type below with Code: 43
// (ILLEGAL_TYPE_OF_ARGUMENT, "Illegal type <T> of argument of function
// match"), in analysis and at execution alike. The fallback gave each of
// them a type, which is a type for an expression that cannot run.
func TestRegexpOperatorRefusesIllegalOperand(t *testing.T) {
	schema := fallbackSurveySchema(t)
	for _, expr := range []string{
		"b REGEXP 'a'",
		"u8 REGEXP 'a'",
		"i32 REGEXP 'a'",
		"i128 REGEXP 'a'",
		"f64 REGEXP 'a'",
		"dec REGEXP 'a'",
		"d REGEXP 'a'",
		"dt REGEXP 'a'",
		"uid REGEXP 'a'",
		"ip4 REGEXP 'a'",
		"arr_s REGEXP 'a'",
		"m REGEXP 'a'",
		"tup REGEXP 'a'",
	} {
		t.Run(expr, func(t *testing.T) {
			got, err := inferCHTypeStringErr(t, schema, expr)
			if err == nil {
				t.Errorf("inferExprType(%q) = %q, want a refusal; ClickHouse rejects this operand with Code: 43", expr, got)
			}
		})
	}
}

// TestDoubleEqualsIsTheSameOperatorAsEquals pins that "==" takes the
// predicate rule of "=".
//
// ClickHouse accepts both spellings of the equality operator and gives
// them the same result in every measured cell. The predicate rule listed
// "=" and not "==", thus "==" fell to the historic fallback and took the
// left operand type. That gave Enum8('a' = 1, 'zz' = 2) for "e8 == s" and
// Float64 for "f64 == i32".
//
// chgen reports Bool where the server reports UInt8 for this family. That
// is the accepted divergence of docs/decision-bool-vs-uint8.md. The point
// of this test is that the two spellings of one operator give one answer.
func TestDoubleEqualsIsTheSameOperatorAsEquals(t *testing.T) {
	schema := fallbackSurveySchema(t)
	for _, expr := range []string{
		"e8 == s", "e8 == e8", "e16 == s",
		"i32 == i64", "f64 == i32", "dec == dec", "i128 == i128",
		"s == s", "fs == s",
		"d == dt", "dt64 == dt",
		"uid == uid", "ip4 == ip4", "ip6 == ip6",
		"arr_i == arr_i", "tup == tup", "m == m",
		"lc == 'a'", "lc == lc", "ns == s", "lc == ns", "lcn == 'a'",
	} {
		t.Run(expr, func(t *testing.T) {
			doubleEquals := inferCHTypeString(t, schema, expr)
			singleEquals := inferCHTypeString(t, schema, strings.Replace(expr, "==", "=", 1))
			if doubleEquals != singleEquals {
				t.Errorf("inferExprType(%q) = %q, but the same expression with = is %q; == and = are one operator",
					expr, doubleEquals, singleEquals)
			}
		})
	}
}

// TestCastOperatorRefuses pins the refusal of the "::" cast operator.
//
// The result of "x :: T" is the type T, which the RIGHT operand names.
// chgen has no rule for the operator and read the right operand as a
// column, thus the failure said `column "String" is not present in FROM
// tables`. That message names the wrong cause and sends the reader to
// look for a missing column that was never wanted.
//
// The operator is an explicit refusal that names the true cause and points
// to the CAST(x AS T) form, which chgen does infer.
func TestCastOperatorRefuses(t *testing.T) {
	schema := fallbackSurveySchema(t)
	for _, expr := range []string{
		"i32 :: String",
		"e8 :: String",
		"f64 :: Int32",
	} {
		t.Run(expr, func(t *testing.T) {
			got, err := inferCHTypeStringErr(t, schema, expr)
			if err == nil {
				t.Fatalf("inferExprType(%q) = %q, want a refusal", expr, got)
			}
			if !strings.Contains(err.Error(), "CAST") {
				t.Errorf("inferExprType(%q) error = %q, want a message that points to the CAST form", expr, err)
			}
		})
	}
}

// TestLambdaArrowOutsideHigherOrderCallRefuses pins the refusal of a bare
// lambda.
//
// A lambda has no result type of its own: only the call that receives it
// has one. The higher-order array functions take the lambda before the
// operand inference sees it, thus a lambda inside arrayMap never comes
// here. A lambda outside such a call came to the fallback and took the
// type of its own PARAMETER, which is a type for an expression that the
// server cannot run at all.
func TestLambdaArrowOutsideHigherOrderCallRefuses(t *testing.T) {
	schema := fallbackSurveySchema(t)
	for _, expr := range []string{
		"i32 -> i32 + 1",
		"s -> s",
	} {
		t.Run(expr, func(t *testing.T) {
			got, err := inferCHTypeStringErr(t, schema, expr)
			if err == nil {
				t.Errorf("inferExprType(%q) = %q, want a refusal; a lambda has no result type of its own", expr, got)
			}
		})
	}
}

// TestUnknownBinaryOperatorRefuses pins the new default.
//
// The historic fallback gave a guessed type to EVERY operator that came to
// it, thus a new operator in a later parser version would silently take a
// wrong type. The default is now a refusal, so that such an operator stops
// the build and a person sees it.
//
// The parser makes only the operators that it knows, thus the node is built
// directly here. This is the one case that no SQL string can reach.
func TestUnknownBinaryOperatorRefuses(t *testing.T) {
	schema := fallbackSurveySchema(t)
	statements, err := clickhouse.NewParser("SELECT i32, i64 FROM t").ParseStmts()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	selectQuery, ok := statements[0].(*clickhouse.SelectQuery)
	if !ok {
		t.Fatalf("parse: not a SELECT")
	}
	scope, _, err := resolveScope(selectQuery, schema)
	if err != nil {
		t.Fatalf("resolveScope: %v", err)
	}
	operation := &clickhouse.BinaryOperation{
		LeftExpr:  selectQuery.SelectItems[0].Expr,
		Operation: "<@>",
		RightExpr: selectQuery.SelectItems[1].Expr,
	}
	inferred, err := inferExprType(operation, scope)
	if err == nil {
		t.Errorf("inferExprType(i32 <@> i64) = %q, want a refusal for an operator with no measured rule", inferred.String())
	}
}
