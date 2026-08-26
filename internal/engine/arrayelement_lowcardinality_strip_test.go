package engine

import "testing"

// arrayElement strips an element LowCardinality wrapper at every depth,
// while an element Nullable wrapper survives. This is the same
// wrapperOpaque LowCardinality rule that groupArrayFunctionResult
// already applies, via stripNestedLowCardinality.
//
// Measured on ClickHouse 25.8.29.51 with real columns and a VALUE
// witness (allow_suspicious_low_cardinality_types=1 to build the
// columns): lca Array(LowCardinality(String)) = ['a','b'],
// lcaa Array(Array(LowCardinality(String))) = [['a','b'],['c']],
// lcna Array(LowCardinality(Nullable(String))) = ['a', NULL].
//
//	arrayElement(lca, 2)                    String            'b'
//	arrayElement(lcaa, 1)                   Array(String)     ['a','b']
//	arrayElement(arrayElement(lcaa, 1), 1)  String            'a'
//	arrayElement(lcna, 1)                   Nullable(String)  'a'
func TestArrayElementStripsLowCardinalityElement(t *testing.T) {
	schema, err := schemaFromDDLErr(t, `CREATE TABLE t (
    lca  Array(LowCardinality(String)),
    lcaa Array(Array(LowCardinality(String))),
    lcna Array(LowCardinality(Nullable(String)))
) ENGINE = MergeTree ORDER BY tuple();`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	cases := []struct{ expr, want string }{
		{"arrayElement(lca, 2)", "String"},
		{"arrayElement(lcaa, 1)", "Array(String)"},
		{"arrayElement(arrayElement(lcaa, 1), 1)", "String"},
		// A Nullable element is NOT stripped, unlike LowCardinality.
		{"arrayElement(lcna, 1)", "Nullable(String)"},
	}
	for _, testCase := range cases {
		if got := inferCHTypeString(t, schema, testCase.expr); got != testCase.want {
			t.Errorf("type of %q = %s, want %s", testCase.expr, got, testCase.want)
		}
	}
}
