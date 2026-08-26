package engine

import (
	"strings"
	"testing"
)

const higherOrderSignatureDDL = `CREATE TABLE t (
    i32 Int32,
    f32 Float32,
    ni32 Nullable(Int32),
    flag Bool,
    u8 UInt8,
    a Array(Int32),
    b Array(Int16),
    short Array(Int16),
    n Array(Nullable(Int32)),
    flags Array(Bool),
    u8s Array(UInt8),
    lc Array(LowCardinality(String)),
    saf SimpleAggregateFunction(anyLast, Array(Int32))
) ENGINE = MergeTree ORDER BY tuple();`

func TestHigherOrderDescriptorCoversSupportedRoster(t *testing.T) {
	want := map[string]struct {
		result higherOrderArrayResult
		domain higherOrderLambdaBodyDomain
	}{
		"arraymap":    {hofArrayOfBody, hofBodyDomainAny},
		"arrayfilter": {hofFirstArray, hofBodyDomainPredicate},
		"arraysort":   {hofFirstArray, hofBodyDomainAny},
		"arraysum":    {hofSumOfBody, hofBodyDomainSummable},
		"arraymin":    {hofBody, hofBodyDomainAny},
		"arraymax":    {hofBody, hofBodyDomainAny},
		"arraycount":  {hofCount, hofBodyDomainPredicate},
		"arrayexists": {hofPredicate, hofBodyDomainPredicate},
		"arrayall":    {hofPredicate, hofBodyDomainPredicate},
	}
	for name, expected := range want {
		rule, ok := higherOrderArrayFunctions[name]
		if !ok {
			t.Errorf("function %s has no higher-order descriptor", name)
			continue
		}
		if err := validateHigherOrderLambdaSignature(name, rule); err != nil {
			t.Errorf("function %s descriptor: %v", name, err)
		}
		if rule.result != expected.result || rule.bodyDomain != expected.domain {
			t.Errorf("function %s descriptor = result %d, domain %d; want result %d, domain %d",
				name, rule.result, rule.bodyDomain, expected.result, expected.domain)
		}
		if rule.minimumArrays != 1 || rule.maximumArrays != -1 ||
			rule.lengthPolicy != hofLengthEqualAtExecution || !rule.allowOuterCapture || rule.accumulator != nil {
			t.Errorf("function %s does not use the linked repeated Array group", name)
		}
	}
}

func TestHigherOrderDescriptorMutationsRefuse(t *testing.T) {
	base := higherOrderArrayFunctions["arraymap"]
	if err := validateHigherOrderLambdaSignature("arraymap", base); err != nil {
		t.Fatalf("baseline descriptor is invalid: %v", err)
	}
	mutations := map[string]higherOrderLambdaSignature{
		"unknown result":        base,
		"unknown body domain":   base,
		"zero arrays":           base,
		"reversed range":        base,
		"unknown length policy": base,
		"invalid accumulator":   base,
	}
	value := mutations["unknown result"]
	value.result = hofAccumulator + 1
	mutations["unknown result"] = value
	value = mutations["unknown body domain"]
	value.bodyDomain = hofBodyDomainNumericReduction + 1
	mutations["unknown body domain"] = value
	value = mutations["zero arrays"]
	value.minimumArrays = 0
	mutations["zero arrays"] = value
	value = mutations["reversed range"]
	value.maximumArrays = 0
	mutations["reversed range"] = value
	value = mutations["unknown length policy"]
	value.lengthPolicy = hofLengthUnknown
	mutations["unknown length policy"] = value
	value = mutations["invalid accumulator"]
	value.accumulator = &higherOrderLambdaAccumulator{argumentPosition: -2, parameterPosition: 0}
	mutations["invalid accumulator"] = value

	wantError := map[string]string{
		"unknown result":        "unknown higher-order result rule",
		"unknown body domain":   "unknown lambda body domain",
		"zero arrays":           "invalid linked Array range",
		"reversed range":        "invalid linked Array range",
		"unknown length policy": "no measured array length policy",
		"invalid accumulator":   "invalid accumulator link",
	}
	for name, mutation := range mutations {
		err := validateHigherOrderLambdaSignature("arraymap", mutation)
		if err == nil || !strings.Contains(err.Error(), wantError[name]) {
			t.Errorf("mutation %s error = %v, want %q", name, err, wantError[name])
		}
	}
}

func TestHigherOrderLinkedArrayArguments(t *testing.T) {
	accepted := map[string]string{
		"arrayExists((x, y) -> x > y, a, b)": "UInt8",
		"arraySort((x, y) -> x + y, a, b)":   "Array(Int32)",
		"arrayMap((x, y) -> x + y, a, b)":    "Array(Int64)",
		"arrayMap(x -> x + i32, a)":          "Array(Int64)",
	}
	for expression, want := range accepted {
		got, err := InferExpressionType(higherOrderSignatureDDL, "t", expression)
		if err != nil {
			t.Errorf("InferExpressionType(%s) error = %v", expression, err)
			continue
		}
		if got.String() != want {
			t.Errorf("InferExpressionType(%s) = %s, want %s", expression, got.String(), want)
		}
	}

	for _, expression := range []string{
		"arrayExists(x -> x > 0, a, b)",
		"arraySort((x, y) -> x + y, a)",
		"arrayMap((x, x) -> x, a, b)",
		"arrayExists((x, y) -> x + y, a, b)",
	} {
		if got, err := InferExpressionType(higherOrderSignatureDDL, "t", expression); err == nil {
			t.Errorf("InferExpressionType(%s) = %s, want refusal", expression, got.String())
		}
	}
}

func TestHigherOrderReadsThroughSimpleAggregateArray(t *testing.T) {
	cases := map[string]string{
		"arrayMap(x -> x + 1, saf)":    "Array(Int64)",
		"arrayExists(x -> x > 0, saf)": "UInt8",
		"arraySort(x -> -x, saf)":      "Array(Int32)",
	}
	for expression, want := range cases {
		got, err := InferExpressionType(higherOrderSignatureDDL, "t", expression)
		if err != nil {
			t.Errorf("InferExpressionType(%s) error = %v", expression, err)
			continue
		}
		if got.String() != want {
			t.Errorf("InferExpressionType(%s) = %s, want %s", expression, got.String(), want)
		}
	}
}

func TestHigherOrderErrorsNameLinkedArity(t *testing.T) {
	_, err := InferExpressionType(higherOrderSignatureDDL, "t", "arrayMap((x, y) -> x + y, a)")
	if err == nil || !strings.Contains(err.Error(), "2 lambda parameters and 1 array arguments") {
		t.Fatalf("linked arity error = %v", err)
	}
}

func TestMeasuredHigherOrderRosterTypes(t *testing.T) {
	accepted := map[string]string{
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
		"arrayCumSumNonNegative(flags)":                          "Array(UInt64)",
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
		got, err := InferExpressionType(higherOrderSignatureDDL, "t", expression)
		if err != nil {
			t.Errorf("InferExpressionType(%s) error = %v", expression, err)
			continue
		}
		if got.String() != want {
			t.Errorf("InferExpressionType(%s) = %s, want %s", expression, got.String(), want)
		}
	}
}

func TestMeasuredHigherOrderRosterRefusals(t *testing.T) {
	const ddl = `CREATE TABLE t (
    a Array(Int32),
    b Array(Int16),
    u8s Array(UInt8),
    n Array(Nullable(Int32)),
    aa Array(Array(Int32))
) ENGINE = MergeTree ORDER BY tuple();`
	for _, expression := range []string{
		"arrayFirstOrNull(x -> 1, aa)",
		"arrayLastOrNull(x -> 1, aa)",
		"arrayAvg(x -> x, n)",
		"arrayProduct(x -> x, n)",
		"arrayCumSum(x -> x, n)",
		"arrayFold((acc, x) -> acc + x, a, toInt8(0))",
		"arrayFold((acc, x) -> x, u8s, toInt8(0))",
		"arrayFold((acc, x) -> x, u8s, toNullable(toUInt8(0)))",
		"arrayAvg()",
		"arrayAvg(a, b)",
		"arrayCumSum(a, b)",
	} {
		if got, err := InferExpressionType(ddl, "t", expression); err == nil {
			t.Errorf("InferExpressionType(%s) = %s, want refusal", expression, got.String())
		}
	}
}

func TestHigherOrderFunctionNamesKeepCanonicalCase(t *testing.T) {
	for _, expression := range []string{
		"ARRAYAVG(a)",
		"ArrayAvg(a)",
		"arrayavg(a)",
		"ARRAYFIRST(x -> x > 0, a)",
		"ARRAYPARTIALSORT(i32, a)",
	} {
		if got, err := InferExpressionType(higherOrderSignatureDDL, "t", expression); err == nil {
			t.Errorf("InferExpressionType(%s) = %s, want case refusal", expression, got.String())
		}
	}
}

func TestPartialSortKeepsLimitBeforeArrays(t *testing.T) {
	for _, expression := range []string{
		"arrayPartialSort(a, i32)",
		"arrayPartialSort(x -> x, a, i32)",
		"arrayPartialSort(f32, a)",
		"arrayPartialSort(ni32, a)",
	} {
		if got, err := InferExpressionType(higherOrderSignatureDDL, "t", expression); err == nil {
			t.Errorf("InferExpressionType(%s) = %s, want limit refusal", expression, got.String())
		}
	}
}

func TestHigherOrderDescriptorFieldsControlCalls(t *testing.T) {
	mutate := func(name string, change func(*higherOrderLambdaSignature), expression string) {
		t.Helper()
		original := higherOrderArrayFunctions[name]
		changed := original
		if original.accumulator != nil {
			accumulator := *original.accumulator
			changed.accumulator = &accumulator
		}
		if original.scalarArgument != nil {
			scalar := *original.scalarArgument
			changed.scalarArgument = &scalar
		}
		change(&changed)
		higherOrderArrayFunctions[name] = changed
		defer func() { higherOrderArrayFunctions[name] = original }()
		if got, err := InferExpressionType(higherOrderSignatureDDL, "t", expression); err == nil {
			t.Errorf("InferExpressionType(%s) = %s after %s mutation, want refusal", expression, got.String(), name)
		}
	}
	mutate("arraymap", func(rule *higherOrderLambdaSignature) { rule.allowOuterCapture = false },
		"arrayMap(x -> x + i32, a)")
	mutate("arraymap", func(rule *higherOrderLambdaSignature) { rule.lengthPolicy = hofLengthUnknown },
		"arrayMap(x -> x, a)")
	mutate("arraymap", func(rule *higherOrderLambdaSignature) { rule.spelling = "arraymap" },
		"arrayMap(x -> x, a)")
	mutate("arrayfold", func(rule *higherOrderLambdaSignature) { rule.accumulator.argumentPosition = 0 },
		"arrayFold((acc, x) -> acc + toInt64(x), a, toInt64(0))")
	mutate("arraypartialsort", func(rule *higherOrderLambdaSignature) { rule.scalarArgument.positionAfterLambda = 1 },
		"arrayPartialSort(x -> x, i32, a)")
}
