package functionrules_test

import (
	"testing"

	"github.com/IlyaGulya/chgen"
)

func TestReversePreservesScalarSimpleAggregateMarkers(t *testing.T) {
	for _, typ := range []string{"String", "FixedString(8)"} {
		want := "SimpleAggregateFunction(anyLast, " + typ + ")"
		got, err := chgen.InferExpressionType("CREATE TABLE t (v "+want+") ENGINE=Memory", "t", "reverse(v)")
		if err != nil || got.String() != want {
			t.Errorf("reverse(%s): got %s, %v; want %s", want, got.String(), err, want)
		}
	}
}

func TestQuantileStateReadsEncodedMarkerArguments(t *testing.T) {
	for _, test := range []struct{ typ, expression, want string }{
		{"SimpleAggregateFunction(anyLast, LowCardinality(Int32))", "quantileState(v)", "AggregateFunction(quantile, Int32)"},
		{"SimpleAggregateFunction(anyLast, LowCardinality(UInt64))", "quantileStateIf(v, b)", "AggregateFunction(quantile, UInt64)"},
		{"SimpleAggregateFunction(anyLast, Nullable(Int32))", "quantileStateIf(v, b)", "AggregateFunction(quantile, Int32)"},
		{"SimpleAggregateFunction(anyLast, Nullable(Int32))", "quantileState(v)", "AggregateFunction(quantile, SimpleAggregateFunction(anyLast, Nullable(Int32)))"},
	} {
		got, err := chgen.InferExpressionType("CREATE TABLE t (v "+test.typ+", b UInt8) ENGINE=Memory", "t", test.expression)
		if err != nil || got.String() != test.want {
			t.Errorf("%s(%s): got %s, %v; want %s", test.expression, test.typ, got.String(), err, test.want)
		}
	}
}
