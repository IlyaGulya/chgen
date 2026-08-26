package engine

import "testing"

// arrayDistinct removes the duplicate elements of an Array. A NULL is a
// duplicate of a NULL, and the server removes it from the DATA, thus the
// result cannot hold a NULL and its element type is not Nullable. Its
// three neighbours (arraySort, arraySlice, arrayResize) keep every
// element, thus they keep the element Nullable.
//
// Measured on ClickHouse 25.8.29.51 with real columns in a real table,
// reading the VALUES and not only the types. The row held
// an = [1, NULL, 1]:
//
//	arrayDistinct(an)     Array(Int32)            [1]
//	arraySort(an)         Array(Nullable(Int32))  [1,1,NULL]
//	arraySlice(an,1,2)    Array(Nullable(Int32))  [1,NULL]
//	arrayResize(an,2)     Array(Nullable(Int32))  [1,NULL]
//
// The measurement went WIDER than that one cell, because a rule read
// from a single cell is the failure mode of this repository. The wider
// table below decides the SHAPE of the rule, and it refutes the simpler
// reading "arrayDistinct strips Nullable":
//
//	column                                 arrayDistinct answer
//	Array(Nullable(Int32))                 Array(Int32)
//	Array(Nullable(String))                Array(String)
//	Array(LowCardinality(String))          Array(String)
//	Array(LowCardinality(Nullable(Str)))   Array(String)
//	Array(Array(Nullable(Int32)))          Array(Array(Nullable(Int32)))
//	Array(Tuple(Nullable(Int32), String))  Array(Tuple(Nullable(Int32), String))
//	Array(Map(String, Nullable(Int32)))    Array(Map(String, Nullable(Int32)))
//
// Two facts come out of it:
//
//  1. The Nullable is removed at the TOP LEVEL of the element only. A
//     Nullable inside a nested Array, a Tuple or a Map stays, because
//     arrayDistinct compares whole elements and does not look inside a
//     container.
//  2. The LowCardinality removal is NOT arrayDistinct's rule. The three
//     neighbours remove it too (measured: arraySort(alc) and
//     arraySlice(alc,1,2) are both Array(String), and at depth
//     arraySort(Array(Array(LowCardinality(String)))) is
//     Array(Array(String))). That behaviour is shared, thus this change
//     must not touch it.
//
// The rule is therefore "remove the top-level Nullable of the element
// type", and nothing else.

const arrayDistinctElementSchema = `
CREATE TABLE probe (
	an Array(Nullable(Int32)),
	ans Array(Nullable(String)),
	ab Array(Int32),
	aan Array(Array(Nullable(Int32))),
	at Array(Tuple(Nullable(Int32), String)),
	am Array(Map(String, Nullable(Int32))),
	nan Nullable(String)
) ENGINE = Memory
`

func arrayDistinctElementTestSchema(t *testing.T) *Schema {
	t.Helper()
	schema, err := schemaFromDDLErr(t, arrayDistinctElementSchema)
	if err != nil {
		t.Fatalf("parse probe schema: %v", err)
	}
	return schema
}

// TestArrayDistinctDropsTheTopLevelElementNullable holds the measured
// answer of the ticket cell and of its String twin.
func TestArrayDistinctDropsTheTopLevelElementNullable(t *testing.T) {
	schema := arrayDistinctElementTestSchema(t)
	for _, testCase := range []struct{ expression, want string }{
		{"arrayDistinct(an)", "Array(Int32)"},
		{"arrayDistinct(ans)", "Array(String)"},
	} {
		result, err := inferTestExprType(t, schema, testCase.expression)
		if err != nil {
			t.Errorf("%s: refused a call that the server accepts: %v", testCase.expression, err)
			continue
		}
		if result != testCase.want {
			t.Errorf("%s: got %s, want %s", testCase.expression, result, testCase.want)
		}
	}
}

// TestArrayDistinctKeepsANullableInsideAContainer is the DISTINGUISHING
// test. A rule that removed every Nullable, at every depth, would pass
// the test above and fail here. Measured: arrayDistinct keeps the
// Nullable inside a nested Array, a Tuple and a Map, because it compares
// whole elements.
func TestArrayDistinctKeepsANullableInsideAContainer(t *testing.T) {
	schema := arrayDistinctElementTestSchema(t)
	for _, testCase := range []struct{ expression, want string }{
		{"arrayDistinct(aan)", "Array(Array(Nullable(Int32)))"},
		{"arrayDistinct(at)", "Array(Tuple(Nullable(Int32), String))"},
		{"arrayDistinct(am)", "Array(Map(String, Nullable(Int32)))"},
	} {
		result, err := inferTestExprType(t, schema, testCase.expression)
		if err != nil {
			t.Errorf("%s: refused a call that the server accepts: %v", testCase.expression, err)
			continue
		}
		if result != testCase.want {
			t.Errorf("%s: got %s, want %s: arrayDistinct removes the element Nullable at the TOP LEVEL only",
				testCase.expression, result, testCase.want)
		}
	}
}

// TestArrayDistinctNeighboursKeepTheElementNullable pins the behaviour
// that must NOT change. These three functions keep every element, thus
// they keep the element Nullable, and the ticket says so explicitly.
func TestArrayDistinctNeighboursKeepTheElementNullable(t *testing.T) {
	schema := arrayDistinctElementTestSchema(t)
	for _, testCase := range []struct{ expression, want string }{
		{"arraySort(an)", "Array(Nullable(Int32))"},
		{"arraySlice(an, 1, 2)", "Array(Nullable(Int32))"},
		{"arrayResize(an, 2)", "Array(Nullable(Int32))"},
		{"arraySort(ans)", "Array(Nullable(String))"},
	} {
		result, err := inferTestExprType(t, schema, testCase.expression)
		if err != nil {
			t.Errorf("%s: refused a call that the server accepts: %v", testCase.expression, err)
			continue
		}
		if result != testCase.want {
			t.Errorf("%s: got %s, want %s: this neighbour keeps the null in the data",
				testCase.expression, result, testCase.want)
		}
	}
}

// TestArrayDistinctLeavesABareElementAlone proves the rule does nothing
// when there is no element Nullable to remove. Measured:
// arrayDistinct(ab) is Array(Int32), as the column is.
func TestArrayDistinctLeavesABareElementAlone(t *testing.T) {
	schema := arrayDistinctElementTestSchema(t)
	result, err := inferTestExprType(t, schema, "arrayDistinct(ab)")
	if err != nil {
		t.Fatalf("arrayDistinct(ab): refused a call that the server accepts: %v", err)
	}
	if result != "Array(Int32)" {
		t.Errorf("arrayDistinct(ab): got %s, want Array(Int32)", result)
	}
}

// TestArrayDistinctKeepsTheWrapperOfTheArrayItself separates the ELEMENT
// Nullable from the Nullable of the ARRAY. The rule removes the first
// and must not touch the second: the argument domain and the wrapper
// class own that decision, and arrayDistinct(NULL) is still NULL.
func TestArrayDistinctKeepsTheWrapperOfTheArrayItself(t *testing.T) {
	schema := arrayDistinctElementTestSchema(t)
	// A non-array argument stays a refusal, thus the domain is not
	// widened by this change.
	if result, err := inferTestExprType(t, schema, "arrayDistinct(nan)"); err == nil {
		t.Errorf("arrayDistinct(nan): got %s, want a refusal: the server answers Code: 43", result)
	}
}
