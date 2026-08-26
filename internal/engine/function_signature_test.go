package engine

import (
	"strings"
	"testing"
)

func TestFunctionSignatureRefusesWrongArity(t *testing.T) {
	schema := schemaFromDDL(t, "CREATE TABLE probe (i8 Int8) ENGINE = Memory")
	_, err := inferTestExprType(t, schema, "abs(i8, i8)")
	if err == nil {
		t.Fatal("inference accepted abs with two arguments")
	}
	if !strings.Contains(err.Error(), "accepts 1 argument") {
		t.Fatalf("wrong refusal: %v", err)
	}
}

func TestFunctionSignatureChecksLinkedArgumentTypes(t *testing.T) {
	schema := schemaFromDDL(t, `CREATE TABLE probe (
		i8 Int8, i64 Int64, arr Array(Int8)
	) ENGINE = Memory`)
	if _, err := inferTestExprType(t, schema, "lagInFrame(i8, 1, i64) OVER ()"); err == nil {
		t.Fatal("inference accepted a window default that changes the value type")
	}
	got, err := inferTestExprType(t, schema, "arrayResize(arr, 2, i64)")
	if err != nil {
		t.Fatal(err)
	}
	if got != "Array(Int64)" {
		t.Fatalf("arrayResize widening = %s, want Array(Int64)", got)
	}
}

func TestFunctionSignatureChecksAggregateParametersAndPlacement(t *testing.T) {
	schema := schemaFromDDL(t, "CREATE TABLE probe (i8 Int8) ENGINE = Memory")
	if _, err := inferTestExprType(t, schema, "quantile(0.5)(i8)"); err != nil {
		t.Fatal(err)
	}
	for _, expression := range []string{
		"quantile(2)(i8)",
		"uniqCombined(1)(i8)",
		"abs(i8) OVER ()",
	} {
		if _, err := inferTestExprType(t, schema, expression); err == nil {
			t.Errorf("inference accepted %s", expression)
		}
	}
}

// TestFunctionSignatureRefusesBareWindowOnlyCalls holds the measured
// placement boundary. ClickHouse accepts each call with OVER () and refuses
// the same call without OVER. first_value and last_value are not in this
// table because ClickHouse also exposes them as bare aggregate aliases.
func TestFunctionSignatureRefusesBareWindowOnlyCalls(t *testing.T) {
	schema := schemaFromDDL(t, "CREATE TABLE probe (i8 Int8) ENGINE = Memory")
	for _, function := range []string{
		"row_number()",
		"rank()",
		"dense_rank()",
		"lagInFrame(i8)",
		"leadInFrame(i8)",
	} {
		if _, err := inferTestExprType(t, schema, function); err == nil {
			t.Errorf("inference accepted bare window-only call %s", function)
		} else if !strings.Contains(err.Error(), "needs an OVER clause") {
			t.Errorf("bare window-only call %s got wrong refusal: %v", function, err)
		}
		if _, err := inferTestExprType(t, schema, function+" OVER ()"); err != nil {
			t.Errorf("inference refused window call %s OVER (): %v", function, err)
		}
	}
}

// TestFunctionSignatureKeepsBareWindowAggregateAliases holds the other side
// of the placement boundary. ClickHouse exposes first_value as any and
// last_value as anyLast, so each name is legal both as a bare aggregate and
// as a window call.
func TestFunctionSignatureKeepsBareWindowAggregateAliases(t *testing.T) {
	schema := schemaFromDDL(t, "CREATE TABLE probe (i8 Int8) ENGINE = Memory")
	for _, function := range []string{"first_value(i8)", "last_value(i8)"} {
		for _, expression := range []string{function, function + " OVER ()"} {
			got, err := inferTestExprType(t, schema, expression)
			if err != nil {
				t.Errorf("inference refused %s: %v", expression, err)
				continue
			}
			if got != "Int8" {
				t.Errorf("%s type = %s, want Int8", expression, got)
			}
		}
	}
}

func TestFunctionSignatureChecksFrameShiftOffsetLiteral(t *testing.T) {
	schema := schemaFromDDL(t, "CREATE TABLE probe (i8 Int8) ENGINE = Memory")
	for _, expression := range []string{
		"lagInFrame(i8, 1.5) OVER ()",
		"lagInFrame(i8, -1) OVER ()",
		"leadInFrame(i8, 9223372036854775808) OVER ()",
	} {
		if _, err := inferTestExprType(t, schema, expression); err == nil {
			t.Errorf("inference accepted invalid frame shift offset %s", expression)
		}
	}
	for _, expression := range []string{
		"lagInFrame(i8, 0) OVER ()",
		"leadInFrame(i8, 9223372036854775807) OVER ()",
	} {
		if _, err := inferTestExprType(t, schema, expression); err != nil {
			t.Errorf("inference refused valid frame shift offset %s: %v", expression, err)
		}
	}
}

func TestFunctionSignatureChecksNumericValuePositions(t *testing.T) {
	schema := schemaFromDDL(t, `CREATE TABLE probe (
		i8 Int8, f64 Float64, s String, d Date, arr Array(Int8)
	) ENGINE = Memory`)
	for _, expression := range []string{"addDays(d, f64)", "arrayResize(arr, i8)", "substring(s, i8)"} {
		if _, err := inferTestExprType(t, schema, expression); err != nil {
			t.Errorf("inference refused numeric value position in %s: %v", expression, err)
		}
	}
	for _, expression := range []string{"addDays(d, s)", "arrayResize(arr, s)", "substring(s, s)"} {
		if _, err := inferTestExprType(t, schema, expression); err == nil {
			t.Errorf("inference accepted nonnumeric value position in %s", expression)
		}
	}
}
