package engine

import "testing"

// hexDateTimeEnumSchema holds the columns that the regression measured:
// DateTime64 (plain and with a timezone), Enum8, Enum16, and control
// columns that hex must keep accepting.
const hexDateTimeEnumSchema = `
CREATE TABLE probe (
    i32 Int32,
    s String,
    f64 Float64,
    fs FixedString(8),
    dt Date,
    dtm DateTime,
    dt64 DateTime64(3),
    dtz64 DateTime64(6, 'UTC'),
    e8 Enum8('a' = 1, 'zz' = 2),
    e16 Enum16('x' = 1, 'y' = 2)
) ENGINE = MergeTree ORDER BY i32
`

func hexDateTimeEnumTestSchema(t *testing.T) *Schema {
	t.Helper()
	return schemaFromDDL(t, hexDateTimeEnumSchema)
}

// TestHexRefusesDateTime64AndEnum covers the regression. Measured on
// ClickHouse 25.8.29.51 with a VALUE select, never toTypeName alone:
//
//	hex(dt64)   Code: 44   Illegal column DateTime64 of argument of hex
//	hex(dtz64)  Code: 44   same message
//	hex(e8)     Code: 43   Illegal type Enum8(...) of argument of hex
//	hex(e16)    Code: 43   same message, Enum16
//
// The two codes differ, thus this test carries both facts, and neither
// one may collapse into the other.
func TestHexRefusesDateTime64AndEnum(t *testing.T) {
	schema := hexDateTimeEnumTestSchema(t)
	refused := []string{
		"hex(dt64)",
		"hex(dtz64)",
		"hex(e8)",
		"hex(e16)",
	}
	for _, exprSQL := range refused {
		inferred, err := inferTestExprType(t, schema, exprSQL)
		if err == nil {
			t.Errorf("%s: chgen answered %s, but the server refuses this call", exprSQL, inferred)
		}
	}
}

// TestHexKeepsItsWideDomain guards the other side of the regression. hex
// reads raw bytes for most types on purpose (the regression), and the fix for
// DateTime64 and Enum must not narrow that. Measured on ClickHouse
// 25.8.29.51 with a VALUE select: every column below RUNS.
func TestHexKeepsItsWideDomain(t *testing.T) {
	schema := hexDateTimeEnumTestSchema(t)
	accepted := map[string]string{
		"hex(i32)": "String",
		"hex(s)":   "String",
		"hex(fs)":  "String",
		"hex(f64)": "String",
		"hex(dt)":  "String",
		"hex(dtm)": "String",
	}
	for exprSQL, want := range accepted {
		inferred, err := inferTestExprType(t, schema, exprSQL)
		if err != nil {
			t.Errorf("%s: chgen refused a call that the server runs: %v", exprSQL, err)
			continue
		}
		if inferred != want {
			t.Errorf("%s: chgen answered %s, want %s", exprSQL, inferred, want)
		}
	}
}
