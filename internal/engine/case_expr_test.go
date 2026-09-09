package engine

import (
	"strings"
	"testing"
)

// These tests pin the CASE WHEN result type and the refusal behaviour of
// the aggregate wrapper path.
//
// Each expected type was measured on ClickHouse 25.8.29.51 (image
// clickhouse/clickhouse-server:25.8, disposable container) with
// SELECT toTypeName(<expression>) FROM <table>, always over real table
// columns and never over literals only, because ClickHouse folds
// constants and a folded constant reports a different wrapper.
//
// The measured rule of CASE is the rule of multiIf:
//
//	CASE WHEN b THEN s ELSE s END      -> String
//	CASE WHEN b THEN s END             -> Nullable(String)   (no ELSE)
//	CASE WHEN b THEN i16 ELSE i32 END  -> Int32              (supertype)
//	CASE WHEN b THEN lc ELSE lc END    -> String             (LowCardinality drops)
//	CASE WHEN b THEN lcn ELSE lc END   -> Nullable(String)
//	CASE i32 WHEN 1 THEN s END         -> Nullable(String)   (simple form)
func TestCaseExprType(t *testing.T) {
	schema := wrapperTestSchema(t)
	cases := []struct{ expr, want string }{
		// The searched form with an ELSE takes the supertype of the
		// value branches.
		{"CASE WHEN b THEN s ELSE s END", "String"},
		{"CASE WHEN b THEN i16 ELSE i32 END", "Int32"},
		{"CASE WHEN b THEN i32 ELSE ni32 END", "Nullable(Int32)"},
		// Without an ELSE the result becomes Nullable, because a row
		// that matches no branch gives NULL.
		{"CASE WHEN b THEN s END", "Nullable(String)"},
		{"CASE WHEN b THEN ns END", "Nullable(String)"},
		{"CASE WHEN b THEN i32 END", "Nullable(Int32)"},
		// LowCardinality never survives a CASE.
		{"CASE WHEN b THEN lc ELSE lc END", "String"},
		{"CASE WHEN b THEN lc ELSE s END", "String"},
		{"CASE WHEN b THEN lcn ELSE lc END", "Nullable(String)"},
		{"CASE WHEN b THEN lc END", "Nullable(String)"},
		// The simple form with a base expression behaves the same.
		{"CASE i32 WHEN 1 THEN s END", "Nullable(String)"},
		{"CASE i32 WHEN 1 THEN i16 ELSE i32 END", "Int32"},
		// More than one WHEN branch takes the supertype of them all.
		{"CASE WHEN b THEN i16 WHEN b THEN i32 ELSE i8 END", "Int32"},
	}
	for _, testCase := range cases {
		if got := inferCHTypeString(t, schema, testCase.expr); got != testCase.want {
			t.Errorf("type of %q = %s, want %s", testCase.expr, got, testCase.want)
		}
	}
}

// A CASE inside the argument of a function must move its wrappers into the
// result. Before the CASE rule these expressions refused; before the
// refusal rule they returned a bare type and silently dropped the
// Nullable, which is the N1 defect.
func TestCaseExprInsideFunctionKeepsWrappers(t *testing.T) {
	schema := wrapperTestSchema(t)
	cases := []struct{ expr, want string }{
		{"concat('x', CASE WHEN b THEN lcn END)", "Nullable(String)"},
		{"length(CASE WHEN b THEN ns END)", "Nullable(UInt64)"},
		{"toInt32(CASE WHEN b THEN i16 END)", "Nullable(Int32)"},
		{"toString(CASE WHEN b THEN i32 ELSE i32 END)", "String"},
	}
	for _, testCase := range cases {
		if got := inferCHTypeString(t, schema, testCase.expr); got != testCase.want {
			t.Errorf("type of %q = %s, want %s", testCase.expr, got, testCase.want)
		}
	}
}

// An argument that inference cannot type must make the whole expression
// refuse. A guess of the bare result type would silently drop a wrapper.
// This is the systemic half of N1: the refusal must hold for every future
// untypeable construct, not only for the ones that have a rule today.
func TestUntypeableArgumentRefusesInsteadOfGuessing(t *testing.T) {
	schema := wrapperTestSchema(t)
	cases := []string{
		// The later arguments of an aggregate used to swallow their
		// error and keep a bare result.
		"argMax(i32, unknownFunctionWithNoRule(s))",
		"max(unknownFunctionWithNoRule(s))",
		"toString(unknownFunctionWithNoRule(s))",
	}
	for _, expr := range cases {
		err := inferCHTypeError(t, schema, expr)
		if !strings.Contains(err.Error(), "unknownFunctionWithNoRule") {
			t.Errorf("refusal for %q = %v, want the cause to name the construct", expr, err)
		}
		if !strings.Contains(err.Error(), "not ClickHouse type inference") {
			t.Errorf("refusal for %q = %v, want an honest explanation of Go overrides", expr, err)
		}
	}
}
