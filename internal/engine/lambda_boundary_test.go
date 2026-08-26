package engine

import (
	"strings"
	"testing"
)

const lambdaBoundaryDDL = `CREATE TABLE t (
    i32 Int32,
    arr_i Array(Int32),
    arr_n Array(Nullable(Int32))
) ENGINE = MergeTree ORDER BY tuple();`

func TestArraySumKeepsWideAndDecimal256BodyTypes(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"arraySum(x -> toUInt128(x + 2147483648), arr_i)": "UInt128",
		"arraySum(x -> toInt128(x), arr_i)":               "Int128",
		"arraySum(x -> toUInt256(x + 2147483648), arr_i)": "UInt256",
		"arraySum(x -> toInt256(x), arr_i)":               "Int256",
		"arraySum(x -> toDecimal256(x, 7), arr_i)":        "Decimal(76, 7)",
		// These controls keep the existing promotion boundary.
		"arraySum(x -> toUInt8(x), arr_i)":         "UInt64",
		"arraySum(x -> toInt32(x), arr_i)":         "Int64",
		"arraySum(x -> toFloat32(x), arr_i)":       "Float64",
		"arraySum(x -> toDecimal128(x, 7), arr_i)": "Decimal(38, 7)",
	}
	for expression, want := range cases {
		got, err := InferExpressionType(lambdaBoundaryDDL, "t", expression)
		if err != nil {
			t.Errorf("InferExpressionType(%s) error = %v", expression, err)
			continue
		}
		if got.String() != want {
			t.Errorf("InferExpressionType(%s) = %s, want %s", expression, got.String(), want)
		}
	}
}

func TestArraySumKeepsUnsupportedBodyRefusals(t *testing.T) {
	t.Parallel()

	for _, expression := range []string{
		"arraySum(x -> x, arr_n)",
		"arraySum(x -> toDate(x), arr_i)",
	} {
		if got, err := InferExpressionType(lambdaBoundaryDDL, "t", expression); err == nil {
			t.Errorf("InferExpressionType(%s) = %s, want refusal", expression, got.String())
		}
	}
}

func TestArraySumWideResultStopsAtGoTransport(t *testing.T) {
	t.Parallel()

	schema, err := schemaFromDDLErr(t, lambdaBoundaryDDL)
	if err != nil {
		t.Fatal(err)
	}
	_, err = parseQueriesWithSchema(t, `-- name: SumWide :one
SELECT arraySum(x -> toUInt128(x + 2147483648), arr_i) AS total FROM t;`, schema)
	if err == nil || !strings.Contains(err.Error(), "measured refusal") {
		t.Fatalf("parse query error = %v, want measured wide integer refusal", err)
	}
}

func TestArraySumDecimal256ResultGenerates(t *testing.T) {
	t.Parallel()

	schema, err := schemaFromDDLErr(t, lambdaBoundaryDDL)
	if err != nil {
		t.Fatal(err)
	}
	queries, err := parseQueriesWithSchema(t, `-- name: SumDecimal256 :one
SELECT arraySum(x -> toDecimal256(x, 7), arr_i) AS total FROM t;`, schema)
	if err != nil {
		t.Fatalf("parse query: %v", err)
	}
	if got := queries[0].Results[0].GoType; got != "decimal.Decimal" {
		t.Fatalf("generated result type = %s, want decimal.Decimal", got)
	}
}

func TestLambdaParameterNamesUseClickHouseCaseRules(t *testing.T) {
	t.Parallel()

	refused := []string{
		"arrayMap((x, x) -> x, arr_i, arr_n)",
		"arrayMap((Value, Value) -> Value, arr_i, arr_n)",
	}
	for _, expression := range refused {
		if got, err := InferExpressionType(lambdaBoundaryDDL, "t", expression); err == nil {
			t.Errorf("InferExpressionType(%s) = %s, want duplicate-name refusal", expression, got.String())
		}
	}
	// A lambda binding is case-sensitive. X does not name x.
	const changedCase = "arrayMap(x -> X, arr_i)"
	if got, err := InferExpressionType(lambdaBoundaryDDL, "t", changedCase); err == nil {
		t.Fatalf("InferExpressionType(%s) = %s, want missing-name refusal", changedCase, got.String())
	}

	accepted := map[string]string{
		"arrayMap((x, y) -> x + y, arr_i, arr_i)": "Array(Int64)",
		// ClickHouse lambda names are case-sensitive.
		"arrayMap((x, X) -> x + X, arr_i, arr_i)": "Array(Int64)",
		// The lambda parameter shadows the outer column with the same name.
		"arrayMap(i32 -> i32 + 1, arr_i)": "Array(Int64)",
		// A lambda body can capture an outer column.
		"arrayMap(x -> x + i32, arr_i)": "Array(Int64)",
	}
	for expression, want := range accepted {
		got, err := InferExpressionType(lambdaBoundaryDDL, "t", expression)
		if err != nil {
			t.Errorf("InferExpressionType(%s) error = %v", expression, err)
			continue
		}
		if got.String() != want {
			t.Errorf("InferExpressionType(%s) = %s, want %s", expression, got.String(), want)
		}
	}
}

func TestLambdaScopeKeepsOuterScalarAliasRules(t *testing.T) {
	t.Parallel()

	outer := queryScope{scalars: map[string]CHType{
		"CapturedAlias": {Name: "Int32"},
		"X":             {Name: "String"},
	}}
	lambda := queryScope{
		scalars:          map[string]CHType{"x": {Name: "Int8"}},
		exactScalarNames: true,
		parent:           &outer,
	}

	if got, ok := lambda.lookupScalar("x"); !ok || got.String() != "Int8" {
		t.Fatalf("exact lambda lookup = %s, %t; want Int8, true", got.String(), ok)
	}
	if got, ok := lambda.lookupScalar("X"); !ok || got.String() != "String" {
		t.Fatalf("outer capture X = %s, %t; want String, true", got.String(), ok)
	}
	if got, ok := lambda.lookupScalar("capturedalias"); !ok || got.String() != "Int32" {
		t.Fatalf("outer case-insensitive capture = %s, %t; want Int32, true", got.String(), ok)
	}
	withoutCapture := queryScope{
		scalars:          map[string]CHType{"x": {Name: "Int8"}},
		exactScalarNames: true,
		parent:           &queryScope{scalars: map[string]CHType{}},
	}
	if got, ok := withoutCapture.lookupScalar("X"); ok {
		t.Fatalf("case-changed lambda lookup = %s, true; want no binding", got.String())
	}
}
