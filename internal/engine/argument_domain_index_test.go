package engine

import (
	"strings"
	"testing"
)

// The tests below pin the measured behaviour of dateDiff and date_diff.
//
// Every answer comes from ClickHouse 25.8.29.51 with real columns in a
// real table, never a literal, because the server folds constants and a
// rule measured over a literal reports the wrong domain. The probe table
// held one row:
//
//	d Date, d32 Date32, dt DateTime, dt64 DateTime64(3),
//	nd Nullable(Date), i32 Int32, u64 UInt64, f64 Float64, s String
//
// The measurement covered the ACCEPTING side first and widely, because a
// domain that is too NARROW turns a working query into a refusal.

// dateDiffProbeSchema holds one column for each type that the dateDiff
// measurement covered.
const dateDiffProbeSchema = `
CREATE TABLE probe (
	d Date,
	d32 Date32,
	dt DateTime,
	dt64 DateTime64(3),
	nd Nullable(Date),
	i32 Int32,
	u64 UInt64,
	f64 Float64,
	s String
) ENGINE = Memory
`

func dateDiffProbeTestSchema(t *testing.T) *Schema {
	t.Helper()
	schema, err := schemaFromDDLErr(t, dateDiffProbeSchema)
	if err != nil {
		t.Fatalf("parse probe schema: %v", err)
	}
	return schema
}

// TestDateDiffAcceptsEveryMeasuredDatePair proves the ACCEPTING side.
// This is the direction that the too-narrow trap breaks: each of these
// 28 calls returns a type on the server, thus none may become a refusal.
//
// Measured, every one accepted:
//
//	dateDiff('day', d,    d|d32|dt|dt64)  Int64
//	dateDiff('day', d32,  d|d32|dt|dt64)  Int64
//	dateDiff('day', dt,   d|d32|dt|dt64)  Int64
//	dateDiff('day', dt64, d|d32|dt|dt64)  Int64
//	dateDiff('day', nd,   d|d32|dt|dt64)  Nullable(Int64)
func TestDateDiffAcceptsEveryMeasuredDatePair(t *testing.T) {
	schema := dateDiffProbeTestSchema(t)
	dates := []string{"d", "d32", "dt", "dt64"}
	for _, name := range []string{"dateDiff", "date_diff"} {
		for _, start := range dates {
			for _, end := range dates {
				expression := name + "('day', " + start + ", " + end + ")"
				result, err := inferTestExprType(t, schema, expression)
				if err != nil {
					t.Errorf("%s: refused a call that the server accepts: %v", expression, err)
					continue
				}
				if result != "Int64" {
					t.Errorf("%s: got %s, want Int64", expression, result)
				}
			}
		}
	}
}

// TestDateDiffKeepsNullableOfADateArgument pins the wrapper answer of the
// accepting side. Measured: dateDiff('day', nd, d) is Nullable(Int64).
func TestDateDiffKeepsNullableOfADateArgument(t *testing.T) {
	schema := dateDiffProbeTestSchema(t)
	for _, name := range []string{"dateDiff", "date_diff"} {
		expression := name + "('day', nd, d)"
		result, err := inferTestExprType(t, schema, expression)
		if err != nil {
			t.Fatalf("%s: refused a call that the server accepts: %v", expression, err)
		}
		if result != "Nullable(Int64)" {
			t.Errorf("%s: got %s, want Nullable(Int64)", expression, result)
		}
	}
}

// TestDateDiffAcceptsEveryMeasuredUnit proves that the domain does not
// reach argument zero. Every unit below returned Int64 on the server, and
// the unit is a String, thus a domain applied to argument zero would
// refuse all 21 of them.
func TestDateDiffAcceptsEveryMeasuredUnit(t *testing.T) {
	schema := dateDiffProbeTestSchema(t)
	units := []string{
		"nanosecond", "microsecond", "millisecond", "second", "minute",
		"hour", "day", "week", "month", "quarter", "year",
		"ns", "us", "ms", "s", "m", "h", "d", "w", "q", "y",
	}
	for _, unit := range units {
		expression := "dateDiff('" + unit + "', dt64, dt64)"
		result, err := inferTestExprType(t, schema, expression)
		if err != nil {
			t.Errorf("%s: refused a unit that the server accepts: %v", expression, err)
			continue
		}
		if result != "Int64" {
			t.Errorf("%s: got %s, want Int64", expression, result)
		}
	}
}

// TestDateDiffAcceptsTheOptionalTimezoneArgument proves that the domain
// does not reach argument three. Measured:
//
//	dateDiff('day', d, dt, 'UTC')  Int64
//
// The timezone is a String, thus a domain applied to every argument, and
// not to the named pair only, would refuse this legal call.
func TestDateDiffAcceptsTheOptionalTimezoneArgument(t *testing.T) {
	schema := dateDiffProbeTestSchema(t)
	for _, name := range []string{"dateDiff", "date_diff"} {
		expression := name + "('day', d, dt, 'UTC')"
		result, err := inferTestExprType(t, schema, expression)
		if err != nil {
			t.Fatalf("%s: refused a call that the server accepts: %v", expression, err)
		}
		if result != "Int64" {
			t.Errorf("%s: got %s, want Int64", expression, result)
		}
	}
}

// TestDateDiffRefusesANonDateInEitherDatePosition is the refusing side.
// These are the 24 grid cells that carried CHGEN_TYPES_SERVER_REFUSES.
//
// Measured, every one Code: 43. The server names the position, which is
// why BOTH positions need the check:
//
//	dateDiff('day', i32, d)   "illegal type ... as 2nd argument 'startdate'"
//	dateDiff('day', d, i32)   "illegal type ... as 3rd argument 'enddate'"
func TestDateDiffRefusesANonDateInEitherDatePosition(t *testing.T) {
	schema := dateDiffProbeTestSchema(t)
	nonDates := []string{"i32", "u64", "f64", "s"}
	for _, name := range []string{"dateDiff", "date_diff"} {
		for _, bad := range nonDates {
			// The start position.
			expression := name + "('day', " + bad + ", d)"
			if result, err := inferTestExprType(t, schema, expression); err == nil {
				t.Errorf("%s: got %s, want a refusal (server answers Code: 43)", expression, result)
			}
			// The end position. This is the case that a single
			// argument index could not catch.
			expression = name + "('day', d, " + bad + ")"
			if result, err := inferTestExprType(t, schema, expression); err == nil {
				t.Errorf("%s: got %s, want a refusal (server answers Code: 43)", expression, result)
			}
		}
	}
}

// TestDateDiffRefusesACallThatIsTooShort pins the deliberate decision
// about an arity below a named index. The domain names arguments one and
// two, thus a call without them cannot be checked. Such a call is a
// refusal and not a silent skip: an answer of Int64 for dateDiff('day')
// would be a type for a call that the server cannot run.
func TestDateDiffRefusesACallThatIsTooShort(t *testing.T) {
	schema := dateDiffProbeTestSchema(t)
	for _, expression := range []string{"dateDiff('day')", "dateDiff('day', d)"} {
		result, err := inferTestExprType(t, schema, expression)
		if err == nil {
			t.Errorf("%s: got %s, want a refusal: the domain cannot check a missing argument", expression, result)
			continue
		}
		if !strings.Contains(err.Error(), "position") {
			t.Errorf("%s: refusal should name the missing position, got: %v", expression, err)
		}
	}
}

// TestDomainArgumentIndexesDefaultsToArgumentZero pins the general
// mechanism. A function that names no list keeps the behaviour that held
// before dateDiff: the domain applies to argument zero only.
func TestDomainArgumentIndexesDefaultsToArgumentZero(t *testing.T) {
	for _, name := range []string{"sum", "avg", "lower", "arraydistinct", "substring"} {
		indexes := domainArgumentIndexes(name)
		if len(indexes) != 1 || indexes[0] != 0 {
			t.Errorf("%s: got %v, want [0]", name, indexes)
		}
	}
	// An unknown name also answers argument zero, so the accessor never
	// returns an empty list that a caller would read as "check nothing".
	if indexes := domainArgumentIndexes("no_such_function"); len(indexes) != 1 || indexes[0] != 0 {
		t.Errorf("unknown name: got %v, want [0]", indexes)
	}
}

// TestDateDiffNamesBothDateArguments pins the registry fact itself, so a
// later edit cannot drop one of the two positions and leave the other
// checked. Dropping the end position would restore the silent wrong type
// for dateDiff('day', d, i32).
func TestDateDiffNamesBothDateArguments(t *testing.T) {
	for _, name := range []string{"datediff", "date_diff"} {
		if _, constrained := argumentDomainFor(name); !constrained {
			t.Errorf("%s: declares no argument domain; the date set is measured and must be attached", name)
		}
		indexes := domainArgumentIndexes(name)
		if len(indexes) != 2 || indexes[0] != 1 || indexes[1] != 2 {
			t.Errorf("%s: got %v, want [1 2]: the unit is argument zero and the timezone is argument three", name, indexes)
		}
	}
}
