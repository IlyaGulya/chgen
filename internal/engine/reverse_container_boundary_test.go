package engine

import "testing"

// reverseContainerSchema holds the columns for reverse container measurements:
// containers that reverse accepts, the Map it refuses, and the
// string-like and date columns that the temporal shift family reads.
const reverseContainerSchema = `
CREATE TABLE probe (
    k UInt8,
    s String,
    fs8 FixedString(8),
    lc LowCardinality(String),
    ns Nullable(String),
    lcn LowCardinality(Nullable(String)),
    arr_i Array(Int32),
    arr_s Array(String),
    arr_n Array(Nullable(Int32)),
    arr_lc Array(LowCardinality(String)),
    arr_arr Array(Array(Int32)),
    tup Tuple(Int32, String),
    tup3 Tuple(Int32, String, Date),
    ntup Tuple(x Int32, y String),
    mp Map(String, String),
    d Date,
    nd Nullable(Date),
    dt DateTime
) ENGINE = MergeTree ORDER BY k
`

func reverseContainerTestSchema(t *testing.T) *Schema {
	t.Helper()
	schema, err := schemaFromDDLErr(t, reverseContainerSchema)
	if err != nil {
		t.Fatalf("parse probe schema: %v", err)
	}
	return schema
}

// TestReverseAcceptsArrayAndTuple covers the regression.
//
// Before this fix reverse used stringArgumentDomain, so it refused every
// Array and every Tuple. The server RUNS those calls, and a refusal
// wider than the server's breaks a query that works.
//
// Measured on ClickHouse 25.8.29.51 over real columns of a real table,
// every cell confirmed by execution with ignore(), because toTypeName is
// analysis and is blind to a run-time refusal.
//
// The two shapes that an identity rule would get SILENTLY WRONG:
//
//   - an Array keeps its element type but LOSES an inner LowCardinality:
//     reverse(arr_lc) is Array(String), not Array(LowCardinality(String));
//   - a Tuple comes back with its ELEMENT ORDER reversed:
//     reverse(tup) is Tuple(String, Int32), not Tuple(Int32, String).
func TestReverseAcceptsArrayAndTuple(t *testing.T) {
	schema := reverseContainerTestSchema(t)
	cases := map[string]string{
		"reverse(arr_i)":   "Array(Int32)",
		"reverse(arr_s)":   "Array(String)",
		"reverse(arr_n)":   "Array(Nullable(Int32))",
		"reverse(arr_arr)": "Array(Array(Int32))",
		// The inner LowCardinality is stripped by the server.
		"reverse(arr_lc)": "Array(String)",
		// The element order is reversed.
		"reverse(tup)":  "Tuple(String, Int32)",
		"reverse(tup3)": "Tuple(Date, String, Int32)",
		// The text shapes stay as they were.
		"reverse(s)":   "String",
		"reverse(fs8)": "FixedString(8)",
	}
	for exprSQL, want := range cases {
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

// TestReverseKeepsNamedTupleNamesWithTheirElements pins the named-Tuple
// spelling. Measured: reverse(ntup) over Tuple(x Int32, y String) is
// Tuple(y String, x Int32) — the names travel with their own elements.
func TestReverseKeepsNamedTupleNamesWithTheirElements(t *testing.T) {
	schema := reverseContainerTestSchema(t)
	inferred, err := inferTestExprType(t, schema, "reverse(ntup)")
	if err != nil {
		t.Fatalf("reverse(ntup): chgen refused a call that the server runs: %v", err)
	}
	if want := "Tuple(y String, x Int32)"; inferred != want {
		t.Errorf("reverse(ntup): chgen answered %s, want %s", inferred, want)
	}
}

// TestReverseStillRefusesAMap is the control that stops the container
// widening from becoming "any container". Measured: reverse(mp) over a
// Map(String, String) column is Code: 43 at ANALYSIS and at EXECUTION.
func TestReverseStillRefusesAMap(t *testing.T) {
	schema := reverseContainerTestSchema(t)
	if inferred, err := inferTestExprType(t, schema, "reverse(mp)"); err == nil {
		t.Errorf("reverse(mp): chgen answered %s, but the server refuses this call with Code: 43", inferred)
	}
}

// TestTemporalShiftStillRefusesAStringColumn pins a refusal that must
// STAY, against the reading that calls it a defect because the server
// gives a type for it.
//
// addDays(s, 3) is DateTime64(3) at ANALYSIS, so a sweep that compares
// toTypeName only reports chgen as too narrow. Execution tells the other
// story. Measured on ClickHouse 25.8.29.51 over a real String column:
//
//	'2024-01-02 03:04:05'  ignore(addDays(s, 3))  RUNS
//	'garbage'              ignore(addDays(s, 3))  Code: 41
//	                                              CANNOT_PARSE_DATETIME
//
// and ONE unparseable row among good rows fails the WHOLE query. The
// success is a property of the DATA, not of the TYPE. A domain states
// what is true of every value of a type, so accepting String here would
// make chgen promise DateTime64(3) for a query that dies at run time,
// which is the silently wrong type the project ranks BELOW a refusal.
func TestTemporalShiftStillRefusesAStringColumn(t *testing.T) {
	schema := reverseContainerTestSchema(t)
	for _, exprSQL := range []string{
		"addDays(s, 3)",
		"addMinutes(lc, 3)",
		"addHours(ns, 3)",
		"subtractDays(lcn, 3)",
	} {
		if inferred, err := inferTestExprType(t, schema, exprSQL); err == nil {
			t.Errorf("%s: chgen answered %s, but the server can only run this "+
				"when every row parses as a date-time (Code: 41 otherwise); "+
				"the refusal must stay", exprSQL, inferred)
		}
	}
	// The measured temporal domain still works, so the refusal above is
	// not a dead function.
	if inferred, err := inferTestExprType(t, schema, "addDays(dt, 3)"); err != nil {
		t.Errorf("addDays(dt, 3): chgen refused a call the server runs: %v", err)
	} else if inferred != "DateTime" {
		t.Errorf("addDays(dt, 3): chgen answered %s, want DateTime", inferred)
	}
}

// TestToStartOfHourAndMinuteStillRefuseANullableDate pins the two cells
// that the axis reports as a too-narrow and that are CORRECT.
//
// Measured on ClickHouse 25.8.29.51: toStartOfHour(nd) over a
// Nullable(Date) column gives Nullable(DateTime) at ANALYSIS but
// Code: 43 at EXECUTION, and the plain Date column behaves the same way.
// The axis compares toTypeName only, so it cannot see this. chgen is
// right and the axis is wrong; the domain must NOT be relaxed here.
func TestToStartOfHourAndMinuteStillRefuseANullableDate(t *testing.T) {
	schema := reverseContainerTestSchema(t)
	for _, exprSQL := range []string{
		"toStartOfHour(nd)",
		"toStartOfMinute(nd)",
		"toStartOfHour(d)",
		"toStartOfMinute(d)",
	} {
		if inferred, err := inferTestExprType(t, schema, exprSQL); err == nil {
			t.Errorf("%s: chgen answered %s, but the server refuses this call "+
				"with Code: 43 at execution; the refusal must stay", exprSQL, inferred)
		}
	}
	// The DateTime argument still works.
	if inferred, err := inferTestExprType(t, schema, "toStartOfHour(dt)"); err != nil {
		t.Errorf("toStartOfHour(dt): chgen refused a call the server runs: %v", err)
	} else if inferred != "DateTime" {
		t.Errorf("toStartOfHour(dt): chgen answered %s, want DateTime", inferred)
	}
}
