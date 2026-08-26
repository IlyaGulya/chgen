package engine

import "testing"

// This file pins what a SCALAR function, an OPERATOR and a
// value-COMPUTING aggregate do with a
// SimpleAggregateFunction(f, Nullable(T)) argument.
//
// The neighbour file simple_aggregate_nullable_inner_test.go holds the
// same rule for the value-preserving AGGREGATES. This file holds the
// other side, which the regression found: the server READS THROUGH the
// marker and answers about the value inside it, thus the inner Nullable
// reaches the result. chgen did not see that Nullable at all, because
// the wrapper split that the scalar path uses removes LowCardinality and
// Nullable only and stops at a SimpleAggregateFunction.
//
// The rule is one sentence: an inner Nullable of the marker makes the
// value nullable, exactly as a top-level Nullable does. It is NOT a rule
// about the marker as such. Every cell below has a NEIGHBOUR cell with a
// non-Nullable inner type, and that neighbour keeps its old answer. A
// change that looks through the marker everywhere fails on the
// constructor cells at the end of this file, and a change that looks
// through it nowhere fails on the first block.
//
// Every expected value was measured on ClickHouse 25.8.29.51 through the
// HTTP interface, against real columns of a real table, never over
// literals, because the server folds constants. The rows were also
// SELECTed and not only read with toTypeName, because toTypeName is
// analysis and not execution.
const simpleAggregateNullableInnerScalarSchema = `
CREATE TABLE t (
    saf    SimpleAggregateFunction(anyLast, Int32),
    safn   SimpleAggregateFunction(anyLast, Nullable(Int32)),
    safs   SimpleAggregateFunction(anyLast, String),
    safns  SimpleAggregateFunction(anyLast, Nullable(String)),
    safarr SimpleAggregateFunction(anyLast, Array(Nullable(Int32))),
    i32    Int32,
    s      String
);
`

// TestSimpleAggregateNullableInnerReachesScalarResults holds the
// measured grid for the scalar and operator families.
func TestSimpleAggregateNullableInnerReachesScalarResults(t *testing.T) {
	schema, err := schemaFromDDLErr(t, simpleAggregateNullableInnerScalarSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	cases := []struct{ expr, want string }{
		// A value-COMPUTING scalar over a Nullable inner type gives a
		// Nullable result. The marker itself is gone, which chgen
		// already did; the Nullable is the part it lost.
		{"toInt8(safn)", "Nullable(Int8)"},
		{"toInt16(safn)", "Nullable(Int16)"},
		{"toInt32(safn)", "Nullable(Int32)"},
		{"toInt64(safn)", "Nullable(Int64)"},
		{"toUInt8(safn)", "Nullable(UInt8)"},
		{"toUInt16(safn)", "Nullable(UInt16)"},
		{"toUInt32(safn)", "Nullable(UInt32)"},
		{"toUInt64(safn)", "Nullable(UInt64)"},
		{"toFloat32(safn)", "Nullable(Float32)"},
		{"toFloat64(safn)", "Nullable(Float64)"},
		{"toString(safn)", "Nullable(String)"},
		{"toDate(safn)", "Nullable(Date)"},
		{"toDate32(safn)", "Nullable(Date32)"},
		{"toDateTime(safn)", "Nullable(DateTime)"},
		{"hex(safn)", "Nullable(String)"},
		{"cityHash64(safn)", "Nullable(UInt64)"},
		{"concat(safns, 'x')", "Nullable(String)"},
		{"length(safns)", "Nullable(UInt64)"},
		// The predicate family always answers plain UInt8, even over a
		// Bool operand; there is no Bool spelling here any more. Only
		// the Nullable is the subject of this test.
		{"empty(safns)", "Nullable(UInt8)"},
		{"notEmpty(safns)", "Nullable(UInt8)"},
		{"trim(safns)", "Nullable(String)"},
		{"trimLeft(safns)", "Nullable(String)"},
		{"trimRight(safns)", "Nullable(String)"},

		// NEIGHBOUR CELLS. The SAME functions over a NON-Nullable inner
		// type keep their old answer. These are what a blanket
		// "the marker always makes the result Nullable" rule breaks.
		{"toInt64(saf)", "Int64"},
		{"toString(saf)", "String"},
		{"toDate(saf)", "Date"},
		{"hex(saf)", "String"},
		{"cityHash64(saf)", "UInt64"},
		{"concat(safs, 'x')", "String"},
		{"length(safs)", "UInt64"},
		{"empty(safs)", "UInt8"},
		{"trim(safs)", "String"},

		// The comparison OPERATORS take the same Nullable. The server
		// says Nullable(UInt8) and so does chgen: the predicate class
		// always answers UInt8. The subject of these cells is the
		// Nullable alone.
		{"safn = i32", "Nullable(UInt8)"},
		{"safn != i32", "Nullable(UInt8)"},
		{"safn < i32", "Nullable(UInt8)"},
		{"safn <= i32", "Nullable(UInt8)"},
		{"safn > i32", "Nullable(UInt8)"},
		{"safn >= i32", "Nullable(UInt8)"},
		{"safns LIKE 'a%'", "Nullable(UInt8)"},
		{"safns NOT LIKE 'a%'", "Nullable(UInt8)"},
		{"safns ILIKE 'a%'", "Nullable(UInt8)"},
		{"safns || 'x'", "Nullable(String)"},

		// NEIGHBOUR CELLS for the operators.
		{"saf = i32", "UInt8"},
		{"saf > i32", "UInt8"},
		{"safs LIKE 'a%'", "UInt8"},
		{"safs || 'x'", "String"},

		// NEIGHBOUR CELL. assumeNotNull over a NON-Nullable inner type
		// keeps the marker, because there is no Nullable to remove.
		// The Nullable-inner half of this pair is NOT asserted here;
		// see the comment at the end of this file.
		{"assumeNotNull(saf)", "SimpleAggregateFunction(anyLast, Int32)"},

		// A value-COMPUTING aggregate already unwrapped the marker, and
		// it must keep the inner Nullable as well.
		{"sum(safn)", "Nullable(Int64)"},
		{"sum(saf)", "Int64"},
		{"avg(safn)", "Nullable(Float64)"},

		// NEIGHBOUR CELL. Over a non-Nullable inner type the whole
		// marker survives as the ELEMENT of groupArray. The
		// Nullable-inner half is NOT asserted here; see the comment at
		// the end of this file.
		{"groupArray(saf)", "Array(SimpleAggregateFunction(anyLast, Int32))"},

		// NEIGHBOUR CELLS. The container CONSTRUCTORS keep the marker
		// whole, inner Nullable and all. This is the trap: a rule that
		// looks through the marker in the split itself would give
		// Array(Nullable(Int32)) here, which the server does not say.
		{"array(safn)", "Array(SimpleAggregateFunction(anyLast, Nullable(Int32)))"},
		{"array(saf)", "Array(SimpleAggregateFunction(anyLast, Int32))"},
		{"tuple(safn, i32)", "Tuple(SimpleAggregateFunction(anyLast, Nullable(Int32)), Int32)"},
		{"map('k', safn)", "Map(String, SimpleAggregateFunction(anyLast, Nullable(Int32)))"},

		// NEIGHBOUR CELL. The Nullable must sit at the MARKER's own
		// level. An Array(Nullable(T)) inner type is not a nullable
		// value, thus length keeps its bare UInt64.
		{"length(safarr)", "UInt64"},
	}
	for _, testCase := range cases {
		got, err := inferSelectItemCHType(t, schema, "SELECT "+testCase.expr+" AS a FROM t")
		if err != nil {
			t.Errorf("%s: error = %v, want %q", testCase.expr, err, testCase.want)
			continue
		}
		if got != testCase.want {
			t.Errorf("%s = %q, want %q", testCase.expr, got, testCase.want)
		}
	}
}

// TWO CELLS OF THIS DEFECT ARE NOT ASSERTED ABOVE, and they are named
// here so they are not lost.
//
// assumeNotNull and the groupArray family are both wrapperOpaque: their
// own rules own the wrappers, thus the transport cannot correct them and
// the fix has to go in the rule. The two rules live in files that other
// tickets own at the time of the regression, thus this ticket measured them
// and left them:
//
//	withoutNullableFunctionArgument   infer_function.go
//	groupArrayFunctionResult          supertype.go
//
// Each rule removes the marker by ONE level and then answers about the
// inner type. Measured on ClickHouse 25.8.29.51 with real columns in a
// real table, never over literals (safn is
// SimpleAggregateFunction(anyLast, Nullable(Int32)), saf is
// SimpleAggregateFunction(anyLast, Int32), safns is
// SimpleAggregateFunction(anyLast, Nullable(String)), safarr is
// SimpleAggregateFunction(anyLast, Array(Nullable(Int32)))):
//
//	assumeNotNull(safn)     Int32
//	assumeNotNull(safns)    String
//	assumeNotNull(saf)      SimpleAggregateFunction(anyLast, Int32)
//	assumeNotNull(safarr)   Array(Nullable(Int32))
//	groupArray(safn)        Array(Int32)
//	groupArray(safns)       Array(String)
//	groupArray(saf)         Array(SimpleAggregateFunction(anyLast, Int32))
//	groupArray(safarr)      Array(Array(Nullable(Int32)))
//
// Note that assumeNotNull(safarr) and groupArray(safarr) both remove the
// marker although the inner type is NOT Nullable at the marker's level.
// The rule is therefore "look through the marker", and it is not the
// nullability rule that the rest of this file holds.
//
// chgen today answers the whole marker for the safn and safns rows,
// which is a silently wrong type. The neighbour rows above ARE asserted,
// so a later fix cannot make the non-Nullable half worse without
// failing.
