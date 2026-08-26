package engine

import (
	"strings"
	"testing"
)

// containerRefusalSchema holds the column shapes that the wrapper grid
// uses for the cells of the regression: an Array under a
// SimpleAggregateFunction marker, a plain Array, and an Array of
// LowCardinality(String).
//
// The plain Array column is the CONTROL. The ticket named the marker as
// the cause. The server refuses the plain Array in the same way, thus
// the marker is not the cause and the control must stay in the test.
const containerRefusalSchema = `
CREATE TABLE probe (
    k UInt8,
    arr_i32 Array(Int32),
    mp Map(String, Int32),
    tup Tuple(Int32, Int32),
    s String,
    i32 Int32,
    safarr_i32 SimpleAggregateFunction(anyLast, Array(Int32)),
    arr_s Array(String),
    lcarr Array(LowCardinality(String))
) ENGINE = MergeTree ORDER BY k
`

func containerRefusalTestSchema(t *testing.T) *Schema {
	t.Helper()
	schema, err := schemaFromDDLErr(t, containerRefusalSchema)
	if err != nil {
		t.Fatalf("parse probe schema: %v", err)
	}
	return schema
}

// TestScalarFunctionRefusesAContainerArgument covers cause 1 of
// the regression: the toXxx conversions, hex and nullIf give a type to a
// container argument although the server answers Code: 43.
//
// Measured on ClickHouse 25.8.29.51 with a VALUE select. toTypeName
// LIES about these calls: "SELECT toTypeName(toDate(arr_i32))" answers
// Date, while "SELECT toDate(arr_i32)" is Code: 43.
//
// The marker column and the PLAIN Array column are both here. They must
// behave alike, because the server refuses both with the same message,
// which names Array(Int32) in each case.
func TestScalarFunctionRefusesAContainerArgument(t *testing.T) {
	schema := containerRefusalTestSchema(t)
	refused := []string{
		// Server: Code: 43, "Illegal type Array(Int32) of argument
		// of function hex".
		"hex(arr_i32)",
		"hex(safarr_i32)",
		"hex(mp)",
		"hex(tup)",
		// Server: Code: 43, "Illegal type Array(Int32) of argument
		// of function toDate".
		"toDate(arr_i32)",
		"toDate(safarr_i32)",
		"toDate32(safarr_i32)",
		"toDateTime(safarr_i32)",
		"toDateTime64(safarr_i32, 2)",
		"toInt8(arr_i32)",
		"toInt8(safarr_i32)",
		"toInt16(safarr_i32)",
		"toInt32(safarr_i32)",
		"toInt64(safarr_i32)",
		"toUInt8(safarr_i32)",
		"toUInt16(safarr_i32)",
		"toUInt32(safarr_i32)",
		"toUInt64(safarr_i32)",
		"toFloat32(safarr_i32)",
		"toFloat64(safarr_i32)",
		"toInt8(mp)",
		"toInt8(tup)",
		// Server: Code: 43, "Nested type Array(Int32) cannot be
		// inside Nullable type".
		"nullIf(arr_i32, arr_i32)",
		"nullIf(safarr_i32, safarr_i32)",
		"nullIf(mp, mp)",
		"nullIf(tup, tup)",
	}
	for _, exprSQL := range refused {
		inferred, err := inferTestExprType(t, schema, exprSQL)
		if err == nil {
			t.Errorf("%s: chgen answered %s, but the server refuses the call with Code: 43", exprSQL, inferred)
		}
	}
}

// TestScalarFunctionAcceptsAScalarArgument is the other half of cause 1.
// A refusal wider than the server's would break a query that runs, thus
// every scalar must keep its type.
//
// Codes 6, 38 and 41 in the sweep are VALUE errors and not type
// evidence: the server accepted the String and then could not parse the
// value. The String therefore stays in the domain.
func TestScalarFunctionAcceptsAScalarArgument(t *testing.T) {
	schema := containerRefusalTestSchema(t)
	accepted := map[string]string{
		"hex(s)":       "String",
		"hex(i32)":     "String",
		"toInt8(i32)":  "Int8",
		"toInt8(s)":    "Int8",
		"toDate(i32)":  "Date",
		"toFloat64(s)": "Float64",
	}
	for exprSQL, want := range accepted {
		inferred, err := inferTestExprType(t, schema, exprSQL)
		if err != nil {
			t.Errorf("%s: chgen refused a call that the server runs: %v", exprSQL, err)
			continue
		}
		if inferred != want {
			t.Errorf("%s: chgen answered %s, want %s", exprSQL, inferred, want)
		}
	}
}

// TestNumericConstantDoesNotFoldIntoAStringColumn covers cause 2 of
// the regression: the eight higher-order array cells over
// Array(LowCardinality(String)).
//
// The lambda body "x > 1" compares a String element with a numeric
// constant. chgen exempted every pair that holds a constant, thus it
// gave the call a type. The server answers Code: 386.
//
// The exemption is DIRECTIONAL. Measured on ClickHouse 25.8.29.51 with
// a VALUE select:
//
//	s   > 1     Code: 386   no supertype for String, UInt8
//	i32 > '1'   1           a String constant folds
//
// The plain Array(String) column is the CONTROL: the server refuses it
// in the same way, thus the LowCardinality element is not the cause.
func TestNumericConstantDoesNotFoldIntoAStringColumn(t *testing.T) {
	schema := containerRefusalTestSchema(t)
	refused := []string{
		// Server: Code: 386, "There is no supertype for types
		// String, UInt8".
		"arrayCount(x -> x > 1, lcarr)",
		"arrayExists(x -> x > 1, lcarr)",
		"arrayAll(x -> x > 1, lcarr)",
		"arrayFilter(x -> x > 1, lcarr)",
		"arrayMap(x -> x > 1, lcarr)",
		"arrayMin(x -> x > 1, lcarr)",
		"arrayMax(x -> x > 1, lcarr)",
		"arraySort(x -> x > 1, lcarr)",
		// The control: a plain Array(String) fails the same way.
		"arrayCount(x -> x > 1, arr_s)",
		// The same pair outside a lambda.
		"s > 1",
	}
	for _, exprSQL := range refused {
		inferred, err := inferTestExprType(t, schema, exprSQL)
		if err == nil {
			t.Errorf("%s: chgen answered %s, but the server refuses the call with Code: 386", exprSQL, inferred)
		}
	}
}

// TestStringConstantStillFolds guards the other direction of cause 2. A
// String constant folds into every column type, thus these calls must
// keep their type. Without this guard the fix could refuse a query that
// the server runs.
//
// Measured on ClickHouse 25.8.29.51 with a VALUE select: "i32 > '1'" is
// 1, and "arrayCount(x -> x > 'b', lcarr)" is 0.
func TestStringConstantStillFolds(t *testing.T) {
	schema := containerRefusalTestSchema(t)
	accepted := []string{
		"i32 > '1'",
		"s > 'b'",
		"arrayCount(x -> x > 'b', lcarr)",
		"arrayCount(x -> x > 1, arr_i32)",
	}
	for _, exprSQL := range accepted {
		if _, err := inferTestExprType(t, schema, exprSQL); err != nil {
			t.Errorf("%s: chgen refused a call that the server runs: %v", exprSQL, err)
		}
	}
}

// TestHasRefusesANeedleThatCannotMatchTheElement covers cause 3 of
// the regression. has(haystack, needle) compares the ELEMENT of the haystack
// with the needle. hasArgumentDomain judges the haystack alone and the
// result is a fixed Bool, thus chgen answered Bool for a call that the
// server refuses.
//
// Measured on ClickHouse 25.8.29.51 with a VALUE select:
//
//	has(arr_i32, i32)       0
//	has(arr_i32, arr_i32)   Code: 386   no supertype for Int32,
//	                                    Array(Int32)
func TestHasRefusesANeedleThatCannotMatchTheElement(t *testing.T) {
	schema := containerRefusalTestSchema(t)
	refused := []string{
		"has(arr_i32, arr_i32)",
		"has(safarr_i32, safarr_i32)",
		"has(arr_s, arr_s)",
		"has(arr_s, i32)",
	}
	for _, exprSQL := range refused {
		inferred, err := inferTestExprType(t, schema, exprSQL)
		if err == nil {
			t.Errorf("%s: chgen answered %s, but the server refuses the call with Code: 386", exprSQL, inferred)
		}
	}
}

// TestHasAcceptsAMatchingNeedle guards the other side of cause 3. A
// needle that shares a comparable family with the element must keep its
// UInt8 predicate result, and a Map haystack must stay untouched, because
// the sweep covered the Array haystack only.
func TestHasAcceptsAMatchingNeedle(t *testing.T) {
	schema := containerRefusalTestSchema(t)
	accepted := []string{
		"has(arr_i32, i32)",
		"has(arr_i32, 1)",
		"has(arr_s, s)",
		"has(mp, 'a')",
	}
	for _, exprSQL := range accepted {
		inferred, err := inferTestExprType(t, schema, exprSQL)
		if err != nil {
			t.Errorf("%s: chgen refused a call that the server runs: %v", exprSQL, err)
			continue
		}
		if !strings.EqualFold(inferred, "UInt8") {
			t.Errorf("%s: chgen answered %s, want UInt8", exprSQL, inferred)
		}
	}
}
