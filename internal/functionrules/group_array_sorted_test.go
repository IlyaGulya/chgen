package functionrules_test

import (
	"testing"

	"github.com/IlyaGulya/chgen"
)

const sortedArraySchema = `CREATE TABLE t (
 s String, n Nullable(String), lc LowCardinality(String), i UInt64,
 a Array(Int32), dt DateTime('UTC'), d Decimal(18, 4),
 saf SimpleAggregateFunction(anyLast, String)
) ENGINE=Memory`

func TestGroupArraySortedPublicTypes(t *testing.T) {
	for _, test := range []struct{ expression, want string }{
		{"groupArraySorted(5)(s)", "Array(String)"},
		{"groupArraySorted(1)(s)", "Array(String)"},
		{"groupArraySorted(16777215)(s)", "Array(String)"},
		{"groupArraySorted(5)(n)", "Array(String)"},
		{"groupArraySorted(5)(lc)", "Array(String)"},
		{"groupArraySorted(5)(i)", "Array(UInt64)"},
		{"groupArraySorted(5)(a)", "Array(Array(Int32))"},
		{"groupArraySorted(5)(dt)", "Array(DateTime('UTC'))"},
		{"groupArraySorted(5)(d)", "Array(Decimal(18, 4))"},
		{"groupArraySorted(5)(saf)", "Array(SimpleAggregateFunction(anyLast, String))"},
	} {
		got, err := chgen.InferExpressionType(sortedArraySchema, "t", test.expression)
		if err != nil || got.String() != test.want {
			t.Errorf("%s: got %s, %v; want %s", test.expression, got.String(), err, test.want)
		}
	}
}

func TestGroupArraySortedDistinctAndIf(t *testing.T) {
	for _, expression := range []string{"groupArraySortedDistinct(5)(s)", "groupArraySortedIf(5)(s, i > 0)", "groupArraySortedDistinctIf(5)(s, i > 0)"} {
		got, err := chgen.InferExpressionType(sortedArraySchema, "t", expression)
		if err != nil || got.String() != "Array(String)" {
			t.Errorf("%s: got %s, %v", expression, got.String(), err)
		}
	}
}

func TestGroupArraySortedRejectsInvalidCalls(t *testing.T) {
	for _, expression := range []string{
		"groupArraySorted(s)", "groupArraySorted()(s)", "groupArraySorted(0)(s)",
		"groupArraySorted(-1)(s)", "groupArraySorted(16777216)(s)", "groupArraySorted(1.5)(s)",
		"groupArraySorted('5')(s)", "groupArraySorted(i)(s)", "groupArraySorted(?)(s)",
		"groupArraySorted(5, 6)(s)", "groupArraySorted(5)()", "groupArraySorted(5)(s,s)",
		"groupArraySorted(5)(missing_column)", "any(groupArraySorted(5)(s))",
		"GROUPARRAYSORTED(5)(s)", "groupArraySortedDistinct(0)(s)",
		"GROUPARRAYSORTEDIf(5)(s, i > 0)", "groupArraySortedif(5)(s, i > 0)",
		"groupArraySortedDistinct(s)", "groupArraySortedIf(5)(s, s)",
	} {
		if _, err := chgen.InferExpressionType(sortedArraySchema, "t", expression); err == nil {
			t.Errorf("invalid call accepted: %s", expression)
		}
	}
}
