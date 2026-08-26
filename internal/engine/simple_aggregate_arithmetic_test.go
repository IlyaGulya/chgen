package engine

import "testing"

// This file pins the rule for a SimpleAggregateFunction(f, T) operand.
//
// Before the rule, chgen refused every arithmetic form of such a column
// with "cannot infer result type for SimpleAggregateFunction(sum, Int64)
// + UInt8". That was a FALSE refusal: the server runs the query and
// answers a type. A false refusal breaks a query that works.
//
// Every expected value below was measured on ClickHouse 25.8.29.51
// through the HTTP interface, against real columns of a real table, never
// over literals, because the server folds constants.
const simpleAggregateArithmeticSchema = `
CREATE TABLE t (
    sagg  SimpleAggregateFunction(sum, Int64),
    nsagg SimpleAggregateFunction(sum, Nullable(Int64)),
    saggf SimpleAggregateFunction(sum, Float64),
    saggs SimpleAggregateFunction(min, String),
    sagga SimpleAggregateFunction(groupArrayArray, Array(Int64)),
    i64   Int64,
    u8    UInt8,
    arr   Array(Int64),
    s     String
);
`

// TestSimpleAggregateArithmeticUnwrapsInnerType locks the positions that
// unwrap. Each result is the answer for the inner type alone.
func TestSimpleAggregateArithmeticUnwrapsInnerType(t *testing.T) {
	schema, err := schemaFromDDLErr(t, simpleAggregateArithmeticSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	cases := []struct{ expr, want string }{
		{"sagg + u8", "Int64"},
		{"sagg - i64", "Int64"},
		{"sagg / i64", "Float64"},
		{"sagg % i64", "Int64"},
		{"sagg + sagg", "Int64"},
		{"u8 - sagg", "Int64"},
		{"i64 / sagg", "Float64"},
		{"saggf * i64", "Float64"},
		{"saggf - sagg", "Float64"},
		// The inner type can itself be Nullable. The unwrap must come
		// before the Nullable rule, so that the Nullable is seen.
		{"nsagg + i64", "Nullable(Int64)"},
		{"nsagg / i64", "Nullable(Float64)"},
		{"nsagg % i64", "Nullable(Int64)"},
		// The inner type can be an Array. The array rule then applies.
		{"sagga + arr", "Array(Int64)"},
		{"sagga / i64", "Array(Float64)"},
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

// TestSimpleAggregateComparisonAfterArithmetic checks the composed form
// that the fuzz oracle found. The server answers UInt8 and gives the value
// 1 for sagg = 5 and i64 = 3; chgen answers UInt8 too, because the
// comparison result is a predicate and the predicate family always
// answers UInt8. What matters is that the operand no longer refuses.
func TestSimpleAggregateComparisonAfterArithmetic(t *testing.T) {
	schema, err := schemaFromDDLErr(t, simpleAggregateArithmeticSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	got, err := inferSelectItemCHType(t, schema, "SELECT (sagg - i64) > 0 AS a FROM t")
	if err != nil {
		t.Fatalf("(sagg - i64) > 0: error = %v", err)
	}
	if got != "UInt8" {
		t.Errorf("(sagg - i64) > 0: CH type = %q, want %q", got, "UInt8")
	}
}

// TestSimpleAggregateKeepsWrapperOutsideArithmetic is the guard against a
// blanket unwrap. Measured on ClickHouse 25.8.29.51 with the same real
// columns, these positions KEEP the wrapper. If the unwrap ever becomes
// global, these cases fail.
func TestSimpleAggregateKeepsWrapperOutsideArithmetic(t *testing.T) {
	schema, err := schemaFromDDLErr(t, simpleAggregateArithmeticSchema)
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

// TestSimpleAggregateArithmeticKeepsRefusals checks that the unwrap does
// not widen the refusal boundary. The inner rule refuses these pairs and
// the server refuses them too. Measured on ClickHouse 25.8.29.51:
// sagg + saggs answers Code: 43, "Illegal types
// SimpleAggregateFunction(sum, Int64) and
// SimpleAggregateFunction(min, String) of arguments of function plus".
func TestSimpleAggregateArithmeticKeepsRefusals(t *testing.T) {
	schema, err := schemaFromDDLErr(t, simpleAggregateArithmeticSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	for _, expr := range []string{"sagg + saggs", "sagg + s"} {
		got, err := inferSelectItemCHType(t, schema, "SELECT "+expr+" AS a FROM t")
		if err == nil {
			t.Errorf("%s: CH type = %q, want a refusal", expr, got)
		}
	}
}

// TestSimpleAggregateInnerTypeHelper checks the helper itself. Only a
// SimpleAggregateFunction with exactly two arguments unwraps; every other
// type passes through unchanged.
func TestSimpleAggregateInnerTypeHelper(t *testing.T) {
	cases := []struct{ in, want string }{
		{"SimpleAggregateFunction(sum, Int64)", "Int64"},
		{"SimpleAggregateFunction(sum, Nullable(Int64))", "Nullable(Int64)"},
		{"Int64", "Int64"},
		{"Nullable(Int64)", "Nullable(Int64)"},
		{"AggregateFunction(uniq, UInt64)", "AggregateFunction(uniq, UInt64)"},
		{"Array(Int64)", "Array(Int64)"},
	}
	for _, testCase := range cases {
		parsed, err := parseCHTypeName(testCase.in)
		if err != nil {
			t.Fatalf("parseCHTypeName(%q) error = %v", testCase.in, err)
		}
		if got := simpleAggregateInnerType(parsed).String(); got != testCase.want {
			t.Errorf("simpleAggregateInnerType(%q) = %q, want %q", testCase.in, got, testCase.want)
		}
	}
}
