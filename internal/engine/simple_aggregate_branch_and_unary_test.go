package engine

import "testing"

// This file pins the two OPPOSITE rules for a SimpleAggregateFunction(f, T).
//
// Before these rules chgen refused forms that the server runs, for
// example "no common ClickHouse type for SimpleAggregateFunction(sum,
// Int64) and Int64" for if(c, sagg, i64), and "function sum does not
// accept an argument of type SimpleAggregateFunction(sum, Int64)". A
// false refusal breaks a query that works.
//
// The two groups need OPPOSITE answers, thus a blanket rule is wrong:
//
//   - The BRANCH group (if, multiIf, ifNull, coalesce) KEEPS the wrapper.
//   - The UNARY and COMPUTING-AGGREGATE group (-x, sum, avg, quantile)
//     UNWRAPS to the inner type.
//
// The tests below hold both groups together with their neighbour cells,
// so a later blanket change cannot pass: a change that unwraps
// everywhere fails the branch test, and a change that keeps everywhere
// fails the unary test.
//
// Every expected value was measured on ClickHouse 25.8.29.51 through the
// HTTP interface, against real columns of a real table, never over
// literals, because the server folds constants.
const simpleAggregateBranchSchema = `
CREATE TABLE t (
    sagg    SimpleAggregateFunction(sum, Int64),
    saggmax SimpleAggregateFunction(max, Int64),
    saggu   SimpleAggregateFunction(max, UInt8),
    saggf   SimpleAggregateFunction(sum, Float64),
    saggn   SimpleAggregateFunction(sum, Nullable(Int64)),
    saggd   SimpleAggregateFunction(sum, Decimal(38, 4)),
    saggs   SimpleAggregateFunction(min, String),
    i64     Int64,
    u8      UInt8,
    f64     Float64,
    dec     Decimal(38, 4),
    s       String,
    ni64    Nullable(Int64),
    nu8     Nullable(UInt8)
);
`

// TestSimpleAggregateBranchKeepsWrapper locks the branch group. The
// wrapper survives only when the peer EQUALS the inner type. A peer that
// merely widens to the inner type drops it, which is why the neighbour
// cells matter.
func TestSimpleAggregateBranchKeepsWrapper(t *testing.T) {
	schema, err := schemaFromDDLErr(t, simpleAggregateBranchSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	cases := []struct{ expr, want string }{
		// The peer EQUALS the inner type: the wrapper stays.
		{"if(i64 > 0, sagg, i64)", "SimpleAggregateFunction(sum, Int64)"},
		{"if(i64 > 0, sagg, sagg)", "SimpleAggregateFunction(sum, Int64)"},
		{"if(i64 > 0, saggu, u8)", "SimpleAggregateFunction(max, UInt8)"},
		{"if(i64 > 0, saggs, s)", "SimpleAggregateFunction(min, String)"},
		{"if(i64 > 0, saggd, dec)", "SimpleAggregateFunction(sum, Decimal(38, 4))"},
		{"if(i64 > 0, saggn, ni64)", "SimpleAggregateFunction(sum, Nullable(Int64))"},
		{"multiIf(i64 > 0, sagg, i64)", "SimpleAggregateFunction(sum, Int64)"},
		{"multiIf(i64 > 0, sagg, i64 > 1, i64, sagg)", "SimpleAggregateFunction(sum, Int64)"},
		{"ifNull(sagg, i64)", "SimpleAggregateFunction(sum, Int64)"},
		{"coalesce(sagg, i64)", "SimpleAggregateFunction(sum, Int64)"},

		// NEIGHBOUR CELLS. The peer only WIDENS to the inner type, thus
		// the wrapper goes. These cells make the test fail if the rule
		// is loosened to "the supertype fits".
		{"if(i64 > 0, sagg, u8)", "Int64"},
		{"if(i64 > 0, saggu, i64)", "Int64"},
		{"if(i64 > 0, sagg, saggu)", "Int64"},
		{"if(i64 > 0, saggn, i64)", "Nullable(Int64)"},
		{"if(i64 > 0, saggn, nu8)", "Nullable(Int64)"},
		{"if(i64 > 0, sagg, nu8)", "Nullable(Int64)"},

		// NEIGHBOUR CELLS. Only the FIRST argument can carry the wrapper
		// into the result. A wrapper in a later branch never does.
		{"if(i64 > 0, i64, sagg)", "Int64"},
		{"if(i64 > 0, sagg, saggmax)", "SimpleAggregateFunction(sum, Int64)"},
		{"if(i64 > 0, saggmax, sagg)", "SimpleAggregateFunction(max, Int64)"},
		{"multiIf(i64 > 0, sagg, i64 > 1, i64, saggmax)", "SimpleAggregateFunction(sum, Int64)"},

		// NEIGHBOUR CELLS. A Nullable peer of an EQUAL inner type keeps
		// the wrapper and puts the Nullable OUTSIDE it.
		// SimpleAggregateFunction accepts a Nullable, unlike
		// AggregateFunction, thus a HasPrefix guard on the name would be
		// wrong here.
		{"if(i64 > 0, sagg, ni64)", "Nullable(SimpleAggregateFunction(sum, Int64))"},
		{"if(i64 > 0, saggu, nu8)", "Nullable(SimpleAggregateFunction(max, UInt8))"},

		// NEIGHBOUR CELLS. coalesce and ifNull read a Nullable INNER
		// type as Nullable, thus they walk past such an argument.
		{"ifNull(saggn, i64)", "Int64"},
		{"coalesce(saggn, i64)", "Int64"},
		{"coalesce(saggn, saggn, i64)", "Int64"},
		{"ifNull(saggn, ni64)", "Nullable(Int64)"},
	}
	for _, testCase := range cases {
		got, err := inferSelectItemCHType(t, schema, "SELECT "+testCase.expr+" AS a FROM t")
		if err != nil {
			t.Errorf("%s: error = %v, want %q", testCase.expr, err, testCase.want)
			continue
		}
		if got != testCase.want {
			t.Errorf("%s = %q, want %q", testCase.expr, got, testCase.want)
		}
	}
}

// TestSimpleAggregateBranchRefusalsStay locks the refusal boundary of the
// branch group. The rule must not give a type to a pair that the server
// refuses with Code: 386 (NO_COMMON_TYPE).
func TestSimpleAggregateBranchRefusalsStay(t *testing.T) {
	schema, err := schemaFromDDLErr(t, simpleAggregateBranchSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	// Measured: each of these answers Code: 386 on ClickHouse 25.8.29.51.
	for _, expr := range []string{
		"if(i64 > 0, sagg, s)",
		"if(i64 > 0, sagg, saggf)",
		"if(i64 > 0, sagg, f64)",
		"if(i64 > 0, saggf, i64)",
	} {
		got, err := inferSelectItemCHType(t, schema, "SELECT "+expr+" AS a FROM t")
		if err == nil {
			t.Errorf("%s = %q, want a refusal", expr, got)
		}
	}
}

// TestSimpleAggregateUnaryAndAggregateUnwrap locks the group that
// UNWRAPS. Each result equals the answer for the inner type alone.
func TestSimpleAggregateUnaryAndAggregateUnwrap(t *testing.T) {
	schema, err := schemaFromDDLErr(t, simpleAggregateBranchSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	cases := []struct{ expr, want string }{
		// The unary minus unwraps, and the inner type then follows the
		// ordinary negate rules, the unsigned widening included.
		{"-sagg", "Int64"},
		{"-saggu", "Int16"},
		{"-saggf", "Float64"},
		{"-saggd", "Decimal(38, 4)"},
		{"-saggn", "Nullable(Int64)"},

		// A COMPUTING aggregate reads the value, thus it unwraps.
		{"sum(sagg)", "Int64"},
		{"sum(saggu)", "UInt64"},
		{"sum(saggf)", "Float64"},
		{"sum(saggd)", "Decimal(38, 4)"},
		{"sum(saggn)", "Nullable(Int64)"},
		{"sumIf(sagg, i64 > 0)", "Int64"},
		{"sumIf(saggn, i64 > 0)", "Nullable(Int64)"},
		{"avg(sagg)", "Float64"},
		{"avg(saggf)", "Float64"},
		{"avg(saggd)", "Float64"},
		{"avg(saggn)", "Nullable(Float64)"},
		{"quantile(0.5)(sagg)", "Float64"},

		// NEIGHBOUR CELLS. The plain inner type gives the same answer.
		// These pin that the unwrap is an unwrap and not a new rule.
		{"-i64", "Int64"},
		{"-u8", "Int16"},
		{"sum(i64)", "Int64"},
		{"sum(u8)", "UInt64"},
		{"avg(i64)", "Float64"},
	}
	for _, testCase := range cases {
		got, err := inferSelectItemCHType(t, schema, "SELECT "+testCase.expr+" AS a FROM t")
		if err != nil {
			t.Errorf("%s: error = %v, want %q", testCase.expr, err, testCase.want)
			continue
		}
		if got != testCase.want {
			t.Errorf("%s = %q, want %q", testCase.expr, got, testCase.want)
		}
	}
}

// TestSimpleAggregateValuePreservingAggregatesKeepWrapper is the guard
// against a blanket unwrap. An aggregate that gives a data value back
// KEEPS the wrapper, although it stands in the same argument position as
// sum and avg. A change that unwraps for every aggregate fails here.
func TestSimpleAggregateValuePreservingAggregatesKeepWrapper(t *testing.T) {
	schema, err := schemaFromDDLErr(t, simpleAggregateBranchSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	cases := []struct{ expr, want string }{
		{"max(sagg)", "SimpleAggregateFunction(sum, Int64)"},
		{"min(sagg)", "SimpleAggregateFunction(sum, Int64)"},
		{"any(sagg)", "SimpleAggregateFunction(sum, Int64)"},
		{"anyLast(sagg)", "SimpleAggregateFunction(sum, Int64)"},
		{"argMax(sagg, i64)", "SimpleAggregateFunction(sum, Int64)"},
		{"maxIf(sagg, i64 > 0)", "SimpleAggregateFunction(sum, Int64)"},
		{"min(saggs)", "SimpleAggregateFunction(min, String)"},
		// greatest and least at arity 1 keep the wrapper as well. See
		// greatestLeastNumericInner and greatestLeastKeepsSimpleAggregateCondition.
		{"greatest(sagg)", "SimpleAggregateFunction(sum, Int64)"},
		// A bare column keeps the wrapper.
		{"sagg", "SimpleAggregateFunction(sum, Int64)"},
	}
	for _, testCase := range cases {
		got, err := inferSelectItemCHType(t, schema, "SELECT "+testCase.expr+" AS a FROM t")
		if err != nil {
			t.Errorf("%s: error = %v, want %q", testCase.expr, err, testCase.want)
			continue
		}
		if got != testCase.want {
			t.Errorf("%s = %q, want %q", testCase.expr, got, testCase.want)
		}
	}
}

// TestSimpleAggregateUnaryRefusalsStay locks the refusal boundary of the
// unwrapping group. A non-numeric inner type stays refused, because the
// server refuses it too with Code: 43 (ILLEGAL_TYPE_OF_ARGUMENT).
func TestSimpleAggregateUnaryRefusalsStay(t *testing.T) {
	schema, err := schemaFromDDLErr(t, simpleAggregateBranchSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	for _, expr := range []string{
		"-saggs",
		"sum(saggs)",
		"avg(saggs)",
	} {
		got, err := inferSelectItemCHType(t, schema, "SELECT "+expr+" AS a FROM t")
		if err == nil {
			t.Errorf("%s = %q, want a refusal", expr, got)
		}
	}
}
