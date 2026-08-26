package engine

import "testing"

// hasMapNeedleSchema holds the columns that the regression measured: two Map
// key shapes (a String key and an Int32 key), and control columns.
const hasMapNeedleSchema = `
CREATE TABLE probe (
    i32 Int32,
    s String,
    m Map(String, Int64),
    m_is Map(Int32, String),
    arr_i Array(Int32)
) ENGINE = MergeTree ORDER BY i32
`

func hasMapNeedleTestSchema(t *testing.T) *Schema {
	t.Helper()
	return schemaFromDDL(t, hasMapNeedleSchema)
}

// TestHasRefusesANeedleThatCannotMatchTheMapKey covers the regression.
// checkHasElementPair already compares an Array haystack's element
// against the needle, but it leaves a Map haystack alone by design. The
// element to compare for a Map haystack is the KEY type, not the value
// type.
//
// Measured on ClickHouse 25.8.29.51 with a VALUE select (m is
// Map(String, Int64), m_is is Map(Int32, String)):
//
//	has(m, arr_i)     Code: 386   no supertype for String, Array(Int32)
//	has(m_is, arr_i)  Code: 386   no supertype for Int32, Array(Int32)
//	has(m, i32)       Code: 386   no supertype for String, Int32; the
//	                              KEY is String, thus an Int32 needle
//	                              is refused even though the Map VALUE
//	                              is Int64
func TestHasRefusesANeedleThatCannotMatchTheMapKey(t *testing.T) {
	schema := hasMapNeedleTestSchema(t)
	refused := []string{
		"has(m, arr_i)",
		"has(m_is, arr_i)",
		"has(m, i32)",
	}
	for _, exprSQL := range refused {
		inferred, err := inferTestExprType(t, schema, exprSQL)
		if err == nil {
			t.Errorf("%s: chgen answered %s, but the server refuses this call with Code: 386", exprSQL, inferred)
		}
	}
}

// TestHasAcceptsANeedleThatMatchesTheMapKey guards the other side of
// the regression: a needle that shares a comparable family with the Map KEY
// must keep its UInt8 result, and the Array haystack behaviour must not
// change.
//
// Measured on ClickHouse 25.8.29.51 with a VALUE select:
//
//	has(m, s)        UInt8   String needle against a String key
//	has(m_is, i32)   UInt8   Int32 needle against an Int32 key
//	has(arr_i, i32)  UInt8   unchanged Array behaviour
func TestHasAcceptsANeedleThatMatchesTheMapKey(t *testing.T) {
	schema := hasMapNeedleTestSchema(t)
	accepted := []string{
		"has(m, s)",
		"has(m_is, i32)",
		"has(arr_i, i32)",
	}
	for _, exprSQL := range accepted {
		inferred, err := inferTestExprType(t, schema, exprSQL)
		if err != nil {
			t.Errorf("%s: chgen refused a call that the server runs: %v", exprSQL, err)
			continue
		}
		if inferred != "UInt8" {
			t.Errorf("%s: chgen answered %s, want UInt8", exprSQL, inferred)
		}
	}
}
