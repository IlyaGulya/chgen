package engine

import "testing"

// nestedDynamicSchema holds the columns for Nested and Dynamic type tests.
//
// nst_a is the Nested column that the regression is about. at is the SAME
// shape declared explicitly as Array(Tuple(...)); it is the control that
// pins the two forms together, because the server reports one type for
// both. atu is the UNNAMED tuple element form, which must keep its own
// (nameless) rendering.
//
// dyn is the Dynamic column that the regression is about. ni32 is the plain
// Nullable control, and i32 is the bare control.
//
// Every expectation in this file was measured on ClickHouse 25.8.29.51
// through the HTTP interface, against real columns of a real table with
// one row, never over literals, because the server folds constants.
// Every cell was asked as
//
//	SELECT toTypeName(<expr>), ignore(<expr>) FROM t
//
// so that a type which only ANALYSES is separated from one that also
// EXECUTES. toTypeName alone has produced false findings in this project
// before: with dyn seeded as a String instead of an Int32, half of the
// comparison cells below answer Code 43 or Code 386 at execution while
// toTypeName still reports a type.
const nestedDynamicSchema = `
CREATE TABLE t (
    i32 Int32,
    i128 Int128,
    d Date,
    f64 Float64,
    d128 Decimal128(4),
    u8 UInt8,
    ni32 Nullable(Int32),
    s String,
    dyn Dynamic,
    nst_a Nested(a Int32, b String),
    at Array(Tuple(a Int32, b String)),
    atu Array(Tuple(Int32, String))
) ENGINE = MergeTree ORDER BY tuple()
`

func nestedDynamicTestSchema(t *testing.T) *Schema {
	t.Helper()
	schema, err := schemaFromDDLErr(t, nestedDynamicSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	return schema
}

// nestedDynamicCases runs one table of expression and expected-type pairs.
func nestedDynamicCases(t *testing.T, cases []struct{ expr, want string }) {
	t.Helper()
	schema := nestedDynamicTestSchema(t)
	for _, testCase := range cases {
		got, err := combinatorCHType(t, schema, "t", testCase.expr)
		if err != nil {
			t.Errorf("%s: error = %v, want %q", testCase.expr, err, testCase.want)
			continue
		}
		if got != testCase.want {
			t.Errorf("%s = %q, want %q", testCase.expr, got, testCase.want)
		}
	}
}

// TestNestedColumnIsArrayOfTuple pins the regression.
//
// A bare Nested column reference is Array(Tuple(...)) on the server, not
// the bare name Nested. Measured:
//
//	toTypeName(nst_a)   Array(Tuple(a Int32, b String))
//	toTypeName(at)      Array(Tuple(a Int32, b String))
//
// The Nested column and the explicit Array(Tuple(...)) column answer the
// SAME type, thus chgen must hold the same type for both. This is the
// TOO-NARROW direction of the fix: a model that keeps the bare name
// Nested fails here.
func TestNestedColumnIsArrayOfTuple(t *testing.T) {
	nestedDynamicCases(t, []struct{ expr, want string }{
		{"nst_a", "Array(Tuple(a Int32, b String))"},
		{"at", "Array(Tuple(a Int32, b String))"},
		// The UNNAMED element form keeps its own rendering. A fix that
		// forced names onto every tuple would fail this cell.
		{"atu", "Array(Tuple(Int32, String))"},
	})
}

// TestNestedPassesThroughWrappersAsArrayOfTuple pins the wrapper cells of
// the regression. These are the functions the tracking item named. They were never
// wrong themselves: each echoes its argument type, and the argument type
// was wrong one level earlier. Each cell below was measured for BOTH
// nst_a and at, and the two agree in every one.
//
//	argMin(nst_a, d)         Array(Tuple(a Int32, b String))
//	min(nst_a)               Array(Tuple(a Int32, b String))
//	groupArray(nst_a)        Array(Array(Tuple(a Int32, b String)))
//	array(nst_a)             Array(Array(Tuple(a Int32, b String)))
//	arrayElement(nst_a, 1)   Tuple(a Int32, b String)
//	length(nst_a)            UInt64
func TestNestedPassesThroughWrappersAsArrayOfTuple(t *testing.T) {
	nestedDynamicCases(t, []struct{ expr, want string }{
		{"argMin(nst_a, d)", "Array(Tuple(a Int32, b String))"},
		{"argMax(nst_a, d)", "Array(Tuple(a Int32, b String))"},
		{"min(nst_a)", "Array(Tuple(a Int32, b String))"},
		{"max(nst_a)", "Array(Tuple(a Int32, b String))"},
		{"any(nst_a)", "Array(Tuple(a Int32, b String))"},
		{"groupArray(nst_a)", "Array(Array(Tuple(a Int32, b String)))"},
		{"groupUniqArray(nst_a)", "Array(Array(Tuple(a Int32, b String)))"},
		{"array(nst_a)", "Array(Array(Tuple(a Int32, b String)))"},
		{"arrayElement(nst_a, 1)", "Tuple(a Int32, b String)"},
		{"length(nst_a)", "UInt64"},
		// The explicit Array(Tuple(...)) control. Every one of these
		// answered the same type on the server as its nst_a twin
		// above, thus a fix that changed only the Nested path and
		// drifted from the Array(Tuple(...)) path would fail here.
		{"argMin(at, d)", "Array(Tuple(a Int32, b String))"},
		{"min(at)", "Array(Tuple(a Int32, b String))"},
		{"groupArray(at)", "Array(Array(Tuple(a Int32, b String)))"},
		{"array(at)", "Array(Array(Tuple(a Int32, b String)))"},
	})
}

// TestDynamicForcesNullableOnComparisonAndHash pins the regression.
//
// A Dynamic operand puts a Nullable wrapper on the fixed result of a
// comparison or a hash. Measured in BOTH operand positions, paired with
// ignore(...), with dyn holding CAST(3 AS Int32):
//
//	equals(dyn, i32)   Nullable(UInt8)    equals(i32, dyn)   Nullable(UInt8)
//	greater(dyn, i128) Nullable(UInt8)    cityHash64(dyn)    Nullable(UInt64)
//	hex(dyn)           Nullable(String)
//
// This is the TOO-NARROW direction: a rule that keeps the bare UInt8
// fails here.
func TestDynamicForcesNullableOnComparisonAndHash(t *testing.T) {
	nestedDynamicCases(t, []struct{ expr, want string }{
		{"equals(dyn, i32)", "Nullable(UInt8)"},
		{"notEquals(dyn, i32)", "Nullable(UInt8)"},
		{"less(dyn, i32)", "Nullable(UInt8)"},
		{"lessOrEquals(dyn, i32)", "Nullable(UInt8)"},
		{"greater(dyn, i128)", "Nullable(UInt8)"},
		{"greaterOrEquals(dyn, i128)", "Nullable(UInt8)"},
		// The Dynamic in the SECOND position forces the wrapper too.
		{"equals(i32, dyn)", "Nullable(UInt8)"},
		{"greater(i32, dyn)", "Nullable(UInt8)"},
		{"cityHash64(dyn)", "Nullable(UInt64)"},
		{"hex(dyn)", "Nullable(String)"},
		{"concat(dyn, i32)", "Nullable(String)"},
	})
}

// TestDynamicNullableDoesNotWidenNeighbours is the TOO-WIDE direction of
// the regression. It is a separate test from the one above on purpose: a fix
// that wrapped everything would PASS the test above and fail only here.
//
// Three neighbour groups must keep the bare type. All measured with
// ignore(...):
//
//  1. The same functions with NO Dynamic operand:
//     equals(i32, i32) UInt8, cityHash64(i32) UInt64, hex(i32) String.
//
//  2. The CONVERSIONS over a Dynamic. A conversion names its target type
//     and consumes the Dynamic: toString(dyn) String, toInt64(dyn)
//     Int64, toDate(dyn) Date, toFloat64(dyn) Float64. A rule that
//     treated a Dynamic argument as a Nullable argument for every
//     wrapperTransparent function would fail these, because toString and
//     equals are the same class.
//
//  3. The type-PASSTHROUGH functions, which keep the bare Dynamic. A
//     Dynamic cannot go inside Nullable at all (toNullable(dyn) is
//     Code 43, "Nested type Dynamic cannot be inside Nullable"), thus a
//     general flag would produce the impossible Nullable(Dynamic):
//     identity(dyn) Dynamic, abs(dyn) Dynamic, any(dyn) Dynamic,
//     argMin(dyn, d) Dynamic, coalesce(dyn, i32) Dynamic (NOT Int32).
func TestDynamicNullableDoesNotWidenNeighbours(t *testing.T) {
	nestedDynamicCases(t, []struct{ expr, want string }{
		// 1. No Dynamic operand: the wrapper must not appear.
		{"equals(i32, i32)", "UInt8"},
		{"notEquals(i32, i32)", "UInt8"},
		{"greater(i32, i32)", "UInt8"},
		{"cityHash64(i32)", "UInt64"},
		{"hex(i32)", "String"},
		{"concat(i32, i32)", "String"},
		{"xor(i32, i32)", "UInt8"},
		// The plain Nullable operand keeps working through the rule
		// that already existed.
		{"equals(ni32, i32)", "Nullable(UInt8)"},
		{"cityHash64(ni32)", "Nullable(UInt64)"},
		// 2. Conversions over a Dynamic stay bare.
		{"toString(dyn)", "String"},
		{"toInt64(dyn)", "Int64"},
		{"toFloat64(dyn)", "Float64"},
		// 3. Passthrough functions keep the bare Dynamic. identity is
		// not in chgen's registry, thus it refuses rather than
		// answering a type; a refusal is not this test's subject, so
		// the passthrough direction is covered by any and argMin.
		{"any(dyn)", "Dynamic"},
		{"argMin(dyn, d)", "Dynamic"},
	})
}

// TestDynamicNullablePropagatesThroughNestedCalls pins the propagation
// half of the regression. The tracking item reported the propagation as part of the
// defect. It needs NO rule of its own: once the Nullable is on the inner
// result, the ordinary Nullable machinery carries it. This test proves
// that claim rather than assuming it. Measured:
//
//	array(equals(dyn, d128))              Array(Nullable(UInt8))
//	not(equals(dyn, d128))                Nullable(UInt8)
//	sum(equals(dyn, d128))                Nullable(UInt64)
//	equals(abs(i128), equals(dyn, d128))  Nullable(UInt8)
func TestDynamicNullablePropagatesThroughNestedCalls(t *testing.T) {
	nestedDynamicCases(t, []struct{ expr, want string }{
		{"array(equals(dyn, d128))", "Array(Nullable(UInt8))"},
		{"not(equals(dyn, d128))", "Nullable(UInt8)"},
		{"sum(equals(dyn, d128))", "Nullable(UInt64)"},
		{"equals(abs(i128), equals(dyn, d128))", "Nullable(UInt8)"},
	})
}
