package engine

import (
	"testing"

	clickhouse "github.com/AfterShip/clickhouse-sql-parser/parser"
)

// These tests pin the inference rules for the constructs that chgen
// refused before: parenthesized expressions, unary operators, the
// concat, substring and nullIf functions, and subscript access. Each
// expected type was measured on ClickHouse 25.8.29.51 (image
// clickhouse/clickhouse-server:25.8, disposable container) with
// DESCRIBE (SELECT <expression> FROM t) or SELECT toTypeName(...).

// inferCHTypeError parses "SELECT <expr> FROM t" and returns the
// inference error, or fails the test when inference succeeds.
func inferCHTypeError(t *testing.T, schema *Schema, exprSQL string) error {
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
	_, inferErr := inferExprType(selectQuery.SelectItems[0].Expr, scope)
	if inferErr == nil {
		t.Fatalf("inferExprType(%q) did not return an error", exprSQL)
	}
	return inferErr
}

func TestParenthesizedExpressionType(t *testing.T) {
	schema := wrapperTestSchema(t)
	cases := []struct{ expr, want string }{
		{"(s)", "String"},
		{"(ns)", "Nullable(String)"},
		{"(1 + 1)", "UInt16"},
		{"((1 + 1))", "UInt16"},
		{"(u8, i8)", "Tuple(UInt8, Int8)"},
		{"if((s ILIKE '%a%'), 1, 2)", "UInt8"},
		// A parenthesized literal stays a constant for the
		// LowCardinality rule: concat(lc, ('a')) keeps the wrapper.
		{"lc = ('a')", "LowCardinality(UInt8)"},
	}
	for _, testCase := range cases {
		if got := inferCHTypeString(t, schema, testCase.expr); got != testCase.want {
			t.Errorf("type of %q = %s, want %s", testCase.expr, got, testCase.want)
		}
	}
}

// NOTE on a prefix operator inside CAST. The ClickHouse SERVER accepts a
// full expression as the first CAST argument, and it was measured:
// SELECT CAST(-i32 AS Int64) FROM t is -5, SELECT CAST(NOT i32 AS UInt8)
// FROM t is 0 and SELECT CAST(1+2 AS Int64) is 3.
//
// This gap is CLOSED: the front end refused such expressions up to v0.5.4,
// and v0.5.5 accepts them. The record stays because it names the cause, and
// because the same release traded this gap for the CASE operand gap. See
// docs/front-end-gaps.md.
//
// The historic cause was in parser_column.go: parseColumnCastExpr
// (line 583) reads the first CAST argument with parseColumnExpr (line 500),
// which parses a PRIMARY expression only. A prefix minus or NOT is handled
// one level higher, in parseUnaryExpr (line 367), and an infix operator in
// parseSubExpr (line 259); neither was on the CAST path. Thus CAST(-(1) AS
// Int64) failed with "unexpected token kind: -" at the minus. CAST(-1 AS
// Int64) parsed, because the lexer folds the minus into the adjacent number
// literal, which is why the failure needed the parenthesized form.
//
// A gap of this kind is a limit of the front end, not a defect of chgen:
// the type rule for a negated operand below is complete, and it applies as
// soon as such an expression reaches the resolver. chgen must not work
// around such a gap, because a workaround would mean that chgen parses SQL
// that the front end owns.

func TestUnaryExpressionType(t *testing.T) {
	schema := wrapperTestSchema(t)
	cases := []struct{ expr, want string }{
		// A minus on a bare literal folds into the literal.
		{"-1", "Int8"},
		{"-129", "Int16"},
		{"-1.5", "Float64"},
		// A minus on any other operand negates the operand type:
		// the unsigned types widen to the next signed type, UInt64
		// and the signed types keep their width.
		{"-(1)", "Int16"},
		{"-(256)", "Int32"},
		{"-(65536)", "Int64"},
		{"-(u8)", "Int16"},
		{"-(u16)", "Int32"},
		{"-(u32)", "Int64"},
		{"-(u64)", "Int64"},
		{"-(i8)", "Int8"},
		{"-(i64)", "Int64"},
		{"-(b)", "Int16"},
		{"-(f32)", "Float32"},
		// The wide integers negate like the narrower ones. Measured on
		// ClickHouse with a real column, because a literal folds and
		// then reports a different type:
		//   DESCRIBE (SELECT -i128 AS x FROM t) -> Int128
		//   DESCRIBE (SELECT -u128 AS x FROM t) -> Int128
		//   DESCRIBE (SELECT -i256 AS x FROM t) -> Int256
		//   DESCRIBE (SELECT -u256 AS x FROM t) -> Int256
		{"-(i128)", "Int128"},
		{"-(u128)", "Int128"},
		{"-(i256)", "Int256"},
		{"-(u256)", "Int256"},
		// The sized Decimal aliases keep the operand type, like dec.
		// ClickHouse reports the canonical spelling (DESCRIBE (SELECT
		// -d32 AS x FROM t) is Decimal(9, 4)), but negation is a
		// passthrough in chgen, so the alias spelling survives. Both
		// spellings map to the same Go type, thus the difference is
		// not observable in generated code.
		{"-(d32)", "Decimal32(4)"},
		{"-(d64)", "Decimal64(6)"},
		{"-(d128)", "Decimal128(10)"},
		{"-(dec)", "Decimal(18, 4)"},
		{"-(ni32)", "Nullable(Int32)"},
		{"-(nf64)", "Nullable(Float64)"},
		{"-(-(u8))", "Int16"},
		{"-(length(lc))", "LowCardinality(Int64)"},
		{"-(length(lcn))", "LowCardinality(Nullable(Int64))"},
		// NOT gives Bool for a Bool operand and UInt8 for any other
		// numeric operand, with the operand wrappers kept.
		{"NOT b", "Bool"},
		{"NOT u8", "UInt8"},
		{"NOT i64", "UInt8"},
		{"NOT f64", "UInt8"},
		{"NOT ni32", "Nullable(UInt8)"},
		{"NOT nf64", "Nullable(UInt8)"},
		// A negated literal stays a constant for the LowCardinality
		// rule: length(lc) + -300 keeps the wrapper.
		{"length(lc) + -300", "LowCardinality(Int64)"},
	}
	for _, testCase := range cases {
		if got := inferCHTypeString(t, schema, testCase.expr); got != testCase.want {
			t.Errorf("type of %q = %s, want %s", testCase.expr, got, testCase.want)
		}
	}
}

func TestUnaryExpressionRefusals(t *testing.T) {
	schema := wrapperTestSchema(t)
	// ClickHouse rejects these operand types, so chgen must refuse.
	for _, expr := range []string{"-(s)", "-(d)", "-(dt64)", "-(arr_i)", "NOT s", "NOT d", "NOT dec", "NOT arr_i"} {
		inferCHTypeError(t, schema, expr)
	}
}

func TestConcatSubstringNullIfToStartOfDayTypes(t *testing.T) {
	schema := wrapperTestSchema(t)
	cases := []struct{ expr, want string }{
		// concat always gives String, with the transparent wrappers.
		{"concat(s, s)", "String"},
		{"concat(fs, fs)", "String"},
		{"concat('abc', fs)", "String"},
		{"concat(s, s, s)", "String"},
		{"concat(ns, s)", "Nullable(String)"},
		{"concat(lcn, s)", "Nullable(String)"},
		{"concat(lc, 'a')", "LowCardinality(String)"},
		{"concat(lcn, 'a')", "LowCardinality(Nullable(String))"},
		{"concat(lc, lc)", "String"},
		{"concat(lc, s)", "String"},
		// substring always gives String, with the transparent wrappers.
		{"substring(s, 1, 2)", "String"},
		{"substring(fs, 1, 2)", "String"},
		{"substring(ns, 1, 2)", "Nullable(String)"},
		{"substring(lc, 1, 2)", "LowCardinality(String)"},
		{"substring(lcn, 1, 2)", "LowCardinality(Nullable(String))"},
		{"substring(lc, u8, 2)", "String"},
		{"substring(lcn, u8, 2)", "Nullable(String)"},
		{"substring(s, 1)", "String"},
		// nullIf gives the Nullable form of the first argument type,
		// with the transparent LowCardinality rule.
		{"nullIf(i32, i32)", "Nullable(Int32)"},
		{"nullIf(u8, i64)", "Nullable(UInt8)"},
		{"nullIf(u8, 300)", "Nullable(UInt8)"},
		{"nullIf(s, s)", "Nullable(String)"},
		{"nullIf(fs, fs)", "Nullable(FixedString(8))"},
		{"nullIf(dec, dec)", "Nullable(Decimal(18, 4))"},
		{"nullIf(b, b)", "Nullable(Bool)"},
		{"nullIf(d, d)", "Nullable(Date)"},
		{"nullIf(dt64, dt64)", "Nullable(DateTime64(3))"},
		{"nullIf(ni32, i32)", "Nullable(Int32)"},
		{"nullIf(f64, dec)", "Nullable(Float64)"},
		{"nullIf(lc, 'a')", "LowCardinality(Nullable(String))"},
		{"nullIf(lc, s)", "Nullable(String)"},
		{"nullIf(lc, lc)", "Nullable(String)"},
		{"nullIf(lcn, s)", "Nullable(String)"},
		// toStartOfDay gives DateTime for every temporal argument,
		// with the transparent wrappers.
		{"toStartOfDay(d)", "DateTime"},
		{"toStartOfDay(dt)", "DateTime"},
		{"toStartOfDay(dt64)", "DateTime"},
		{"toStartOfDay(nullIf(d, d))", "Nullable(DateTime)"},
	}
	for _, testCase := range cases {
		if got := inferCHTypeString(t, schema, testCase.expr); got != testCase.want {
			t.Errorf("type of %q = %s, want %s", testCase.expr, got, testCase.want)
		}
	}
}

func TestSubscriptAndArrayLiteralTypes(t *testing.T) {
	schema := wrapperTestSchema(t)
	cases := []struct{ expr, want string }{
		// Array subscript gives the element type. A Nullable index
		// makes the result Nullable.
		{"arr_i[1]", "Int32"},
		{"arr_s[1]", "String"},
		{"arr_i[u8]", "Int32"},
		{"arr_i[ni32]", "Nullable(Int32)"},
		{"arr_s[ni32]", "Nullable(String)"},
		// Map subscript gives the value type. A Nullable key makes
		// the result Nullable. A LowCardinality key does not.
		{"m['k']", "Int64"},
		{"m[s]", "Int64"},
		{"m[lc]", "Int64"},
		{"m[ns]", "Nullable(Int64)"},
		{"m[lcn]", "Nullable(Int64)"},
		// An array literal with elements of one type gives an Array
		// of that type.
		{"[1, 2, 3]", "Array(UInt8)"},
		{"['a', 'b']", "Array(String)"},
		{"[1.5, 2.5]", "Array(Float64)"},
		{"[ni32]", "Array(Nullable(Int32))"},
		{"[1, 2][1]", "UInt8"},
		{"[1, 2][ni32]", "Nullable(UInt8)"},
		// A bare true or false parses as an identifier, not a
		// literal, and has the type Bool.
		{"true", "Bool"},
		{"false", "Bool"},
		{"if(true, 1, 2)", "UInt8"},
	}
	for _, testCase := range cases {
		if got := inferCHTypeString(t, schema, testCase.expr); got != testCase.want {
			t.Errorf("type of %q = %s, want %s", testCase.expr, got, testCase.want)
		}
	}
}

func TestSubscriptRefusals(t *testing.T) {
	schema := wrapperTestSchema(t)
	// ClickHouse rejects a subscript on a String, so chgen must refuse.
	// An array literal that mixes element types refuses only when the
	// members have no common supertype, which is the answer of the
	// server as well: [s, 1] is Code 386, NO_COMMON_TYPE on 25.8.29.51.
	//
	// [1, 300] was in this list before, because chgen did not model the
	// supertype lattice for an array literal. The server accepts it
	// (SELECT toTypeName([1, 300]) is Array(UInt16)), thus the refusal
	// was wrong. The cell now lives in
	// TestContainerConstructorMemberSupertype.
	for _, expr := range []string{"s[1]", "i64[1]", "[s, 1]"} {
		inferCHTypeError(t, schema, expr)
	}
}

func TestConstantExpressionKeepsLowCardinality(t *testing.T) {
	schema := wrapperTestSchema(t)
	// ClickHouse folds an expression without column references into a
	// constant, so the LowCardinality rule must treat it as one.
	cases := []struct{ expr, want string }{
		{"concat(lc, trim(toString(-2.5)))", "LowCardinality(String)"},
		{"concat(lc, toString(now()))", "LowCardinality(String)"},
		{"concat(lc, ('a' || 'b'))", "LowCardinality(String)"},
		{"concat(lc, substring('abc', 1, 2))", "LowCardinality(String)"},
		{"lcn || substring('abc', 1, 2)", "LowCardinality(Nullable(String))"},
		{"substring((lower('%a%') || lc), 1, 2)", "LowCardinality(String)"},
		{"length(lc) + length('ab')", "LowCardinality(UInt64)"},
		// An expression that reads a column is not a constant.
		{"concat(lc, toString(u8))", "String"},
		{"concat(lc, lower(s))", "String"},
	}
	for _, testCase := range cases {
		if got := inferCHTypeString(t, schema, testCase.expr); got != testCase.want {
			t.Errorf("type of %q = %s, want %s", testCase.expr, got, testCase.want)
		}
	}
}
