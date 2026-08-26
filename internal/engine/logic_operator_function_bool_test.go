package engine

import (
	"testing"

	clickhouse "github.com/AfterShip/clickhouse-sql-parser/parser"
)

// This file pins the regression: the FUNCTION spelling of and, or and xor now
// has a type rule (inferLogicOperatorFunctionType), and that rule carries
// a Bool-stickiness axis that AND/OR and xor answer DIFFERENTLY.
//
// Every expected type was measured on ClickHouse 25.8.29.51 with
// toTypeName over real table columns, never over literals, because the
// server folds constants. and, or and xor share the SAME argument domain
// (isLogicOperandType / logicOperatorArgumentDomain): the narrow integers
// (Int8/16/32/64, UInt8/16/32/64), the two floats and Bool. They refuse
// the wide integers (Int128/256, UInt128/256), every Decimal, every
// temporal type, String, FixedString, Enum, UUID, IPv4, IPv6 and the
// containers, each with Code: 43.

// logicOperatorBoolTestSchema gives a schema with the Bool, Nullable(Bool)
// and wide-integer columns that the Bool-stickiness and domain-refusal
// cases need. logicOperatorTestSchema (in
// logic_operator_lowcardinality_test.go) covers the LowCardinality axis
// only and has no Bool or wide-integer column.
func logicOperatorBoolTestSchema(t *testing.T) *Schema {
	t.Helper()
	schema, err := schemaFromDDLErr(t, `CREATE TABLE t (
    u8    UInt8,
    b     Bool,
    nb    Nullable(Bool),
    nb2   Nullable(Bool),
    n     Nullable(UInt8),
    s     String,
    i128  Int128,
    dec   Decimal32(2)
) ENGINE = MergeTree ORDER BY tuple();`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	return schema
}

func inferLogicOperatorFunctionBoolType(t *testing.T, schema *Schema, exprSQL string) string {
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
		t.Fatalf("inferExprType(%q): unexpected refusal: %v", exprSQL, err)
	}
	return inferred.String()
}

// TestLogicOperatorFunctionFormBoolStickiness pins the measured
// Bool-stickiness grid of and, or and xor in the FUNCTION spelling.
//
// AND and OR make the base Bool only when a Bool value sits OUTSIDE a
// Nullable wrapper (the same rule the infix path already measured, in
// andOrOperandCarriesBareBool). xor makes the base Bool when a Bool value
// is present at ALL, Nullable or not (xorOperandCarriesBool). The two
// rules give a DIFFERENT answer for the SAME pair of Nullable(Bool)
// columns:
//
//	and(nb, nb2)   Nullable(UInt8)   every Bool is under Nullable
//	xor(nb, nb2)   Nullable(Bool)    xor counts a Bool under Nullable
//
// BOTH SIDES of the grid are pinned on purpose, exactly as
// TestLogicOperatorLowCardinalityGrid does: a test that pinned only the
// cells where Bool survives would let a later blanket change make every
// cell Bool and still pass.
func TestLogicOperatorFunctionFormBoolStickiness(t *testing.T) {
	schema := logicOperatorBoolTestSchema(t)
	cases := []struct {
		expr string
		want string
	}{
		// No Bool anywhere: every operator gives UInt8.
		{"and(u8, u8)", "UInt8"},
		{"or(u8, u8)", "UInt8"},
		{"xor(u8, u8)", "UInt8"},

		// A bare Bool operand: every operator gives Bool.
		{"and(b, b)", "Bool"},
		{"or(b, b)", "Bool"},
		{"xor(b, b)", "Bool"},
		{"and(b, u8)", "Bool"},
		{"xor(b, u8)", "Bool"},

		// Two Nullable(Bool) operands: AND stays Nullable(UInt8), xor
		// becomes Nullable(Bool). This is the one cell where the two
		// families disagree, and it is the reason xor needs its own
		// predicate (xorOperandCarriesBool) rather than reusing
		// andOrOperandCarriesBareBool.
		{"and(nb, nb2)", "Nullable(UInt8)"},
		{"or(nb, nb2)", "Nullable(UInt8)"},
		{"xor(nb, nb2)", "Nullable(Bool)"},

		// A Nullable(Bool) mixed with a bare Bool: the bare Bool alone
		// already makes AND/OR answer Bool, thus this cell does not
		// distinguish the two families, but it is pinned anyway because
		// it is a measured cell in its own right.
		{"and(nb, b)", "Nullable(Bool)"},
		{"xor(nb, b)", "Nullable(Bool)"},

		// A Nullable(Bool) mixed with a plain, non-Bool Nullable: AND
		// still has no bare Bool, xor still has a Bool under Nullable.
		{"and(nb, n)", "Nullable(UInt8)"},
		{"xor(nb, n)", "Nullable(Bool)"},

		// Arity 3: the bare Bool (for AND) or the Nullable(Bool) (for
		// xor) still counts wherever it sits in the argument list.
		{"and(u8, u8, b)", "Bool"},
		{"xor(u8, u8, nb)", "Nullable(Bool)"},
	}
	for _, testCase := range cases {
		t.Run(testCase.expr, func(t *testing.T) {
			got := inferLogicOperatorFunctionBoolType(t, schema, testCase.expr)
			if got != testCase.want {
				t.Errorf("inferExprType(%q) = %s, want %s", testCase.expr, got, testCase.want)
			}
		})
	}
}

// TestLogicOperatorFunctionFormDomainRefuses pins the measured argument
// domain of and, or and xor: the narrow integers, the two floats and
// Bool are accepted, and everything else is refused with a message that
// names the domain, exactly as the server refuses it with Code: 43.
//
// The wide integers (Int128 here) are the one boundary that a domain
// built on the general integerBaseType helper would miss, because that
// helper accepts them for the arithmetic aggregates. See
// isLogicOperandType for the full measured table.
func TestLogicOperatorFunctionFormDomainRefuses(t *testing.T) {
	schema := logicOperatorBoolTestSchema(t)
	cases := []string{
		"and(u8, s)",
		"or(u8, s)",
		"xor(u8, s)",
		"and(i128, i128)",
		"or(i128, i128)",
		"xor(i128, i128)",
		"and(dec, dec)",
		"or(dec, dec)",
		"xor(dec, dec)",
		// The refusal must name the OFFENDING argument even when it is
		// not the first: and(u8, s) and and(s, u8) both refuse, and the
		// generic argsGeneric domain check in inferFunctionType only
		// reaches argument zero, so this call would otherwise slip
		// through with a type for a call the server refuses.
		"and(s, u8)",
	}
	for _, exprSQL := range cases {
		t.Run(exprSQL, func(t *testing.T) {
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
			if inferred, err := inferExprType(selectQuery.SelectItems[0].Expr, scope); err == nil {
				t.Errorf("inferExprType(%q) = %s, want a refusal", exprSQL, inferred.String())
			}
		})
	}
}

// TestLogicOperatorFunctionFormArity pins that and, or and xor refuse a
// call with fewer than two arguments, exactly as ClickHouse itself does
// (measured: and(u8) is Code: 35,
// "Number of arguments for function \"and\" should be at least 2").
func TestLogicOperatorFunctionFormArity(t *testing.T) {
	schema := logicOperatorBoolTestSchema(t)
	cases := []string{"and(u8)", "or(u8)", "xor(u8)"}
	for _, exprSQL := range cases {
		t.Run(exprSQL, func(t *testing.T) {
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
			if inferred, err := inferExprType(selectQuery.SelectItems[0].Expr, scope); err == nil {
				t.Errorf("inferExprType(%q) = %s, want a refusal", exprSQL, inferred.String())
			}
		})
	}
}
