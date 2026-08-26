package engine

import (
	"strings"
	"testing"
)

// A wrapper node gives a result type. That result type says what the
// call gives back. It does not say that the argument of the call is
// legal. Before this test existed, a node with a fixed result type and
// the wrapperOpaque class gave its type without ever inferring its
// argument, thus uniq(bogusfn(s)) was UInt64 with no error. CAST and
// the condition slot of if and multiIf had the same gap.
//
// An argument that is never inferred cannot break a domain rule, thus
// this gap disarmed every argument rule that chgen has. The tests below
// pin the propagation.
//
// Every expected answer was measured on ClickHouse 25.8.29.51 with
// SELECT toTypeName(<expression>) FROM t over real columns:
//
//	count(*)                        UInt64
//	count(s)                        UInt64
//	uniq(s)                         UInt64
//	isNull(ni32)                    UInt8
//	has(arr_i, 1)                   UInt8
//	arrayExists(x -> x > 0, arr_i)  UInt8
//	today()                         Date
//	uniq(bogusfn(s))                Code: 46, no such function
//	CAST(bogusfn(s) AS Int32)       Code: 46, no such function
//	if(bogusfn(s), i32, i32)        Code: 46, no such function
//	multiIf(bogusfn(s), i32, i32)   Code: 46, no such function

// TestWrapperRefusesUntypeableArgument pins that a wrapper gives the
// failure of its argument back. `bogusfn` is not a ClickHouse function,
// thus the server refuses every one of these with Code: 46. A type here
// would be a silently wrong answer.
func TestWrapperRefusesUntypeableArgument(t *testing.T) {
	schema := wrapperTestSchema(t)
	for _, expr := range []string{
		// The fixed-result aggregates. These are the names that
		// carry the wrapperOpaque class with the argsIndependent
		// strategy, which was the leaking combination.
		"count(bogusfn(s))",
		"countIf(bogusfn(s))",
		"uniq(bogusfn(s))",
		"uniqExact(bogusfn(s))",
		"uniqExactIf(bogusfn(s))",
		"uniqCombined(bogusfn(s))",
		// The fixed-result scalar wrappers.
		"isNull(bogusfn(s))",
		"isNotNull(bogusfn(s))",
		"has(arr_i, bogusfn(s))",
		"arrayExists(bogusfn(s))",
		"arrayStringConcat(bogusfn(s))",
		// A window rank takes no argument, but a call that gives
		// one must still not hide it.
		"row_number(bogusfn(s)) OVER ()",
		"rank(bogusfn(s)) OVER ()",
		"dense_rank(bogusfn(s)) OVER ()",
		"today(bogusfn(s))",
		// CAST names its result, but the source must still type.
		"CAST(bogusfn(s) AS Int32)",
		"CAST(bogusfn(s) AS String)",
		// The condition slot of if and multiIf does not reach the
		// result type, and it must still be inferred.
		"if(bogusfn(s), i32, i32)",
		"multiIf(bogusfn(s), i32, i32)",
		"multiIf(b, i32, bogusfn(s), i32, i32)",
		// The value slots already refused. Keep them pinned so a
		// later change cannot open them.
		"if(b, bogusfn(s), i32)",
		"multiIf(b, bogusfn(s), i32)",
		"coalesce(bogusfn(s), i32)",
		"ifNull(bogusfn(s), i32)",
		"sum(bogusfn(s))",
		"quantile(0.5)(bogusfn(s))",
	} {
		t.Run(expr, func(t *testing.T) {
			err := inferCHTypeError(t, schema, expr)
			if err == nil {
				return
			}
			// The refusal must name the unknown function, so the
			// user reads why the query stopped.
			if !strings.Contains(err.Error(), "bogusfn") {
				t.Errorf("inferExprType(%q) error = %v, want the error to name bogusfn", expr, err)
			}
		})
	}
}

// TestWrapperKeepsLegalArgument is the negative half. A refusal is
// better than a wrong type, but a refusal of a query that the server
// runs is a defect of its own. Each expression here was accepted by
// ClickHouse 25.8.29.51, thus chgen must give it a type.
func TestWrapperKeepsLegalArgument(t *testing.T) {
	schema := wrapperTestSchema(t)
	cases := []struct {
		expr string
		want string
	}{
		// count(*) has no argument expression at all. The parser
		// gives the star as an identifier named "*", and a column
		// lookup for it fails. The argument walk must step over it.
		{"count(*)", "UInt64"},
		{"count()", "UInt64"},
		{"count(s)", "UInt64"},
		{"count(ni32)", "UInt64"},
		{"countIf(b)", "UInt64"},
		{"uniq(s)", "UInt64"},
		{"uniq(i32, s)", "UInt64"},
		{"uniqExact(s)", "UInt64"},
		// The scalar wrappers over a real column.
		{"isNull(ni32)", "UInt8"},
		{"isNotNull(ni32)", "UInt8"},
		{"has(arr_i, 1)", "UInt8"},
		{"arrayExists(x -> x > 0, arr_i)", "UInt8"},
		// The no-argument forms must not need an argument.
		{"row_number() OVER ()", "UInt64"},
		{"rank() OVER ()", "UInt64"},
		{"dense_rank() OVER ()", "UInt64"},
		{"today()", "Date"},
		{"now()", "DateTime"},
		// CAST over a real source keeps its target type.
		{"CAST(s AS Int32)", "Int32"},
		{"CAST(i32 AS String)", "String"},
		{"CAST(ni32 AS Int64)", "Int64"},
		// The condition slot over a real column still works.
		{"if(b, i32, i32)", "Int32"},
		{"multiIf(b, i32, b, i32, i32)", "Int32"},
		{"if(i32 = 1, i32, i32)", "Int32"},
	}
	for _, testCase := range cases {
		t.Run(testCase.expr, func(t *testing.T) {
			if got := inferCHTypeString(t, schema, testCase.expr); got != testCase.want {
				t.Errorf("inferExprType(%q) = %q, want %q", testCase.expr, got, testCase.want)
			}
		})
	}
}

// TestWrapperAbsorbsPlaceholderArgument pins the one argument failure
// that a wrapper must NOT give back. A bare positional placeholder has
// no result type by construction, and a pinned parameter in a wrapper
// is a normal query. The argument walk added for the propagation fix
// must not turn these into refusals.
func TestWrapperAbsorbsPlaceholderArgument(t *testing.T) {
	schema := wrapperTestSchema(t)
	cases := []struct {
		expr string
		want string
	}{
		{"countIf(i32 = ?)", "UInt64"},
		{"uniqIf(s, i32 = ?)", "UInt64"},
		{"count(?)", "UInt64"},
		{"CAST(? AS Int32)", "Int32"},
		{"CAST(? AS String)", "String"},
		{"if(?, i32, i32)", "Int32"},
		{"multiIf(?, i32, i32)", "Int32"},
	}
	for _, testCase := range cases {
		t.Run(testCase.expr, func(t *testing.T) {
			if got := inferCHTypeString(t, schema, testCase.expr); got != testCase.want {
				t.Errorf("inferExprType(%q) = %q, want %q", testCase.expr, got, testCase.want)
			}
		})
	}
}
