package engine

import (
	"testing"

	clickhouse "github.com/AfterShip/clickhouse-sql-parser/parser"
)

// These tests pin two rules that decide the type of a large unsigned
// integer literal inside a nested expression. Every expected value was
// measured on ClickHouse 25.8.29.51 with SELECT toTypeName(<expr>) FROM t
// against the real columns of the oracle fixture, never against bare
// constants, because the server folds a bare constant and then reports a
// type that the same literal does not keep inside an expression.

// TestLargeLiteralNarrowsThroughPassThroughFunction pins the rule that a
// large unsigned integer literal keeps the freedom to become Int64 while
// it travels to a branch position through a function that passes one
// argument type through unchanged.
//
// Measured on ClickHouse 25.8.29.51. The literal alone is UInt64, and so
// is every one of these sub-expressions alone:
//
//	toTypeName(10000000000)                -> UInt64
//	toTypeName(nullIf(10000000000, i32))   -> Nullable(UInt64)
//	toTypeName(ifNull(10000000000, u8))    -> UInt64
//
// Yet in a branch pair against Int64 the literal narrows:
//
//	if(b, i64, nullIf(10000000000, i32))   -> Nullable(Int64)
//	if(b, i64, ifNull(10000000000, u8))    -> Int64
//	if(b, i64, if(b, 10000000000, 10000000001)) -> Int64
func TestLargeLiteralNarrowsThroughPassThroughFunction(t *testing.T) {
	schema := supertypeTestSchema(t)
	cases := []struct{ expr, want string }{
		// The ticket reproduction.
		{"if(b, dateDiff('second', d, dt), nullIf(10000000000, i32))", "Nullable(Int64)"},
		// nullIf and ifNull take the type of the first argument, so
		// the literal keeps its freedom to narrow.
		{"if(b, i64, nullIf(10000000000, i32))", "Nullable(Int64)"},
		{"if(b, i64, nullIf(10000000000, u8))", "Nullable(Int64)"},
		{"if(b, i64, ifNull(10000000000, u8))", "Int64"},
		{"if(b, i64, coalesce(10000000000))", "Int64"},
		// A nested branch construct whose every branch is itself a
		// narrowable literal passes the freedom outward.
		{"if(b, i64, if(b, 10000000000, 10000000001))", "Int64"},
		{"if(b, i64, multiIf(b, 10000000000, 10000000001))", "Int64"},
		// A parenthesised literal is still the literal.
		{"if(b, i64, (10000000000))", "Int64"},
	}
	for _, testCase := range cases {
		got, err := inferCHTypeStringErr(t, schema, testCase.expr)
		if err != nil {
			t.Errorf("type of %q refused: %v, want %s", testCase.expr, err, testCase.want)
			continue
		}
		if got != testCase.want {
			t.Errorf("type of %q = %s, want %s", testCase.expr, got, testCase.want)
		}
	}
}

// TestLargeLiteralStaysUnsignedWhenPinned pins the other half of the same
// rule, and it is the half that protects against a silently wrong type.
// Once the literal is merged into a supertype with any peer, or is put
// through a cast or an arithmetic operator, it is a hard UInt64 and the
// pair against Int64 has no supertype. Every one of these is Code: 386
// (NO_COMMON_TYPE) on ClickHouse 25.8.29.51, so chgen MUST refuse them.
func TestLargeLiteralStaysUnsignedWhenPinned(t *testing.T) {
	schema := supertypeTestSchema(t)
	refused := []string{
		// A cast pins the type outright.
		"if(b, i64, toUInt64(10000000000))",
		// Arithmetic produces a new value, not the literal.
		"if(b, i64, 10000000000 + 0)",
		// greatest and least compute a supertype of their arguments,
		// which pins the literal before it reaches the outer branch.
		"if(b, i64, greatest(10000000000, 5))",
		"if(b, i64, least(10000000000, u32))",
		// A branch construct that merges the literal with a peer of a
		// different type pins it too.
		"if(b, i64, multiIf(b, 10000000000, u8))",
		"if(b, i64, if(b, 10000000000, u8))",
		// A real UInt64 column never narrows.
		"if(b, i64, u64)",
		"if(b, i64, nullIf(u64, i32))",
	}
	for _, expr := range refused {
		if got, err := inferCHTypeStringErr(t, schema, expr); err == nil {
			t.Errorf("type of %q = %s, want a refusal (the server answers Code: 386)", expr, got)
		}
	}
}

// TestMultiBranchSupertypeIsComputedAtOnce pins the rule that ClickHouse
// computes the supertype over every branch together, not by folding the
// branches pairwise in source order.
//
// Measured on ClickHouse 25.8.29.51:
//
//	if(b, u32, i16)                    -> Int64
//	if(b, if(b, u32, i16), 1e10)       -> Code: 386
//	multiIf(b, u32, false, i16, 1e10)  -> Float64
//
// A pairwise fold reaches Int64 first and then refuses Int64 with
// Float64, although the true three way join is Float64. UInt32 and Int16
// both fit Float64 exactly, so the joint answer is Float64.
func TestMultiBranchSupertypeIsComputedAtOnce(t *testing.T) {
	schema := supertypeTestSchema(t)
	cases := []struct{ expr, want string }{
		// The ticket reproduction.
		{"multiIf(b, u32, false, i16, 1e10)", "Float64"},
		{"multiIf(b, u32, false, i16, f64)", "Float64"},
		// The pairwise fold of the same branches is still refused,
		// because there the Int64 really is an intermediate result.
		{"if(b, u32, i16)", "Int64"},
	}
	for _, testCase := range cases {
		got, err := inferCHTypeStringErr(t, schema, testCase.expr)
		if err != nil {
			t.Errorf("type of %q refused: %v, want %s", testCase.expr, err, testCase.want)
			continue
		}
		if got != testCase.want {
			t.Errorf("type of %q = %s, want %s", testCase.expr, got, testCase.want)
		}
	}
	// An explicit nested if really does compute Int64 first, so the
	// outer join against Float64 has no supertype and must stay refused.
	if got, err := inferCHTypeStringErr(t, schema, "if(b, if(b, u32, i16), 1e10)"); err == nil {
		t.Errorf("type of nested if = %s, want a refusal (the server answers Code: 386)", got)
	}
}

// TestNoCommonTypePairsStayRefused guards the boundary that a previous fix
// in this area broke. Widening the literal rule must not start accepting a
// pair that the server refuses. Every pair here is Code: 386 on
// ClickHouse 25.8.29.51.
func TestNoCommonTypePairsStayRefused(t *testing.T) {
	schema := supertypeTestSchema(t)
	refused := []string{
		"if(b, i64, u64)",
		"if(b, i64, f64)",
		"if(b, u64, f64)",
		"if(b, i32, u64)",
		"if(b, i64, toFloat64(1))",
		"if(b, u64, toFloat64(1))",
	}
	for _, expr := range refused {
		if got, err := inferCHTypeStringErr(t, schema, expr); err == nil {
			t.Errorf("type of %q = %s, want a refusal (the server answers Code: 386)", expr, got)
		}
	}
	// The pairs next to them that the server DOES join must keep working.
	accepted := []struct{ expr, want string }{
		{"if(b, i32, f64)", "Float64"},
		{"if(b, u32, f64)", "Float64"},
		{"if(b, i64, u32)", "Int64"},
		{"if(b, i32, f32)", "Float64"},
		{"if(b, i16, f32)", "Float32"},
	}
	for _, testCase := range accepted {
		got, err := inferCHTypeStringErr(t, schema, testCase.expr)
		if err != nil {
			t.Errorf("type of %q refused: %v, want %s", testCase.expr, err, testCase.want)
			continue
		}
		if got != testCase.want {
			t.Errorf("type of %q = %s, want %s", testCase.expr, got, testCase.want)
		}
	}
}

// inferCHTypeStringErr is inferCHTypeString without the fatal on a refusal,
// so that a test can assert that chgen refuses an expression.
func inferCHTypeStringErr(t *testing.T, schema *Schema, exprSQL string) (string, error) {
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

// TestLargeLiteralNarrowsThroughCaseExpression pins the rule for the CASE
// syntax form. A CASE is the syntax form of multiIf: its result is the
// supertype of its value branches, which are the THEN values and the ELSE
// value. The CASE operand and the WHEN conditions never reach the result.
//
// chgen refused the first case below although ClickHouse both types it
// and executes it, because the narrowing walk knew the function forms if,
// multiIf and coalesce but not the CASE node.
//
// Measured on ClickHouse 25.8.29.51 against the oracle fixture columns,
// with toTypeName for the analysis witness and a plain SELECT for the
// execution witness:
//
//	toInt128(if(empty('abc'), arr_i[1],
//	    (CASE WHEN true THEN 10000000000 ELSE 10000000000 END)))
//	    -> analysis Int128, execution 10000000000
func TestLargeLiteralNarrowsThroughCaseExpression(t *testing.T) {
	schema := supertypeTestSchema(t)
	cases := []struct{ expr, want string }{
		// The ticket reproduction, and its hand shrink.
		{"toInt128(if(empty('abc'), arr_i[1], (CASE WHEN true THEN 10000000000 ELSE 10000000000 END)))", "Int128"},
		{"if(b, arr_i[1], CASE WHEN b THEN 10000000000 ELSE 10000000000 END)", "Int64"},
		// Every value branch is a narrowable literal, so the CASE
		// passes the freedom outward exactly as multiIf does.
		{"if(b, i64, CASE WHEN b THEN 10000000000 ELSE 10000000001 END)", "Int64"},
		// A simple CASE compares its operand against each WHEN. The
		// operand is not a value branch, thus a String operand does
		// not pin the integer result.
		{"if(b, i64, CASE s WHEN 'abc' THEN 10000000000 ELSE 10000000001 END)", "Int64"},
		// A missing ELSE adds an implicit NULL, not a typed peer, so
		// the literal still narrows and the result gains Nullable.
		{"if(b, i64, CASE WHEN b THEN 10000000000 END)", "Nullable(Int64)"},
	}
	for _, testCase := range cases {
		got, err := inferCHTypeStringErr(t, schema, testCase.expr)
		if err != nil {
			t.Errorf("type of %q refused: %v, want %s", testCase.expr, err, testCase.want)
			continue
		}
		if got != testCase.want {
			t.Errorf("type of %q = %s, want %s", testCase.expr, got, testCase.want)
		}
	}
}

// TestLargeLiteralStaysUnsignedWhenPinnedInCase is the protective half of
// the CASE rule. A CASE that merges the literal with any pinned peer has
// no supertype against Int64, so chgen MUST keep refusing it. Every one
// of these is Code: 386 (NO_COMMON_TYPE) on ClickHouse 25.8.29.51.
func TestLargeLiteralStaysUnsignedWhenPinnedInCase(t *testing.T) {
	schema := supertypeTestSchema(t)
	refused := []string{
		// A column peer pins the branch.
		"if(b, i64, CASE WHEN b THEN 10000000000 ELSE u8 END)",
		"if(b, i64, CASE s WHEN 'abc' THEN 10000000000 ELSE u8 END)",
		// A cast pins the type outright.
		"if(b, i64, CASE WHEN b THEN toUInt64(10000000000) ELSE 1 END)",
		// A small literal peer pins the branch too: see
		// TestSmallLiteralPeerPinsTheBranch for the measured boundary.
		"if(b, i64, CASE WHEN b THEN 10000000000 ELSE 1 END)",
		// The same rule one level down, through a nested CASE.
		"if(b, i64, CASE WHEN b THEN CASE WHEN b THEN 10000000000 ELSE 1 END ELSE 2 END)",
	}
	for _, expr := range refused {
		if got, err := inferCHTypeStringErr(t, schema, expr); err == nil {
			t.Errorf("type of %q = %s, want a refusal (the server answers Code: 386)", expr, got)
		}
	}
}

// TestSmallLiteralPeerPinsTheBranch pins the size boundary of a narrowable
// literal. ClickHouse gives a literal the SMALLEST type that holds it, so
// a peer literal that fits UInt32 or less arrives already pinned to that
// unsigned type and takes the whole branch with it. Only a literal that
// needs more than 32 bits keeps the freedom to become Int64.
//
// Measured on ClickHouse 25.8.29.51 by moving the peer literal in
// "if(b, i64, if(b, 10000000000, PEER))":
//
//	PEER = 1, 255, 256, 65535, 4294967295  -> Code: 386
//	PEER = 4294967296, 10000000001         -> Int64
//
// This is not specific to CASE. The same boundary holds for if and
// multiIf, where chgen accepted the small peer and returned Int64 while
// the server refused the expression outright.
func TestSmallLiteralPeerPinsTheBranch(t *testing.T) {
	schema := supertypeTestSchema(t)
	refused := []string{
		"if(b, i64, if(b, 10000000000, 1))",
		"if(b, i64, if(b, 10000000000, 4294967295))",
		"if(b, i64, multiIf(b, 10000000000, 1))",
		"if(b, i64, if(b, if(b, 10000000000, 1), 2))",
	}
	for _, expr := range refused {
		if got, err := inferCHTypeStringErr(t, schema, expr); err == nil {
			t.Errorf("type of %q = %s, want a refusal (the server answers Code: 386)", expr, got)
		}
	}
	// Just above the boundary the literal narrows again.
	accepted := []struct{ expr, want string }{
		{"if(b, i64, if(b, 10000000000, 4294967296))", "Int64"},
	}
	for _, testCase := range accepted {
		got, err := inferCHTypeStringErr(t, schema, testCase.expr)
		if err != nil {
			t.Errorf("type of %q refused: %v, want %s", testCase.expr, err, testCase.want)
			continue
		}
		if got != testCase.want {
			t.Errorf("type of %q = %s, want %s", testCase.expr, got, testCase.want)
		}
	}
}
