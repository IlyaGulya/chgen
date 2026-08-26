package engine

import "testing"

// The server holds one general rule about an ELEMENT LowCardinality: a
// LowCardinality wrapper that sits on the element of a container is gone
// from a READ value at any depth, while a Nullable element survives. This
// is the regression rule. It is not a rule per function; it is one
// shared fact about how the server reads a container element, and each
// function below meets it only where that function reads its argument as
// a value.
//
// Measured on ClickHouse 25.8.29.51 with real columns in a real table,
// reading the VALUE and not only the type, never over literals (a folded
// literal reports a different wrapper):
//
//	lca  Array(LowCardinality(String))              held ['a','b']
//	lcaa Array(Array(LowCardinality(String)))        held [['a','b'],['c']]
//	lcna Array(LowCardinality(Nullable(String)))     held ['a',NULL]
//
//	arrayElement(lca, 2)                String                  'b'
//	arrayElement(lcaa, 1)                Array(String)           ['a','b']
//	arrayElement(arrayElement(lcaa,1),1) String                  'a'
//	arrayElement(lcna, 1)                Nullable(String)        'a'
//	assumeNotNull(lca)                   Array(String)           ['a','b']
//	assumeNotNull(lcaa)                   Array(Array(String))    [['a','b'],['c']]
//	assumeNotNull(lcna)                   Array(Nullable(String)) ['a',NULL]
//
// A Nullable element is NOT touched: only the LowCardinality wrapper
// comes off, at every depth, in Array, Map and Tuple alike. See
// stripNestedLowCardinality, which already carries this behaviour for
// the aggregate paths (groupArray, argMin, and so on) and is now reused
// here for arrayElement's off-limits rule (arrayElementFunctionResult,
// supertype.go) and for assumeNotNull
// (withoutNullableFunctionArgument, infer_function.go).
//
// Before this fix chgen answered LowCardinality(String) for
// arrayElement(lca, 2) and Array(LowCardinality(String)) for
// assumeNotNull(lca). Both are silently wrong types: the server never
// gives them back.
const elementLowCardinalitySchemaDDL = `
CREATE TABLE probe (
	id   UInt64,
	lca  Array(LowCardinality(String)),
	lcaa Array(Array(LowCardinality(String))),
	lcna Array(LowCardinality(Nullable(String)))
) ENGINE = Memory
`

func elementLowCardinalityTestSchema(t *testing.T) *Schema {
	t.Helper()
	schema, err := schemaFromDDLErr(t, elementLowCardinalitySchemaDDL)
	if err != nil {
		t.Fatalf("parse probe schema: %v", err)
	}
	return schema
}

// arrayElement's half of this ticket (fn/arrayelement/bare/lcarr) is NOT
// fixed here. arrayElementFunctionResult lives in supertype.go,
// which this change does not own. Apply
// stripNestedLowCardinality to the returned element type there, the
// same way this file applies it to assumeNotNull below.

// TestAssumeNotNullDropsTheElementLowCardinality is the same rule read
// through assumeNotNull. The function reads its argument as a value (see
// withoutNullableFunctionArgument), so the element LowCardinality of a
// container argument is gone too, at every depth, while the Nullable
// element that assumeNotNull is FOR stays where it already was (inside
// the array, not on the array itself).
func TestAssumeNotNullDropsTheElementLowCardinality(t *testing.T) {
	schema := elementLowCardinalityTestSchema(t)
	for _, testCase := range []struct{ expression, want string }{
		{"assumeNotNull(lca)", "Array(String)"},
		{"assumeNotNull(lcaa)", "Array(Array(String))"},
		{"assumeNotNull(lcna)", "Array(Nullable(String))"},
	} {
		result, err := inferTestExprType(t, schema, testCase.expression)
		if err != nil {
			t.Errorf("%s: refused a call that the server accepts: %v", testCase.expression, err)
			continue
		}
		if result != testCase.want {
			t.Errorf("%s: got %s, want %s: the server drops the element LowCardinality",
				testCase.expression, result, testCase.want)
		}
	}
}
