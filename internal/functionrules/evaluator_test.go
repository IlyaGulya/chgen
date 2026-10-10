package functionrules_test

import (
	"strings"
	"testing"

	"github.com/IlyaGulya/chgen"
)

func TestFunctionEvaluationPreservesResultAndArgumentRoles(t *testing.T) {
	ddl := `CREATE TABLE t (i Int32, n Nullable(Int32), s String,
		a SimpleAggregateFunction(anyLast, Int32),
		an SimpleAggregateFunction(anyLast, Nullable(Int32))) ENGINE=Memory`
	for _, test := range []struct{ expression, want string }{
		{"sin(n)", "Nullable(Float64)"},
		{"sin(?)", "Float64"},
		{"count(*)", "UInt64"},
		{"countIf(i = ?)", "UInt64"},
		{"sum(an)", "Nullable(Int64)"},
		{"max(a)", "SimpleAggregateFunction(anyLast, Int32)"},
		{"argMax(a, n)", "Nullable(SimpleAggregateFunction(anyLast, Int32))"},
		{"nullIf(a, i)", "Nullable(SimpleAggregateFunction(anyLast, Int32))"},
		{"tuple(i, n)", "Tuple(Int32, Nullable(Int32))"},
	} {
		got, err := chgen.InferExpressionType(ddl, "t", test.expression)
		if err != nil || got.String() != test.want {
			t.Errorf("%s: got %s, %v; want %s", test.expression, got.String(), err, test.want)
		}
	}
	for _, test := range []struct{ expression, diagnostic string }{
		{"sin(sin(missing))", `function sin argument: function sin argument: column "missing"`},
		{"argMax(i, missing)", `function argMax argument: column "missing"`},
		{"tuple(i, missing)", `function tuple argument: column "missing"`},
		{"cityHash64(?, missing)", `function cityHash64 argument: column "missing"`},
		{"sum(s)", "function sum does not accept an argument of type String;"},
	} {
		_, err := chgen.InferExpressionType(ddl, "t", test.expression)
		if err == nil || !strings.Contains(err.Error(), test.diagnostic) {
			t.Errorf("%s: diagnostic must retain %q, got %v", test.expression, test.diagnostic, err)
		}
	}
}
