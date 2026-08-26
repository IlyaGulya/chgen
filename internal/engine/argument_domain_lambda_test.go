package engine

import (
	"strings"
	"testing"
)

// These tests fail at base 0f75ec1 and pass after the domain rules of this
// change. Every expected accept and every expected refusal is measured on
// ClickHouse 25.8.29.51 with real table columns, never with a literal,
// because ClickHouse folds a constant and a rule measured over a literal
// reports the wrong domain.

// The probe schema and the inference helper come from
// argument_domain_test.go, so the two domain suites measure over the same
// columns.
func lambdaDomainType(t *testing.T, expression string) (string, error) {
	t.Helper()
	return inferTestExprType(t, argumentDomainTestSchema(t), expression)
}

// TestPredicateLambdaBodyRefusesNonPredicate covers the largest group of
// the ticket: chgen gave arrayCount the type UInt32 whatever the lambda
// body was, and ClickHouse then answered
//
//	Expression for function arrayCount must return UInt8 or
//	Nullable(UInt8), found Int64   (Code: 43)
//
// The body type, not an argument type, is the thing the server refuses.
func TestPredicateLambdaBodyRefusesNonPredicate(t *testing.T) {
	refused := []struct {
		expression string
		bodyType   string
	}{
		// Measured: each of these is Code: 43 on the server.
		{"arrayCount(x -> (x + 1), arr_i)", "Int64"},
		{"arrayCount(x -> x, arr_i)", "Int32"},
		{"arrayCount(x -> toString(x), arr_i)", "String"},
		{"arrayCount(x -> toFloat64(x), arr_i)", "Float64"},
		{"arrayExists(x -> x, arr_i)", "Int32"},
		{"arrayAll(x -> toInt8(x), arr_i)", "Int8"},
		{"arrayFilter(x -> x, arr_i)", "Int32"},
	}
	for _, testCase := range refused {
		got, err := lambdaDomainType(t, testCase.expression)
		if err == nil {
			t.Errorf("%s: chgen gave the type %s, but ClickHouse refuses the call with Code: 43", testCase.expression, got)
			continue
		}
		if !strings.Contains(err.Error(), "lambda body") {
			t.Errorf("%s: the refusal must name the lambda body, got %q", testCase.expression, err)
		}
		if !strings.Contains(err.Error(), testCase.bodyType) {
			t.Errorf("%s: the refusal must name the body type %s, got %q", testCase.expression, testCase.bodyType, err)
		}
		if !strings.Contains(err.Error(), pinTypeHint) {
			t.Errorf("%s: the refusal must carry the pin-type hint, got %q", testCase.expression, err)
		}
	}
}

// TestPredicateLambdaBodyKeepsTheAcceptedBodies proves the rule refuses
// only what the server refuses. Each expression below gives a type on
// ClickHouse 25.8.29.51.
func TestPredicateLambdaBodyKeepsTheAcceptedBodies(t *testing.T) {
	accepted := map[string]string{
		// Measured: arrayCount(x -> x > 1, arr_i) is UInt32.
		"arrayCount(x -> x > 1, arr_i)":      "UInt32",
		"arrayCount(x -> toUInt8(x), arr_i)": "UInt32",
		// Measured: arrayExists and arrayAll give UInt8, and so does
		// chgen: this is the same predicate family as the comparison
		// operators.
		"arrayExists(x -> x > 1, arr_i)": "UInt8",
		"arrayAll(x -> x > 1, arr_i)":    "UInt8",
		// Measured: arrayFilter(x -> x > 1, arr_i) is Array(Int32).
		"arrayFilter(x -> x > 1, arr_i)": "Array(Int32)",
		// arraySort shares the result shape of arrayFilter but reads no
		// predicate. Measured: arraySort(x -> x, arr_i) is Array(Int32).
		// The rule must not refuse it.
		"arraySort(x -> x, arr_i)": "Array(Int32)",
		// arrayMap, arrayMin and arrayMax take any body type.
		"arrayMap(x -> toString(x), arr_i)": "Array(String)",
		"arrayMin(x -> x, arr_i)":           "Int32",
		"arrayMax(x -> (x + 1), arr_i)":     "Int64",
	}
	for expression, want := range accepted {
		got, err := lambdaDomainType(t, expression)
		if err != nil {
			t.Errorf("%s: ClickHouse accepts this call, but chgen refused it: %v", expression, err)
			continue
		}
		if got != want {
			t.Errorf("%s: got %s, want %s", expression, got, want)
		}
	}
}

// TestToStartOfIntervalRefusesAnIllegalUnit covers the second group of the
// ticket. ClickHouse reports this one at execution time
// ("Illegal interval kind for argument data type Date"), thus a missing
// rule let the generated Go compile and the query fail on the server.
func TestToStartOfIntervalRefusesAnIllegalUnit(t *testing.T) {
	refused := []string{
		// Measured: every sub-day unit against a Date or a Date32 is
		// Code: 43.
		"toStartOfInterval(d, INTERVAL 1 HOUR)",
		"toStartOfInterval(d, INTERVAL 1 MINUTE)",
		"toStartOfInterval(d, INTERVAL 1 SECOND)",
		"toStartOfInterval(d32t, INTERVAL 1 HOUR)",
		// Measured: a sub-second unit against a DateTime is Code: 43.
		"toStartOfInterval(dt, INTERVAL 1 MILLISECOND)",
	}
	for _, expression := range refused {
		got, err := lambdaDomainType(t, expression)
		if err == nil {
			t.Errorf("%s: chgen gave the type %s, but ClickHouse refuses the call with Code: 43", expression, got)
			continue
		}
		if !strings.Contains(err.Error(), "INTERVAL") {
			t.Errorf("%s: the refusal must name the unit, got %q", expression, err)
		}
		if !strings.Contains(err.Error(), pinTypeHint) {
			t.Errorf("%s: the refusal must carry the pin-type hint, got %q", expression, err)
		}
	}
}

// TestToStartOfIntervalKeepsTheLegalUnits proves the unit rule refuses
// only what the server refuses. Each result below is measured.
func TestToStartOfIntervalKeepsTheLegalUnits(t *testing.T) {
	accepted := map[string]string{
		"toStartOfInterval(d, INTERVAL 1 DAY)":            "DateTime",
		"toStartOfInterval(d, INTERVAL 1 WEEK)":           "Date",
		"toStartOfInterval(d, INTERVAL 1 MONTH)":          "Date",
		"toStartOfInterval(d32t, INTERVAL 1 YEAR)":        "Date",
		"toStartOfInterval(dt, INTERVAL 1 SECOND)":        "DateTime",
		"toStartOfInterval(dt, INTERVAL 1 HOUR)":          "DateTime",
		"toStartOfInterval(dt64, INTERVAL 1 MILLISECOND)": "DateTime64(3)",
	}
	for expression, want := range accepted {
		got, err := lambdaDomainType(t, expression)
		if err != nil {
			t.Errorf("%s: ClickHouse accepts this call, but chgen refused it: %v", expression, err)
			continue
		}
		if got != want {
			t.Errorf("%s: got %s, want %s", expression, got, want)
		}
	}
}
