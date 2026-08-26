package engine

import (
	"strings"
	"testing"
)

const dynamicXORBoundarySchema = `
CREATE TABLE probe (
    i8 Int8,
    i16 Int16,
    i32 Int32,
    i64 Int64,
    u8 UInt8,
    u16 UInt16,
    u32 UInt32,
    u64 UInt64,
    i128 Int128,
    u128 UInt128,
    f32 Float32,
    f64 Float64,
    dec Decimal(18, 4),
    b Bool,
    s String,
    ni32 Nullable(Int32),
    dyn Dynamic
) ENGINE = MergeTree ORDER BY tuple()
`

func dynamicXORTestSchema(t *testing.T) *Schema {
	t.Helper()
	schema, err := schemaFromDDLErr(t, dynamicXORBoundarySchema)
	if err != nil {
		t.Fatalf("parse the Dynamic xor schema: %v", err)
	}
	return schema
}

// TestXORAcceptsDynamicWithMeasuredNumericNeighbors checks the accepted
// Dynamic boundary. ClickHouse 25.8.29.51 gives Nullable(UInt8) for a real
// Dynamic column with every integer of at most 64 bits, both floats, Bool,
// Nullable(Int32), another Dynamic column and both operand orders.
func TestXORAcceptsDynamicWithMeasuredNumericNeighbors(t *testing.T) {
	schema := dynamicXORTestSchema(t)
	for _, expression := range []string{
		"xor(dyn, i8)", "xor(dyn, i16)", "xor(dyn, i32)", "xor(dyn, i64)",
		"xor(dyn, u8)", "xor(dyn, u16)", "xor(dyn, u32)", "xor(dyn, u64)",
		"xor(i32, dyn)", "xor(dyn, f32)", "xor(dyn, f64)",
		"xor(dyn, ni32)", "xor(dyn, dyn)",
	} {
		inferred, err := inferTestExprType(t, schema, expression)
		if err != nil {
			t.Errorf("%s: want Nullable(UInt8), got refusal %v", expression, err)
		} else if inferred != "Nullable(UInt8)" {
			t.Errorf("%s: want Nullable(UInt8), got %s", expression, inferred)
		}
	}

	for _, testCase := range []struct {
		expression string
		want       string
	}{
		{"xor(dyn, b)", "Nullable(UInt8)"},
		{"xor(i32, i64)", "UInt8"},
		{"xor(b, i32)", "Bool"},
		{"xor(f32, i32)", "UInt8"},
		{"ifNull(xor(dyn, i32), u8)", "UInt8"},
		{"nullIf(xor(dyn, i32), u8)", "Nullable(UInt8)"},
		{"not(xor(dyn, i32))", "Nullable(UInt8)"},
	} {
		inferred, err := inferTestExprType(t, schema, testCase.expression)
		if err != nil {
			t.Errorf("%s: want %s, got refusal %v", testCase.expression, testCase.want, err)
		} else if inferred != testCase.want {
			t.Errorf("%s: want %s, got %s", testCase.expression, testCase.want, inferred)
		}
	}
}

// TestDynamicDoesNotWidenOtherLogicDomains checks the refused side. xor
// still refuses wide integers, Decimal and String. and and or still refuse
// Dynamic. The unary not function keeps its separately measured support.
func TestDynamicDoesNotWidenOtherLogicDomains(t *testing.T) {
	schema := dynamicXORTestSchema(t)
	for _, expression := range []string{
		"xor(dyn, i128)",
		"xor(dyn, u128)",
		"xor(dyn, dec)",
		"xor(dyn, s)",
		"and(dyn, i32)",
		"or(dyn, i32)",
	} {
		if inferred, err := inferTestExprType(t, schema, expression); err == nil {
			t.Errorf("%s: want a refusal, got %s", expression, inferred)
		} else if !strings.Contains(err.Error(), "does not accept") {
			t.Errorf("%s: want a logic-domain refusal, got %v", expression, err)
		}
	}

	for _, expression := range []string{"not(dyn)", "NOT dyn"} {
		inferred, err := inferTestExprType(t, schema, expression)
		if err != nil {
			t.Errorf("%s: want Nullable(UInt8), got refusal %v", expression, err)
		} else if inferred != "Nullable(UInt8)" {
			t.Errorf("%s: want Nullable(UInt8), got %s", expression, inferred)
		}
	}
}
