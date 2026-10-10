package functionrules_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/IlyaGulya/chgen"
)

// Exercise the public boundary, including parsing and scope construction.
// Depth is deliberately large enough to expose repeated subtree inference.
func TestDeepMeasuredCallsKeepTypesAndDiagnostics(t *testing.T) {
	ddl := "CREATE TABLE t (v Nullable(Float64)) ENGINE=Memory"
	expression := strings.Repeat("sin(", 18) + "v" + strings.Repeat(")", 18)
	got, err := chgen.InferExpressionType(ddl, "t", expression)
	if err != nil || got.String() != "Nullable(Float64)" {
		t.Fatalf("nested call: %s, %v", got.String(), err)
	}
	_, err = chgen.InferExpressionType(ddl, "t", "sin(sin(missing))")
	if err == nil || !strings.Contains(err.Error(), `function sin argument: function sin argument: column "missing"`) {
		t.Fatalf("nested diagnostic lost its argument path: %v", err)
	}
}

func BenchmarkMeasuredCallDepth(b *testing.B) {
	for _, depth := range []int{5, 10, 18} {
		b.Run(fmt.Sprint(depth), func(b *testing.B) {
			expression := strings.Repeat("sin(", depth) + "v" + strings.Repeat(")", depth)
			for b.Loop() {
				_, err := chgen.InferExpressionType("CREATE TABLE t (v Nullable(Float64)) ENGINE=Memory", "t", expression)
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestDeepTemporalCallsUseTheSameValidatedArgumentTypes(t *testing.T) {
	expression := strings.Repeat("addDays(", 24) + "v" + strings.Repeat(", 1)", 24)
	got, err := chgen.InferExpressionType("CREATE TABLE t (v Date) ENGINE=Memory", "t", expression)
	if err != nil || got.String() != "Date" {
		t.Fatalf("nested temporal call: %s, %v", got.String(), err)
	}
}
