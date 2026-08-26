package engine

import (
	"strings"
	"testing"
)

// temporalNumericStringSchema holds one column of every base type this test file needs.
// dtb carries an explicit Europe/Berlin zone; d64 carries an explicit UTC
// zone with millisecond precision, matching the probe columns the regression
// and the regression measured against ClickHouse 25.8.29.51.
const temporalNumericStringSchema = `
CREATE TABLE probe (
    k UInt8,
    d1 Decimal(9, 2),
    d2 Decimal(18, 4),
    fs8 FixedString(8),
    s String,
    dtb DateTime('Europe/Berlin'),
    d64 DateTime64(3, 'UTC'),
    dt DateTime,
    e8 Enum8('a' = 1, 'b' = 2),
    e16 Enum16('a' = 1, 'b' = 2),
    sagg SimpleAggregateFunction(sum, Int64)
) ENGINE = AggregatingMergeTree ORDER BY k
`

func temporalNumericStringTestSchema(t *testing.T) *Schema {
	t.Helper()
	schema, err := schemaFromDDLErr(t, temporalNumericStringSchema)
	if err != nil {
		t.Fatalf("parse probe schema: %v", err)
	}
	return schema
}

// TestToStartOfHourAndMinuteCarryTheArgumentZone covers the regression.
//
// Measured on ClickHouse 25.8.29.51 with DESCRIBE over the probe table:
//
//	toStartOfHour(d64)     DateTime('UTC')
//	toStartOfMinute(d64)   DateTime('UTC')
//	toStartOfHour(dtb)     DateTime('Europe/Berlin')
//
// The result is DateTime, never DateTime64, even for a DateTime64
// argument: the sub-second precision is dropped and the zone survives.
func TestToStartOfHourAndMinuteCarryTheArgumentZone(t *testing.T) {
	schema := temporalNumericStringTestSchema(t)
	cases := map[string]string{
		"toStartOfHour(d64)":   "DateTime('UTC')",
		"toStartOfMinute(d64)": "DateTime('UTC')",
		"toStartOfHour(dtb)":   "DateTime('Europe/Berlin')",
		"toStartOfMinute(dtb)": "DateTime('Europe/Berlin')",
		"toStartOfHour(dt)":    "DateTime",
		"toStartOfMinute(dt)":  "DateTime",
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

// TestToStartOfHourAndMinuteRefuseADateArgument is the CONTROL for
// TestToStartOfHourAndMinuteCarryTheArgumentZone. toStartOfHour and
// toStartOfMinute refuse a Date argument, even though toStartOfDay
// accepts one and even though the server's OWN refusal message for
// this cell claims Date is accepted ("Should be Date, Date32, DateTime
// or DateTime64"). Measured on ClickHouse 25.8.29.51:
//
//	toStartOfHour(toDate(dtb))    Code: 43
//	toStartOfMinute(toDate(dtb))  Code: 43
//	toStartOfDay(toDate(dtb))     DateTime      (the wider neighbour RUNS)
//
// If hourMinuteStartOfArgumentDomain were relaxed to dateArgumentDomain
// (toStartOfDay's domain), this test would start failing where it
// currently passes: it would stop refusing toStartOfHour(Date), which
// the live server still refuses.
func TestToStartOfHourAndMinuteRefuseADateArgument(t *testing.T) {
	schema := temporalNumericStringTestSchema(t)
	refused := []string{
		"toStartOfHour(toDate(dtb))",
		"toStartOfMinute(toDate(dtb))",
		"toStartOfHour(s)",
		"toStartOfMinute(e8)",
	}
	for _, exprSQL := range refused {
		inferred, err := inferTestExprType(t, schema, exprSQL)
		if err == nil {
			t.Errorf("%s: chgen answered %s, but the server refuses this call with Code: 43", exprSQL, inferred)
		}
	}
	// The control: toStartOfDay must still accept a Date argument. A
	// rule that accidentally narrowed toStartOfDay itself would still
	// pass the refusal test above but fail here.
	if inferred, err := inferTestExprType(t, schema, "toStartOfDay(toDate(dtb))"); err != nil {
		t.Errorf("toStartOfDay(toDate(dtb)): chgen refused a call the server runs: %v", err)
	} else if inferred != "DateTime" {
		t.Errorf("toStartOfDay(toDate(dtb)): chgen answered %s, want DateTime", inferred)
	}
}

// TestAddSecondsAndAddDaysPreserveShapeAndZone covers the regression's
// addDays/addSeconds cells, plus their siblings across the
// temporalShiftFunctions family. Measured on ClickHouse 25.8.29.51 with
// DESCRIBE over the probe table:
//
//	addDays(dtb, 1)      DateTime('Europe/Berlin')
//	addSeconds(d64, 1)   DateTime64(3, 'UTC')
//	addHours(dtb, 1)     DateTime('Europe/Berlin')
//	addMonths(d64, 1)    DateTime64(3, 'UTC')
//	subtractDays(dtb, 1) DateTime('Europe/Berlin')
//
// Every add*/subtract* name gives back the EXACT shape of its DateTime or
// DateTime64 argument: the zone and the precision both survive, whatever
// the shift unit is. This is the family fact that justifies one shared
// rule (inferTemporalShiftType) instead of sixteen separate ones.
func TestAddSecondsAndAddDaysPreserveShapeAndZone(t *testing.T) {
	schema := temporalNumericStringTestSchema(t)
	cases := map[string]string{
		"addDays(dtb, 1)":         "DateTime('Europe/Berlin')",
		"addSeconds(d64, 1)":      "DateTime64(3, 'UTC')",
		"addHours(dtb, 1)":        "DateTime('Europe/Berlin')",
		"addMinutes(d64, 1)":      "DateTime64(3, 'UTC')",
		"addMonths(dtb, 1)":       "DateTime('Europe/Berlin')",
		"addMonths(d64, 1)":       "DateTime64(3, 'UTC')",
		"addQuarters(dtb, 1)":     "DateTime('Europe/Berlin')",
		"addWeeks(dtb, 1)":        "DateTime('Europe/Berlin')",
		"addYears(d64, 1)":        "DateTime64(3, 'UTC')",
		"subtractDays(dtb, 1)":    "DateTime('Europe/Berlin')",
		"subtractSeconds(d64, 1)": "DateTime64(3, 'UTC')",
		"subtractHours(dtb, 1)":   "DateTime('Europe/Berlin')",
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

// TestAddDaysPromotesADateOnlyUnderASubDayUnit is the CONTROL that
// separates the two temporalShiftFunctions groups: a Date argument STAYS
// Date under a day-or-larger unit but PROMOTES to DateTime under an
// Hour, Minute or Second unit. Measured on ClickHouse 25.8.29.51 with
// DESCRIBE over the probe table:
//
//	addDays(toDate(dtb), 1)     Date        (day-or-larger: stays Date)
//	addMonths(toDate(dtb), 1)   Date
//	addSeconds(toDate(dtb), 1)  DateTime    (sub-day: promotes)
//	addHours(toDate(dtb), 1)    DateTime
//
// A rule that used one shape for the whole family (either always
// preserving Date, or always promoting it) would be silently wrong on
// one side of this boundary.
func TestAddDaysPromotesADateOnlyUnderASubDayUnit(t *testing.T) {
	schema := temporalNumericStringTestSchema(t)
	cases := map[string]string{
		"addDays(toDate(dtb), 1)":      "Date",
		"addMonths(toDate(dtb), 1)":    "Date",
		"addWeeks(toDate(dtb), 1)":     "Date",
		"subtractDays(toDate(dtb), 1)": "Date",
		"addSeconds(toDate(dtb), 1)":   "DateTime",
		"addHours(toDate(dtb), 1)":     "DateTime",
		"addMinutes(toDate(dtb), 1)":   "DateTime",
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

// TestAddDaysRefusesANonTemporalArgument is a refusal-side control for
// the add*/subtract* family. A String argument gives a type at ANALYSIS
// time on the live server (addDays(String, 1) is DateTime64(3)) but
// fails at EXECUTION on a value the parser cannot read as a date
// (measured: Code: 41 CANNOT_PARSE_DATETIME over a real 'ab' column).
// chgen refuses String here rather than crediting that execution-fragile
// analysis answer; a Decimal and a FixedString are refused by the server
// at every stage and chgen must refuse them too.
func TestAddDaysRefusesANonTemporalArgument(t *testing.T) {
	schema := temporalNumericStringTestSchema(t)
	refused := []string{
		"addDays(s, 1)",
		"addDays(d1, 1)",
		"addDays(fs8, 1)",
		"addSeconds(fs8, 1)",
	}
	for _, exprSQL := range refused {
		inferred, err := inferTestExprType(t, schema, exprSQL)
		if err == nil {
			t.Errorf("%s: chgen answered %s; the server either refuses this at analysis or the answer only "+
				"holds at analysis time and fails on execution, so chgen must refuse it", exprSQL, inferred)
		}
	}
}

// TestSumWithOverflowKeepsTheExactDecimalShape covers the regression.
// Measured on ClickHouse 25.8.29.51 with DESCRIBE over the probe table:
//
//	sumWithOverflow(d2)   Decimal(18, 4)   the EXACT input shape
//	sum(d2)               Decimal(38, 4)   sum WIDENS the precision
//
// sumWithOverflow never widens; sum always does. Registering
// sumWithOverflow with sum's own rule (sumFunctionArgument) would be a
// silently wrong, too-wide precision.
func TestSumWithOverflowKeepsTheExactDecimalShape(t *testing.T) {
	schema := temporalNumericStringTestSchema(t)
	if inferred, err := inferTestExprType(t, schema, "sumWithOverflow(d2)"); err != nil {
		t.Errorf("sumWithOverflow(d2): chgen refused a call the server runs: %v", err)
	} else if inferred != "Decimal(18, 4)" {
		t.Errorf("sumWithOverflow(d2): chgen answered %s, want Decimal(18, 4)", inferred)
	}
}

// TestSumWidensDecimalPrecisionUnlikeSumWithOverflow is the CONTROL for
// TestSumWithOverflowKeepsTheExactDecimalShape: the neighbouring
// aggregate sum(d2) must still WIDEN to Decimal(38, 4), so a rule that
// accidentally reused sumWithOverflow's identity behaviour for sum
// itself would fail here.
func TestSumWidensDecimalPrecisionUnlikeSumWithOverflow(t *testing.T) {
	schema := temporalNumericStringTestSchema(t)
	if inferred, err := inferTestExprType(t, schema, "sum(d2)"); err != nil {
		t.Errorf("sum(d2): chgen refused a call the server runs: %v", err)
	} else if inferred != "Decimal(38, 4)" {
		t.Errorf("sum(d2): chgen answered %s, want Decimal(38, 4)", inferred)
	}
}

// TestSumWithOverflowOnEnumGivesTheUnderlyingWidth is a second control:
// sumWithOverflow does NOT behave like a plain identity rule on an Enum
// argument. Measured on ClickHouse 25.8.29.51:
//
//	sumWithOverflow(e8)    Int8    (the Enum8 underlying width, not Enum8 itself)
//	sumWithOverflow(e16)   Int16
//	sum(e8)                Int64   (sum widens the Enum to Int64 instead)
func TestSumWithOverflowOnEnumGivesTheUnderlyingWidth(t *testing.T) {
	schema := temporalNumericStringTestSchema(t)
	cases := map[string]string{
		"sumWithOverflow(e8)":  "Int8",
		"sumWithOverflow(e16)": "Int16",
		"sum(e8)":              "Int64",
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

// TestRoundFamilyKeepsTheExactDecimalShape covers the regression's
// round(d2, 1) cell and its siblings round, roundBankers, floor, ceil
// and trunc/truncate all share one measured shape: the FIRST argument's
// type comes back unchanged, whatever the digits argument says. Measured
// on ClickHouse 25.8.29.51 with DESCRIBE over the probe table:
//
//	round(d2, 1)         Decimal(18, 4)
//	round(d2, 0)         Decimal(18, 4)
//	round(d2, 10)        Decimal(18, 4)
//	roundBankers(d2, 1)  Decimal(18, 4)
//	floor(d2, 1)         Decimal(18, 4)
//	ceil(d2, 1)          Decimal(18, 4)
//	trunc(d2, 1)         Decimal(18, 4)
func TestRoundFamilyKeepsTheExactDecimalShape(t *testing.T) {
	schema := temporalNumericStringTestSchema(t)
	cases := []string{
		"round(d2, 1)",
		"round(d2, 0)",
		"round(d2, 10)",
		"round(d2)",
		"roundBankers(d2, 1)",
		"floor(d2, 1)",
		"ceil(d2, 1)",
		"trunc(d2, 1)",
	}
	for _, exprSQL := range cases {
		inferred, err := inferTestExprType(t, schema, exprSQL)
		if err != nil {
			t.Errorf("%s: chgen refused a call that the server runs: %v", exprSQL, err)
			continue
		}
		if inferred != "Decimal(18, 4)" {
			t.Errorf("%s: chgen answered %s, want Decimal(18, 4)", exprSQL, inferred)
		}
	}
}

// TestRoundFamilyRefusesANonNumericArgument is the refusal-side control
// for the round family: it shares abs and negate's accept-set
// (avgArgumentDomain) and refuses String, FixedString and Enum, exactly
// as abs and negate do. Measured on ClickHouse 25.8.29.51:
// round(s, 1), round(fs8, 1) and round(e8, 1) are all Code: 43,
// "A value of illegal type was provided ... Expected: A number to
// round".
func TestRoundFamilyRefusesANonNumericArgument(t *testing.T) {
	schema := temporalNumericStringTestSchema(t)
	refused := []string{
		"round(s, 1)",
		"round(fs8, 1)",
		"round(e8, 1)",
		"floor(s, 1)",
	}
	for _, exprSQL := range refused {
		inferred, err := inferTestExprType(t, schema, exprSQL)
		if err == nil {
			t.Errorf("%s: chgen answered %s, but the server refuses this call with Code: 43", exprSQL, inferred)
		}
	}
}

// TestRoundFamilyKeepsTheSimpleAggregateFunctionMarker is a regression
// control found by the axis sweep while developing the regression: giving
// round an argument domain (avgArgumentDomain) makes the argsFirstOnly
// path unwrap a SimpleAggregateFunction marker by default, UNLESS the
// rule is also named in valuePreservingDomainFunctions. round, floor and
// their siblings must be named there, because the server KEEPS the
// marker, unlike abs and sum, which unwrap it. Measured on ClickHouse
// 25.8.29.51 over a real SimpleAggregateFunction(sum, Int64) column in
// an AggregatingMergeTree table:
//
//	round(sagg, 1)   SimpleAggregateFunction(sum, Int64)   KEPT
//	floor(sagg)      SimpleAggregateFunction(sum, Int64)   KEPT
//	abs(sagg)        UInt64                                UNWRAPPED (contrast)
//	sum(sagg)        Int64                                 UNWRAPPED (contrast)
//
// Before valuePreservingDomainFunctions named round's family, chgen
// answered a bare Int64 for round(sagg, 1), a SHAPE_MISMATCH the axis
// sweep caught immediately (6 cells, one per name in this family) on
// the very first run after registering the family.
func TestRoundFamilyKeepsTheSimpleAggregateFunctionMarker(t *testing.T) {
	schema := temporalNumericStringTestSchema(t)
	cases := map[string]string{
		"round(sagg, 1)":        "SimpleAggregateFunction(sum, Int64)",
		"floor(sagg)":           "SimpleAggregateFunction(sum, Int64)",
		"ceil(sagg)":            "SimpleAggregateFunction(sum, Int64)",
		"trunc(sagg)":           "SimpleAggregateFunction(sum, Int64)",
		"roundBankers(sagg, 1)": "SimpleAggregateFunction(sum, Int64)",
	}
	for exprSQL, want := range cases {
		inferred, err := inferTestExprType(t, schema, exprSQL)
		if err != nil {
			t.Errorf("%s: chgen refused a call that the server runs: %v", exprSQL, err)
			continue
		}
		if inferred != want {
			t.Errorf("%s: chgen answered %s, want %s (the marker must survive)", exprSQL, inferred, want)
		}
	}
	// The contrast: abs unwraps the SAME argument. A fix that
	// accidentally added abs to valuePreservingDomainFunctions would
	// pass the cases above but fail here.
	if inferred, err := inferTestExprType(t, schema, "abs(sagg)"); err != nil {
		t.Errorf("abs(sagg): chgen refused a call the server runs: %v", err)
	} else if inferred != "UInt64" {
		t.Errorf("abs(sagg): chgen answered %s, want UInt64 (abs unwraps the marker)", inferred)
	}
}

// TestReverseKeepsFixedStringWidth covers the regression.
//
// Measured on ClickHouse 25.8.29.51 with DESCRIBE over the probe table:
//
//	reverse(fs8)   FixedString(8)   the width is KEPT
//	reverse(s)     String
func TestReverseKeepsFixedStringWidth(t *testing.T) {
	schema := temporalNumericStringTestSchema(t)
	cases := map[string]string{
		"reverse(fs8)": "FixedString(8)",
		"reverse(s)":   "String",
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

// TestSubstringDropsTheFixedStringWidthUnlikeReverse is the CONTROL for
// TestReverseKeepsFixedStringWidth. substring can return FEWER bytes
// than its source, so it must give String and never keep the source
// width, unlike reverse, which always returns the same number of bytes.
// Measured on ClickHouse 25.8.29.51: substring(fs8, 1, 2) is String, not
// FixedString(8) and not FixedString(2). A rule that shared reverse's
// width-preserving behaviour with substring would be a silently wrong,
// too-specific type.
func TestSubstringDropsTheFixedStringWidthUnlikeReverse(t *testing.T) {
	schema := temporalNumericStringTestSchema(t)
	if inferred, err := inferTestExprType(t, schema, "substring(fs8, 1, 2)"); err != nil {
		t.Errorf("substring(fs8, 1, 2): chgen refused a call the server runs: %v", err)
	} else if inferred != "String" {
		t.Errorf("substring(fs8, 1, 2): chgen answered %s, want String", inferred)
	}
}

// TestReverseRefusesAnEnumUnlikeSubstring is a second control: reverse
// and substring do NOT share the same argument domain, although both
// read "text". Measured on ClickHouse 25.8.29.51: reverse(e8) is
// Code: 43 ("Illegal type Enum8(...) of argument of function reverse"),
// while substring(e8, 2) RUNS. reverse must therefore keep the Enums
// OUT of its domain (reverseArgumentDomain: the two text types plus
// Array and Tuple), never use textArgumentDomain (which also accepts
// the Enums).
func TestReverseRefusesAnEnumUnlikeSubstring(t *testing.T) {
	schema := temporalNumericStringTestSchema(t)
	if inferred, err := inferTestExprType(t, schema, "reverse(e8)"); err == nil {
		t.Errorf("reverse(e8): chgen answered %s, but the server refuses this call with Code: 43", inferred)
	}
	if inferred, err := inferTestExprType(t, schema, "substring(e8, 2)"); err != nil {
		t.Errorf("substring(e8, 2): chgen refused a call the server runs: %v", err)
	} else if inferred != "String" {
		t.Errorf("substring(e8, 2): chgen answered %s, want String", inferred)
	}
}

// TestToFixedStringNarrowingMessageNamesTheMeasuredMechanism covers
// the regression. It pins the CURRENT wording, so a future edit that drifts
// the message back toward the old, wrong "ClickHouse refuses ... below
// the source width" analysis-time claim fails loudly.
//
// toTypeName is an ANALYSIS-ONLY witness for this cell and must NOT be
// used to re-litigate it: SELECT toTypeName(toFixedString(fs8, 2))
// answers FixedString(2) and analysis succeeds, which looks like it
// "disproves" the refusal below, but the query FAILS at execution with
// Code: 131 (String too long) on every real row. chgen's own oracle
// elsewhere in this codebase uses toTypeName as its primary witness;
// this cell is the one place a toTypeName-only check would be lied to.
func TestToFixedStringNarrowingMessageNamesTheMeasuredMechanism(t *testing.T) {
	schema := temporalNumericStringTestSchema(t)
	_, err := inferTestExprType(t, schema, "toFixedString(fs8, 2)")
	if err == nil {
		t.Fatalf("toFixedString(fs8, 2): chgen accepted a narrowing that fails on the server with Code: 131 at execution")
	}
	message := err.Error()
	for _, want := range []string{
		"FixedString(8)",
		"FixedString(2)",
		"NUL-pad",
		"Code: 131",
		"RUNS",
	} {
		if !strings.Contains(message, want) {
			t.Errorf("toFixedString(fs8, 2) message %q does not mention %q; "+
				"the message must state the measured mechanism (NUL-padding makes every "+
				"FixedString(8) value exactly 8 bytes, and the server fails this at EXECUTION, "+
				"not at analysis)", message, want)
		}
	}
	if strings.Contains(message, "ClickHouse refuses a target width below the source width") {
		t.Errorf("toFixedString(fs8, 2) message %q still carries the old, WRONG analysis-time claim; "+
			"the server's own analysis (toTypeName) succeeds for this call, only execution fails", message)
	}
}

// TestToFixedStringAsymmetryBothHalves pins BOTH halves of the measured
// asymmetry that the regression records: a FixedString source is a TYPE
// property (every value is refused, because every value of
// FixedString(8) is exactly 8 bytes), while a String source is a VALUE
// property (some values fit, some do not, and chgen cannot decide this
// statically, so it must not refuse the type).
//
// Measured on ClickHouse 25.8.29.51 with a VALUE select, not toTypeName
// alone (see the note on TestToFixedStringNarrowingMessageNamesTheMeasuredMechanism):
//
//	toFixedString(fs8, 2)   FixedString source   Code: 131 for EVERY value
//	toFixedString(s, 2)     String source        RUNS ('ab' fits; a longer
//	                                              value would still fail,
//	                                              but that is undecidable
//	                                              from the type alone)
func TestToFixedStringAsymmetryBothHalves(t *testing.T) {
	schema := temporalNumericStringTestSchema(t)
	if inferred, err := inferTestExprType(t, schema, "toFixedString(fs8, 2)"); err == nil {
		t.Errorf("toFixedString(fs8, 2): chgen answered %s, but a FixedString(8) source can never fit "+
			"FixedString(2): every value is padded to exactly 8 bytes", inferred)
	}
	if inferred, err := inferTestExprType(t, schema, "toFixedString(s, 2)"); err != nil {
		t.Errorf("toFixedString(s, 2): chgen refused a String source; the server RUNS this call "+
			"(a String source's fit is a VALUE property, undecidable statically): %v", err)
	} else if inferred != "FixedString(2)" {
		t.Errorf("toFixedString(s, 2): chgen answered %s, want FixedString(2)", inferred)
	}
}

// TestToFixedStringWideningAndEqualWidthStillRun is a widening/equal
// control for the same narrowing check: toFixedString must still accept
// an equal or wider target for a FixedString source, so a rule that
// over-refused (for example by refusing every FixedString source
// outright) would fail here.
func TestToFixedStringWideningAndEqualWidthStillRun(t *testing.T) {
	schema := temporalNumericStringTestSchema(t)
	cases := map[string]string{
		"toFixedString(fs8, 8)":  "FixedString(8)",
		"toFixedString(fs8, 16)": "FixedString(16)",
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
