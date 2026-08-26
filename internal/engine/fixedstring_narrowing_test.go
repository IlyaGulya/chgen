package engine

import "testing"

// fixedStringNarrowSchema holds the columns that the regression measured: two
// FixedString widths and a plain String, so that the rule can be told
// apart from a blanket String refusal.
const fixedStringNarrowSchema = `
CREATE TABLE probe (
    i32 Int32,
    s String,
    fs FixedString(8),
    fs16 FixedString(16)
) ENGINE = MergeTree ORDER BY i32
`

func fixedStringNarrowTestSchema(t *testing.T) *Schema {
	t.Helper()
	return schemaFromDDL(t, fixedStringNarrowSchema)
}

// TestToFixedStringRefusesNarrowingASource covers the regression. Measured on
// ClickHouse 25.8.29.51 with a VALUE select (DESCRIBE alone answers a
// type here, but the server still refuses at run time):
//
//	toFixedString(fs, 3)     Code: 131   String too long for type
//	                                     FixedString(3); source is
//	                                     FixedString(8)
//	toFixedString(fs16, 3)   Code: 131   source is FixedString(16)
//	toFixedString(fs16, 15)  Code: 131   source is FixedString(16),
//	                                     target still below source
//
// The refusal holds for every row, including a short value that would
// fit ('ab' in FixedString(15)), because the server compares the
// DECLARED widths and never the value.
func TestToFixedStringRefusesNarrowingASource(t *testing.T) {
	schema := fixedStringNarrowTestSchema(t)
	refused := []string{
		"toFixedString(fs, 3)",
		"toFixedString(fs16, 3)",
		"toFixedString(fs16, 15)",
	}
	for _, exprSQL := range refused {
		inferred, err := inferTestExprType(t, schema, exprSQL)
		if err == nil {
			t.Errorf("%s: chgen answered %s, but the server refuses this call with Code: 131", exprSQL, inferred)
		}
	}
}

// TestToFixedStringAcceptsAWideningOrEqualTarget guards the other side of
// the regression. A target width at or above the source width must keep
// working, and a String source must never be refused by type: the
// server's Code: 131 for a String source is a VALUE error (the value did
// not fit), not a type refusal, so this domain must not touch it.
//
// Measured on ClickHouse 25.8.29.51 with a VALUE select:
//
//	toFixedString(fs, 8)     FixedString(8)    equal width
//	toFixedString(fs, 16)    FixedString(16)   widening
//	toFixedString(fs16, 16)  FixedString(16)   equal width
//	toFixedString(fs16, 20)  FixedString(20)   widening
//	toFixedString(s, 3)      FixedString(3)    String source, RUNS
func TestToFixedStringAcceptsAWideningOrEqualTarget(t *testing.T) {
	schema := fixedStringNarrowTestSchema(t)
	accepted := map[string]string{
		"toFixedString(fs, 8)":    "FixedString(8)",
		"toFixedString(fs, 16)":   "FixedString(16)",
		"toFixedString(fs16, 16)": "FixedString(16)",
		"toFixedString(fs16, 20)": "FixedString(20)",
		"toFixedString(s, 3)":     "FixedString(3)",
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
