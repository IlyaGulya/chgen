package engine

import "testing"

// This file pins the rule for an AggregateFunction(f, T) state operand of
// arithmetic. Before the rule in this file, chgen had two defects on the
// same cell, found by the type oracle gate:
//
//  1. False refusal was not the bug here; the bug ran the other way. chgen
//     answered a type for every arithmetic form of a state, including the
//     ones the server refuses with code 43 (agg + 2, agg % 2, and so on).
//  2. Where the expression IS legal (groupArray(agg) * 2), chgen answered
//     Array(UInt64): it stripped the AggregateFunction wrapper off the
//     array element.
//
// "state * N" is not arithmetic on the values a state has accumulated. It
// is the ClickHouse idiom that builds N COPIES of the state, thus only a
// count known at PLAN TIME is meaningful, and a column can never be one.
//
// Every expected value below was measured on ClickHouse 25.8.29.51 through
// the HTTP interface, against real columns of a real AggregatingMergeTree
// table, never over a literal alone paired with another literal, because
// the server folds constants.
const aggregateStateArithmeticSchema = `
CREATE TABLE probe (
    agg  AggregateFunction(uniq, UInt64),
    sagg SimpleAggregateFunction(sum, Int64),
    i32  Int32,
    u8   UInt8
) ENGINE = AggregatingMergeTree ORDER BY tuple();
`

// TestAggregateStateReplicationByConstant pins the ACCEPTED cells: a state
// multiplied by a non-negative integer constant of an unsigned type no
// wider than UInt64, the state on either side, and an Array of the state
// nested to any depth. The element type of the Array form must still
// carry the AggregateFunction wrapper; this is the SECOND defect in
// the regression and each Array case below pins the full element type, not
// only the outer Array shape.
func TestAggregateStateReplicationByConstant(t *testing.T) {
	schema, err := schemaFromDDLErr(t, aggregateStateArithmeticSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	cases := []struct{ expr, want string }{
		{"agg * 2", "AggregateFunction(uniq, UInt64)"},
		{"agg * 0", "AggregateFunction(uniq, UInt64)"},
		{"agg * toUInt8(2)", "AggregateFunction(uniq, UInt64)"},
		{"2 * agg", "AggregateFunction(uniq, UInt64)"},
		{"toUInt8(2) * agg", "AggregateFunction(uniq, UInt64)"},
		{"agg * toUInt16(2)", "AggregateFunction(uniq, UInt64)"},
		{"agg * toUInt32(2)", "AggregateFunction(uniq, UInt64)"},
		{"agg * toUInt64(2)", "AggregateFunction(uniq, UInt64)"},
		{"agg * 256", "AggregateFunction(uniq, UInt64)"},
	}
	for _, testCase := range cases {
		got, err := inferSelectItemCHType(t, schema, "SELECT "+testCase.expr+" AS a FROM probe")
		if err != nil {
			t.Errorf("%s: error = %v, want %q", testCase.expr, err, testCase.want)
			continue
		}
		if got != testCase.want {
			t.Errorf("%s: CH type = %q, want %q", testCase.expr, got, testCase.want)
		}
	}
}

// TestAggregateStateReplicationRefusesEveryOtherForm pins the REFUSED
// cells. Each one must return an error: a wrong type here is the more
// dangerous defect, because a wrong answer gives the user generated Go
// for a query the server will not run.
func TestAggregateStateReplicationRefusesEveryOtherForm(t *testing.T) {
	schema, err := schemaFromDDLErr(t, aggregateStateArithmeticSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	exprs := []string{
		"agg + 2",
		"agg - 2",
		"agg / 2",
		"agg % 2",
		"intDiv(agg, 2)",
		"agg * 2.5",
		"agg * -2",
		"agg * '2'",
		"agg * u8",           // a COLUMN, not a constant: code 44, not 43
		"agg * toInt32(2)",   // a SIGNED constant type: code 43
		"agg * toUInt256(2)", // wider than UInt64: code 43
		"groupArray(agg) * i32",
		"groupArray(agg) * groupArray(agg)",
		"groupArray(agg) + 2",
		"groupArray(agg) - 2",
		"groupArray(agg) / 2",
		"groupArray(agg) % 2",
		"groupArray(agg) * 2.5",
		"-agg",
		"-groupArray(agg)",
		"abs(groupArray(agg))",
	}
	for _, expr := range exprs {
		got, err := inferSelectItemCHType(t, schema, "SELECT "+expr+" AS a FROM probe")
		if err == nil {
			t.Errorf("%s: CH type = %q, want an error (the server refuses this expression)", expr, got)
		}
	}
}

// TestAggregateStateArrayReplicationKeepsElementWrapper pins the second
// defect directly: the element type of an Array(AggregateFunction(...))
// after "* N" must still carry the AggregateFunction wrapper, never
// Array(UInt64). Also covers Array nested two deep and the state on the
// left and on the right of the array form.
func TestAggregateStateArrayReplicationKeepsElementWrapper(t *testing.T) {
	schema, err := schemaFromDDLErr(t, aggregateStateArithmeticSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	cases := []struct{ expr, want string }{
		{"groupArray(agg) * 2", "Array(AggregateFunction(uniq, UInt64))"},
		{"groupArray(agg) * 3", "Array(AggregateFunction(uniq, UInt64))"},
		{"2 * groupArray(agg)", "Array(AggregateFunction(uniq, UInt64))"},
		{"groupArray(agg) * toUInt8(2)", "Array(AggregateFunction(uniq, UInt64))"},
	}
	for _, testCase := range cases {
		got, err := inferSelectItemCHType(t, schema, "SELECT "+testCase.expr+" AS a FROM probe")
		if err != nil {
			t.Errorf("%s: error = %v, want %q", testCase.expr, err, testCase.want)
			continue
		}
		if got != testCase.want {
			t.Errorf("%s: CH type = %q, want %q (must keep the AggregateFunction wrapper, not strip it to UInt64)", testCase.expr, got, testCase.want)
		}
	}
}

// TestDate32AndDateTime64HaveNoModuloRule pins a SECOND, unrelated defect
// that the type oracle gate found on the SAME "%" arithmetic path while
// this tracking item was under review: chgen answered a type for "Date32 % N" and
// "DateTime64 % N", where the server has NO modulo rule for these two
// types at all (only Date and DateTime have one). The gate found this as
// "agg-arith:%: chgen=Array(UInt64)" through
// "groupArray(d32) % max(u64)", because the Array branch recurses through
// the same scalar "%" rule for its element type.
//
// Measured on ClickHouse 25.8.29.51 with real columns of a MergeTree
// table (d Date, d32 Date32, dt DateTime, dt64 DateTime64(3), u64 UInt64):
//
//	d % u64                        UInt64          <- ACCEPTED
//	dt % u64                       UInt64          <- ACCEPTED
//	groupArray(d) % max(u64)       Array(UInt64)   <- ACCEPTED
//	d32 % u64                      code 43         <- REFUSED
//	dt64 % u64                     code 43         <- REFUSED
//	groupArray(d32) % max(u64)     code 43         <- REFUSED
func TestDate32AndDateTime64HaveNoModuloRule(t *testing.T) {
	schema, err := schemaFromDDLErr(t, `
CREATE TABLE t (
    d    Date,
    d32  Date32,
    dt   DateTime,
    dt64 DateTime64(3),
    u64  UInt64
);
`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	acceptedCases := []struct{ expr, want string }{
		{"d % u64", "UInt64"},
		{"dt % u64", "UInt64"},
		{"groupArray(d) % max(u64)", "Array(UInt64)"},
	}
	for _, testCase := range acceptedCases {
		got, err := inferSelectItemCHType(t, schema, "SELECT "+testCase.expr+" AS a FROM t")
		if err != nil {
			t.Errorf("%s: error = %v, want %q", testCase.expr, err, testCase.want)
			continue
		}
		if got != testCase.want {
			t.Errorf("%s: CH type = %q, want %q", testCase.expr, got, testCase.want)
		}
	}
	refusedExprs := []string{
		"d32 % u64",
		"dt64 % u64",
		"groupArray(d32) % max(u64)",
		"groupArray(dt64) % max(u64)",
	}
	for _, expr := range refusedExprs {
		got, err := inferSelectItemCHType(t, schema, "SELECT "+expr+" AS a FROM t")
		if err == nil {
			t.Errorf("%s: CH type = %q, want an error (the server has no modulo rule for Date32 or DateTime64)", expr, got)
		}
	}
}

// TestSimpleAggregateArithmeticStillUnwraps re-runs a slice of the
// neighbouring SimpleAggregateFunction rule (see
// simple_aggregate_arithmetic_test.go) against the schema in THIS file, to
// show that the new AggregateFunction state rule did not change the
// SimpleAggregateFunction path: SimpleAggregateFunction is an ordinary
// value and unwraps to its inner type under every operator, unlike an
// AggregateFunction state.
func TestSimpleAggregateArithmeticStillUnwraps(t *testing.T) {
	schema, err := schemaFromDDLErr(t, aggregateStateArithmeticSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	cases := []struct{ expr, want string }{
		{"sagg + u8", "Int64"},
		{"sagg * 2", "Int64"},
		{"sagg % 2", "Int16"},
	}
	for _, testCase := range cases {
		got, err := inferSelectItemCHType(t, schema, "SELECT "+testCase.expr+" AS a FROM probe")
		if err != nil {
			t.Errorf("%s: error = %v, want %q", testCase.expr, err, testCase.want)
			continue
		}
		if got != testCase.want {
			t.Errorf("%s: CH type = %q, want %q", testCase.expr, got, testCase.want)
		}
	}
}
