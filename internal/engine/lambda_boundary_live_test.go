//go:build fuzzoracle

package engine

import (
	"strings"
	"testing"
)

const higherOrderLiveDDL = `CREATE TABLE t (
    a Array(Int32),
    b Array(Int16),
    i32 Int32,
    f32 Float32,
    ni32 Nullable(Int32),
    flag Bool,
    u8 UInt8,
    flags Array(Bool),
    u8s Array(UInt8),
    lc Array(LowCardinality(String)),
    e8s Array(Enum8('a' = 1, 'b' = 2)),
    dates Array(Date),
    short Array(Int16),
    n Array(Nullable(Int32)),
    aa Array(Array(Int32)),
    saf SimpleAggregateFunction(anyLast, Array(Int32))
) ENGINE = MergeTree ORDER BY tuple()`

const higherOrderLiveSeed = `INSERT INTO t VALUES ([1, 2], [3, 4], 1, 1.5, NULL, false, 0, [true, false], [1, 2], ['b', 'a'], ['a', 'b'], ['2025-01-01', '2025-01-02'], [5], [1, NULL], [[1], [2]], [2, 1])`

// TestLambdaBoundariesAgainstClickHouse keeps an analysis witness and an
// execution witness for the lambda rules in lambda_boundary_test.go. The
// expressions read the real arr_i column of the current fixture. A literal
// array is not evidence because ClickHouse can fold it.
func TestLambdaBoundariesAgainstClickHouse(t *testing.T) {
	oracle := execWitnessFixture(t)
	schema, err := schemaFromDDLErr(t, oracleSchemaDDL)
	if err != nil {
		t.Fatal(err)
	}

	accepted := map[string]string{
		"arraySum(x -> toUInt128(x + 2147483648), arr_i)": "UInt128",
		"arraySum(x -> toInt128(x), arr_i)":               "Int128",
		"arraySum(x -> toUInt256(x + 2147483648), arr_i)": "UInt256",
		"arraySum(x -> toInt256(x), arr_i)":               "Int256",
		"arraySum(x -> toDecimal256(x, 7), arr_i)":        "Decimal(76, 7)",
		"arrayMap(x -> x, arr_i)":                         "Array(Int32)",
		"arrayMap((x, X) -> x + X, arr_i, arr_i)":         "Array(Int64)",
		"arrayMap(i32 -> i32 + 1, arr_i)":                 "Array(Int64)",
		"arrayMap(x -> x + i32, arr_i)":                   "Array(Int64)",
	}
	for expression, want := range accepted {
		analysis, analysisErr := oracle.exec("SELECT toTypeName(" + expression + ") FROM t")
		if analysisErr != nil {
			t.Errorf("analysis of %s: %v", expression, analysisErr)
			continue
		}
		if got := strings.TrimSpace(analysis); got != want {
			t.Errorf("analysis type of %s = %s, want %s", expression, got, want)
		}
		if _, executionErr := oracle.exec("SELECT ignore(" + expression + ") FROM t"); executionErr != nil {
			t.Errorf("execution of %s: %v", expression, executionErr)
		}
		chgenType, chgenErr := chgenInferType(schema, expression)
		if chgenErr != nil {
			t.Errorf("chgen inference of %s: %v", expression, chgenErr)
			continue
		}
		if chgenType != want {
			t.Errorf("chgen type of %s = %s, want %s", expression, chgenType, want)
		}
	}

	const duplicate = "arrayMap((x, x) -> x, arr_i, arr_i)"
	if _, err := oracle.exec("SELECT toTypeName(" + duplicate + ") FROM t"); err == nil || !strings.Contains(err.Error(), "Code: 36") {
		t.Fatalf("ClickHouse duplicate-name error = %v, want Code 36", err)
	}
	if _, err := chgenInferType(schema, duplicate); err == nil {
		t.Fatal("chgen accepted duplicate lambda parameter names")
	}

	const changedCase = "arrayMap(x -> X, arr_i)"
	if _, err := oracle.exec("SELECT toTypeName(" + changedCase + ") FROM t"); err == nil || !strings.Contains(err.Error(), "Code: 47") {
		t.Fatalf("ClickHouse changed-case lambda error = %v, want Code 47", err)
	}
	if _, err := chgenInferType(schema, changedCase); err == nil {
		t.Fatal("chgen accepted a changed-case lambda parameter reference")
	}
}

func TestHigherOrderLinkedArraysAgainstClickHouse(t *testing.T) {
	oracle := execWitnessFixtureWithDDL(t, higherOrderLiveDDL, higherOrderLiveSeed)
	schema, err := schemaFromDDLErr(t, higherOrderLiveDDL)
	if err != nil {
		t.Fatal(err)
	}

	accepted := map[string]string{
		"arrayExists((x, y) -> x > y, a, b)":                     "UInt8",
		"arraySort((x, y) -> x + y, a, b)":                       "Array(Int32)",
		"arrayMap(x -> x + 1, saf)":                              "Array(Int64)",
		"arrayFirst(x -> x > 1, a)":                              "Int32",
		"arrayFirstOrNull(x -> x > 1, a)":                        "Nullable(Int32)",
		"arrayLast(x -> x > 1, a)":                               "Int32",
		"arrayLastOrNull(x -> x > 1, a)":                         "Nullable(Int32)",
		"arrayFirstIndex(x -> x > 1, a)":                         "UInt32",
		"arrayLastIndex(x -> x > 1, a)":                          "UInt32",
		"arrayAvg(x -> x, a)":                                    "Float64",
		"arrayProduct(x -> x, a)":                                "Float64",
		"arrayCumSum(x -> x, a)":                                 "Array(Int64)",
		"arrayCumSumNonNegative(x -> x, a)":                      "Array(Int64)",
		"arrayAvg(a)":                                            "Float64",
		"arrayProduct(a)":                                        "Float64",
		"arrayCumSum(a)":                                         "Array(Int64)",
		"arrayCumSumNonNegative(a)":                              "Array(Int64)",
		"arrayAvg(flags)":                                        "Float64",
		"arrayProduct(flags)":                                    "Float64",
		"arrayCumSum(flags)":                                     "Array(UInt64)",
		"arrayAvg(e8s)":                                          "Float64",
		"arrayProduct(e8s)":                                      "Float64",
		"arrayFill(x -> x > 1, a)":                               "Array(Int32)",
		"arrayReverseFill(x -> x > 1, a)":                        "Array(Int32)",
		"arraySplit(x -> x > 1, a)":                              "Array(Array(Int32))",
		"arrayReverseSplit(x -> x > 1, a)":                       "Array(Array(Int32))",
		"arrayFold((acc, x) -> acc + toInt64(x), a, toInt64(0))": "Int64",
		"arrayFold((acc, x, y) -> acc + toInt64(x) + toInt64(y), a, b, toInt64(0))": "Int64",
		"arrayFold((acc, x) -> x, u8s, flag)":                                       "Bool",
		"arrayFold((acc, x) -> x, flags, u8)":                                       "UInt8",
		"arrayReverseSort(a)":                                                       "Array(Int32)",
		"arrayReverseSort(x -> -x, a)":                                              "Array(Int32)",
		"arrayPartialSort(i32, a)":                                                  "Array(Int32)",
		"arrayPartialSort(0, a)":                                                    "Array(Int32)",
		"arrayPartialSort(-1, a)":                                                   "Array(Int32)",
		"arrayPartialSort(u8, a)":                                                   "Array(Int32)",
		"arrayPartialSort(flag, a)":                                                 "Array(Int32)",
		"arrayPartialSort(x -> -x, i32, a)":                                         "Array(Int32)",
		"arrayPartialSort((x, y) -> x + y, i32, a, b)":                              "Array(Int32)",
		"arrayPartialReverseSort(i32, lc)":                                          "Array(String)",
		"arrayPartialReverseSort(flag, lc)":                                         "Array(String)",
		"arrayPartialReverseSort(x -> x, i32, lc)":                                  "Array(String)",
	}
	for expression, want := range accepted {
		analysis, analysisErr := oracle.exec("SELECT toTypeName(" + expression + ") FROM t")
		if analysisErr != nil {
			t.Errorf("analysis of %s: %v", expression, analysisErr)
			continue
		}
		if got := strings.TrimSpace(analysis); got != want {
			t.Errorf("analysis type of %s = %s, want %s", expression, got, want)
		}
		if _, executionErr := oracle.exec("SELECT ignore(" + expression + ") FROM t"); executionErr != nil {
			t.Errorf("execution of %s: %v", expression, executionErr)
		}
		chgenType, chgenErr := chgenInferType(schema, expression)
		if chgenErr != nil {
			t.Errorf("chgen inference of %s: %v", expression, chgenErr)
		} else if chgenType != want {
			t.Errorf("chgen type of %s = %s, want %s", expression, chgenType, want)
		}
	}

	const unequal = "arrayMap((x, y) -> x + y, a, short)"
	analysis, analysisErr := oracle.exec("SELECT toTypeName(" + unequal + ") FROM t")
	if analysisErr != nil || strings.TrimSpace(analysis) != "Array(Int64)" {
		t.Fatalf("unequal-length analysis = %q, %v; want Array(Int64)", strings.TrimSpace(analysis), analysisErr)
	}
	if _, executionErr := oracle.exec("SELECT ignore(" + unequal + ") FROM t"); executionErr == nil {
		t.Fatal("unequal-length higher-order call executed successfully")
	}
	if chgenType, chgenErr := chgenInferType(schema, unequal); chgenErr != nil || chgenType != "Array(Int64)" {
		t.Fatalf("chgen unequal-length type = %q, %v; want Array(Int64)", chgenType, chgenErr)
	}

	for _, expression := range []string{
		"arrayFirstOrNull(x -> 1, aa)",
		"arrayLastOrNull(x -> 1, aa)",
		"arrayAvg(x -> x, n)",
		"arrayProduct(x -> x, n)",
		"arrayCumSum(x -> x, n)",
		"arrayFold((acc, x) -> acc + x, a, toInt8(0))",
		"arrayFold((acc, x) -> x, u8s, toInt8(0))",
		"arrayFold((acc, x) -> x, u8s, toNullable(toUInt8(0)))",
		"arrayCumSum(e8s)",
		"arrayCumSum(dates)",
		"arrayAvg(dates)",
		"arrayPartialSort(a, i32)",
		"arrayPartialSort(x -> x, a, i32)",
		"arrayPartialSort(f32, a)",
		"arrayPartialSort(ni32, a)",
	} {
		if _, analysisErr := oracle.exec("SELECT toTypeName(" + expression + ") FROM t"); analysisErr == nil {
			t.Errorf("analysis accepted %s", expression)
		}
		if _, executionErr := oracle.exec("SELECT ignore(" + expression + ") FROM t"); executionErr == nil {
			t.Errorf("execution accepted %s", expression)
		}
		if chgenType, chgenErr := chgenInferType(schema, expression); chgenErr == nil {
			t.Errorf("chgen accepted %s as %s", expression, chgenType)
		}
	}

	for _, expression := range []string{"ARRAYAVG(a)", "ArrayAvg(a)", "arrayavg(a)", "ARRAYFIRST(x -> x > 0, a)"} {
		if _, analysisErr := oracle.exec("SELECT toTypeName(" + expression + ") FROM t"); analysisErr == nil || !strings.Contains(analysisErr.Error(), "Code: 46") {
			t.Errorf("case analysis of %s = %v, want Code 46", expression, analysisErr)
		}
		if chgenType, chgenErr := chgenInferType(schema, expression); chgenErr == nil {
			t.Errorf("chgen accepted case variant %s as %s", expression, chgenType)
		}
	}

	const unequalFold = "arrayFold((acc, x, y) -> acc + toInt64(x) + toInt64(y), a, short, toInt64(0))"
	if analysis, analysisErr := oracle.exec("SELECT toTypeName(" + unequalFold + ") FROM t"); analysisErr != nil || strings.TrimSpace(analysis) != "Int64" {
		t.Fatalf("unequal fold analysis = %q, %v; want Int64", strings.TrimSpace(analysis), analysisErr)
	}
	if _, executionErr := oracle.exec("SELECT ignore(" + unequalFold + ") FROM t"); executionErr == nil {
		t.Fatal("unequal-length arrayFold executed successfully")
	}
	unequalFamilies := map[string]string{
		"arrayFirst((x, y) -> x > y, a, short)":                   "Int32",
		"arrayFill((x, y) -> x > y, a, short)":                    "Array(Int32)",
		"arraySplit((x, y) -> x > y, a, short)":                   "Array(Array(Int32))",
		"arrayAvg((x, y) -> x + y, a, short)":                     "Float64",
		"arrayProduct((x, y) -> x + y, a, short)":                 "Float64",
		"arrayCumSum((x, y) -> x + y, a, short)":                  "Array(Int64)",
		"arrayPartialSort((x, y) -> x + y, i32, a, short)":        "Array(Int32)",
		"arrayPartialReverseSort((x, y) -> x + y, i32, a, short)": "Array(Int32)",
	}
	for expression, want := range unequalFamilies {
		analysis, analysisErr := oracle.exec("SELECT toTypeName(" + expression + ") FROM t")
		if analysisErr != nil || strings.TrimSpace(analysis) != want {
			t.Errorf("unequal family analysis of %s = %q, %v; want %s", expression, strings.TrimSpace(analysis), analysisErr, want)
			continue
		}
		if _, executionErr := oracle.exec("SELECT ignore(" + expression + ") FROM t"); executionErr == nil || !strings.Contains(executionErr.Error(), "Code: 190") {
			t.Errorf("unequal family execution of %s = %v, want Code 190", expression, executionErr)
		}
	}
}
