package engine

import "testing"

// This file pins the rule for greatest and least over a
// SimpleAggregateFunction(f, T) argument.
//
// Before the rule, chgen gave greatest(sagg, i64) the type
// SimpleAggregateFunction(sum, Int64). The server gives Int64. That was a
// SILENTLY WRONG TYPE, not a refusal: the generated Go field carried the
// wrapper spelling where the server returns the bare inner type.
//
// greatest and least are NOT members of the branch-supertype family,
// although chgen treated them as if they were. The branch family (if,
// multiIf, coalesce) and the aggregate family (max) KEEP the wrapper.
//
// Every expected value below was measured on ClickHouse 25.8.29.51
// through the HTTP interface, against real columns of a real table, never
// over literals, because the server folds constants.
const simpleAggregateGreatestLeastSchema = `
CREATE TABLE t (
    sagg   SimpleAggregateFunction(sum, Int64),
    saggu  SimpleAggregateFunction(max, UInt8),
    saggf  SimpleAggregateFunction(sum, Float64),
    saggn  SimpleAggregateFunction(sum, Nullable(Int64)),
    saggd  SimpleAggregateFunction(sum, Decimal(38, 4)),
    saggs  SimpleAggregateFunction(min, String),
    saggfs SimpleAggregateFunction(min, FixedString(4)),
    saggdt SimpleAggregateFunction(max, DateTime),
    i64    Int64,
    u8     UInt8,
    f64    Float64,
    dec    Decimal(18, 4),
    s      String
);
`

// TestGreatestLeastDropsSimpleAggregateWrapper locks the positions where
// the server DROPS the wrapper. Two or more arguments with a numeric
// inner type give the supertype of the inner types.
//
// Measured on ClickHouse 25.8.29.51, for example:
//
//	SELECT toTypeName(greatest(sagg, i64)) FROM t  ->  Int64
//	SELECT toTypeName(least(sagg, i64)) FROM t     ->  Int64
//	SELECT toTypeName(greatest(sagg, sagg)) FROM t ->  Int64
//
// The values were also selected, not only analysed: for sagg = 5 and
// i64 = 3, greatest(sagg, i64) gives 5 and least(sagg, i64) gives 3.
func TestGreatestLeastDropsSimpleAggregateWrapper(t *testing.T) {
	schema, err := schemaFromDDLErr(t, simpleAggregateGreatestLeastSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	cases := []struct{ expr, want string }{
		{"greatest(sagg, i64)", "Int64"},
		{"least(sagg, i64)", "Int64"},
		{"greatest(sagg, sagg)", "Int64"},
		{"least(sagg, sagg)", "Int64"},
		{"greatest(saggu, u8)", "UInt8"},
		{"greatest(saggu, saggu)", "UInt8"},
		{"greatest(saggf, f64)", "Float64"},
		{"least(saggf, f64)", "Float64"},
		// The inner type can itself be Nullable. The Nullable stays.
		{"greatest(saggn, i64)", "Nullable(Int64)"},
		{"least(saggn, i64)", "Nullable(Int64)"},
		// A Decimal inner type is numeric here.
		{"greatest(saggd, dec)", "Decimal(38, 4)"},
		{"least(saggd, dec)", "Decimal(38, 4)"},
	}
	for _, testCase := range cases {
		got, err := inferSelectItemCHType(t, schema, "SELECT "+testCase.expr+" AS a FROM t")
		if err != nil {
			t.Errorf("%s: error = %v, want %q", testCase.expr, err, testCase.want)
			continue
		}
		if got != testCase.want {
			t.Errorf("%s: CH type = %q, want %q", testCase.expr, got, testCase.want)
		}
	}
}

// TestGreatestLeastKeepsSimpleAggregateWrapper locks the positions where
// the server KEEPS the wrapper. These are the reason the unwrap must stay
// narrow.
//
// ONE argument keeps the wrapper, for a numeric inner type too, because
// the server computes no supertype. A NON-numeric inner type keeps the
// wrapper as well, because the server takes a different path there and
// gives back the first argument type as it is.
//
// Measured on ClickHouse 25.8.29.51, for example:
//
//	SELECT toTypeName(greatest(sagg)) FROM t     ->  SimpleAggregateFunction(sum, Int64)
//	SELECT toTypeName(greatest(saggs, s)) FROM t ->  SimpleAggregateFunction(min, String)
func TestGreatestLeastKeepsSimpleAggregateWrapper(t *testing.T) {
	schema, err := schemaFromDDLErr(t, simpleAggregateGreatestLeastSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	cases := []struct{ expr, want string }{
		// One argument computes no supertype.
		{"greatest(sagg)", "SimpleAggregateFunction(sum, Int64)"},
		{"least(sagg)", "SimpleAggregateFunction(sum, Int64)"},
		{"greatest(saggf)", "SimpleAggregateFunction(sum, Float64)"},
		{"greatest(saggd)", "SimpleAggregateFunction(sum, Decimal(38, 4))"},
		{"greatest(saggs)", "SimpleAggregateFunction(min, String)"},
		// A non-numeric inner type keeps the wrapper.
		{"greatest(saggs, s)", "SimpleAggregateFunction(min, String)"},
		{"least(saggs, s)", "SimpleAggregateFunction(min, String)"},
		{"greatest(saggs, saggs)", "SimpleAggregateFunction(min, String)"},
		{"greatest(saggfs, saggfs)", "SimpleAggregateFunction(min, FixedString(4))"},
		{"greatest(saggdt, saggdt)", "SimpleAggregateFunction(max, DateTime)"},
	}
	for _, testCase := range cases {
		got, err := inferSelectItemCHType(t, schema, "SELECT "+testCase.expr+" AS a FROM t")
		if err != nil {
			t.Errorf("%s: error = %v, want %q", testCase.expr, err, testCase.want)
			continue
		}
		if got != testCase.want {
			t.Errorf("%s: CH type = %q, want %q", testCase.expr, got, testCase.want)
		}
	}
}

// TestGreatestLeastUnwrapStaysNarrow is the guard against a blanket
// unwrap. The branch family, the aggregate family and the bare column
// KEEP the wrapper. If the greatest and least unwrap ever becomes global,
// these cases fail.
//
// Measured on ClickHouse 25.8.29.51:
//
//	SELECT toTypeName(sagg) FROM t             ->  SimpleAggregateFunction(sum, Int64)
//	SELECT toTypeName(if(1, sagg, i64)) FROM t ->  SimpleAggregateFunction(sum, Int64)
//	SELECT toTypeName(max(sagg)) FROM t        ->  SimpleAggregateFunction(sum, Int64)
func TestGreatestLeastUnwrapStaysNarrow(t *testing.T) {
	schema, err := schemaFromDDLErr(t, simpleAggregateGreatestLeastSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	cases := []struct{ expr, want string }{
		{"sagg", "SimpleAggregateFunction(sum, Int64)"},
		{"max(sagg)", "SimpleAggregateFunction(sum, Int64)"},
		{"coalesce(sagg, i64)", "SimpleAggregateFunction(sum, Int64)"},
	}
	for _, testCase := range cases {
		got, err := inferSelectItemCHType(t, schema, "SELECT "+testCase.expr+" AS a FROM t")
		if err != nil {
			t.Errorf("%s: error = %v, want %q", testCase.expr, err, testCase.want)
			continue
		}
		if got != testCase.want {
			t.Errorf("%s: CH type = %q, want %q", testCase.expr, got, testCase.want)
		}
	}
}

// There is no separate helper-level test for the numeric-inner rule any
// more. The rule used to be pinned twice: once through the live path
// above (inferSelectItemCHType, exercising
// greatestLeastKeepsSimpleAggregateCondition in
// wrapper_transport.go), and once through a test-only helper,
// greatestLeastInnerType, that no production code called.
//
// The duplicate had drifted from the shipped rule and gone stale without
// failing: it answered "keeps the wrapper" for a composite inner type
// (Array, Tuple, Map, LowCardinality) at any arity, including arity 1,
// while the shipped rule (fixed for the ticket behind
// TestValuePreservingScalarMarkerCompositeInner and
// TestValuePreservingScalarMarkerOverLowCardinalityInner in
// simple_aggregate_value_preserving_scalar_test.go) correctly
// drops the marker there. A duplicated statement of a measured rule, kept
// alive only by a test that pins the duplicate rather than the shipped
// code, is exactly the situation that hid this drift: the test passed
// while the pinned function was already wrong. The helper and its test
// were removed; the composite-inner cells are covered by the tests named
// above, which exercise the shipped transport directly.
