package engine

import "testing"

// innerLowCardinalitySchema holds SimpleAggregateFunction columns whose
// INNER type is LowCardinality, together with the plain LowCardinality
// columns that give the control answers.
const innerLowCardinalitySchema = `
CREATE TABLE probe (
    saflc_i32 SimpleAggregateFunction(anyLast, LowCardinality(Int32)),
    saflc_u64 SimpleAggregateFunction(anyLast, LowCardinality(UInt64)),
    saflc_s   SimpleAggregateFunction(anyLast, LowCardinality(String)),
    saflc_fs  SimpleAggregateFunction(anyLast, LowCardinality(FixedString(8))),
    saf_i32   SimpleAggregateFunction(anyLast, Int32),
    saf_s     SimpleAggregateFunction(anyLast, String),
    lc_i32    LowCardinality(Int32),
    lc_s      LowCardinality(String),
    dt        DateTime
) ENGINE = AggregatingMergeTree ORDER BY tuple();
`

func innerLowCardinalityTestSchema(t *testing.T) *Schema {
	t.Helper()
	schema, err := schemaFromDDLErr(t, innerLowCardinalitySchema)
	if err != nil {
		t.Fatalf("parse schema: %v", err)
	}
	return schema
}

// TestSimpleAggregateInnerLowCardinalityReachesResult pins the measured
// rule that a LowCardinality INSIDE a SimpleAggregateFunction marker is
// a transport wrapper exactly as one outside the marker.
//
// A function that COMPUTES a new value drops the marker. The server then
// answers about the value inside the marker, and that value is
// LowCardinality, thus the result carries the wrapper. Before this rule
// the inner LowCardinality was invisible to the flag that decides the
// result wrapper, because splitCHWrappers reports the wrappers ON the
// type and this one lives in the inner type. The result was a bare base
// type where the server answers a LowCardinality one.
//
// The plain LowCardinality column gives the control answer on the same
// row. The two agree on the server, thus they must agree in chgen.
//
// Measured on ClickHouse 25.8.29.51 with real columns of a real
// AggregatingMergeTree table with one row, never over literals, because
// the server folds constants.
func TestSimpleAggregateInnerLowCardinalityReachesResult(t *testing.T) {
	schema := innerLowCardinalityTestSchema(t)

	cases := []struct {
		expr string
		want string
	}{
		// The conversion family. Measured:
		//	toString(saflc_i32)  LowCardinality(String)
		//	toUInt64(saflc_i32)  LowCardinality(UInt64)
		//	toInt32(saflc_u64)   LowCardinality(Int32)
		//	toFloat64(saflc_i32) LowCardinality(Float64)
		{"toString(saflc_i32)", "LowCardinality(String)"},
		{"toUInt64(saflc_i32)", "LowCardinality(UInt64)"},
		{"toInt32(saflc_u64)", "LowCardinality(Int32)"},
		{"toFloat64(saflc_i32)", "LowCardinality(Float64)"},

		// The transparent computing family. Measured:
		//	hex(saflc_i32)        LowCardinality(String)
		//	length(saflc_s)       LowCardinality(UInt64)
		//	trim(saflc_s)         LowCardinality(String)
		//	cityHash64(saflc_i32) LowCardinality(UInt64)
		{"hex(saflc_i32)", "LowCardinality(String)"},
		{"length(saflc_s)", "LowCardinality(UInt64)"},
		{"trim(saflc_s)", "LowCardinality(String)"},
		{"cityHash64(saflc_i32)", "LowCardinality(UInt64)"},

		// The LIKE operator family. Measured:
		//	saflc_s LIKE 'a'  LowCardinality(UInt8)
		{"saflc_s LIKE 'a'", "LowCardinality(UInt8)"},

		// The control cells. A PLAIN LowCardinality column answers the
		// same type, thus the marker must not change the answer.
		{"toString(lc_i32)", "LowCardinality(String)"},
		{"hex(lc_i32)", "LowCardinality(String)"},
		{"length(lc_s)", "LowCardinality(UInt64)"},
		{"lc_s LIKE 'a'", "LowCardinality(UInt8)"},

		// A marker with a BARE inner type carries no LowCardinality,
		// thus the result must stay bare. This cell separates "lift the
		// inner LowCardinality" from "always wrap the result".
		// Measured: toString(saf_i32) is String, length(saf_s) is
		// UInt64.
		{"toString(saf_i32)", "String"},
		{"length(saf_s)", "UInt64"},
	}

	for _, testCase := range cases {
		t.Run(testCase.expr, func(t *testing.T) {
			got, err := inferTestExprType(t, schema, testCase.expr)
			if err != nil {
				t.Fatalf("infer %q: %v", testCase.expr, err)
			}
			if got != testCase.want {
				t.Fatalf("infer %q = %s, want %s", testCase.expr, got, testCase.want)
			}
		})
	}
}
