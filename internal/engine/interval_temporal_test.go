package engine

import (
	"fmt"
	"testing"

	clickhouse "github.com/AfterShip/clickhouse-sql-parser/parser"
)

// These tests pin the temporal type rules added for INTERVAL arithmetic,
// toStartOfInterval, toTimeZone and the window frame functions.
//
// EVERY expected type in this file was MEASURED on ClickHouse 25.8.29.51
// (image clickhouse/clickhouse-server:25.8, disposable container) with
// SELECT toTypeName(<expression>) FROM <table>, against REAL TABLE COLUMNS.
// Literals are not usable as evidence for these rules, because the server
// folds constants and then reports the type of the folded value instead of
// the type that the operator produces.
//
// Do not replace a number here from memory or from the ClickHouse
// documentation. Measure it again if it looks wrong.

// intervalTestSchema holds one column of every temporal type that the rules
// distinguish, with and without a timezone, and in the Nullable and
// LowCardinality forms.
//
// A note on LowCardinality over a temporal type: the server refuses to CREATE
// such a column unless allow_suspicious_low_cardinality_types is set. The
// measurements for the LowCardinality rows were taken with that setting on. A
// LowCardinality temporal can still reach a query through a cast, so the rule
// is still needed.
func intervalTestSchema(t *testing.T) *Schema {
	t.Helper()
	schema, err := schemaFromDDLErr(t, `CREATE TABLE t (
    d     Date,
    d32   Date32,
    dt    DateTime,
    dtz   DateTime('UTC'),
    dt64  DateTime64(3),
    dtz64 DateTime64(6, 'UTC'),
    dt64n DateTime64(9),
    nd    Nullable(Date),
    nd32  Nullable(Date32),
    ndt   Nullable(DateTime),
    ndtz  Nullable(DateTime('UTC')),
    ndt64 Nullable(DateTime64(3)),
    lcd   LowCardinality(Date),
    lcdt  LowCardinality(DateTime),
    lcdtz LowCardinality(DateTime('UTC')),
    lcnd  LowCardinality(Nullable(Date)),
    lcndt LowCardinality(Nullable(DateTime)),
    i32   Int32,
    ni32  Nullable(Int32),
    s     String,
    ns    Nullable(String),
    lc    LowCardinality(String)
) ENGINE = MergeTree ORDER BY tuple();`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	return schema
}

func runTemporalTypeCases(t *testing.T, cases []struct{ expr, want string }) {
	t.Helper()
	schema := intervalTestSchema(t)
	for _, testCase := range cases {
		got := inferCHTypeString(t, schema, testCase.expr)
		if got != testCase.want {
			t.Errorf("type of %q = %q, measured on ClickHouse 25.8.29.51 = %q", testCase.expr, got, testCase.want)
		}
	}
}

// NOTE on MICROSECOND and NANOSECOND. The ClickHouse SERVER supports both
// units, and they were measured on 25.8.29.51: dt + INTERVAL 1 MICROSECOND
// is DateTime64(6) and dt + INTERVAL 1 NANOSECOND is DateTime64(9). They do
// not appear in the cases below because the front end that chgen uses
// refuses them: its intervalUnits set holds only MILLISECOND, SECOND,
// MINUTE, HOUR, DAY, WEEK, MONTH, QUARTER and YEAR, so such an expression
// never reaches the type resolver. The rule in intervalUnitPrecision still
// covers them, so the behaviour is correct if the front end gains the units
// later. TestSubSecondIntervalUnitsStayUnreachable below pins the gap, and
// docs/front-end-gaps.md records why such a refusal is not a chgen defect.

// TestSubSecondIntervalUnitsStayUnreachable pins the front end gap that
// keeps INTERVAL MICROSECOND and NANOSECOND away from the type resolver.
// The test asserts the REFUSAL, not a type, so it fails as soon as the
// front end gains the units. That failure is the signal to move the two
// units into the measured matrix above, where the server types
// DateTime64(6) and DateTime64(9) wait for them.
func TestSubSecondIntervalUnitsStayUnreachable(t *testing.T) {
	for _, unit := range []string{"MICROSECOND", "NANOSECOND"} {
		query := fmt.Sprintf("SELECT dt + INTERVAL 1 %s FROM t", unit)
		if _, err := clickhouse.NewParser(query).ParseStmts(); err == nil {
			t.Errorf("the front end now parses %q; ClickHouse 25.8.29.51 gives "+
				"DateTime64(6) for MICROSECOND and DateTime64(9) for NANOSECOND, "+
				"thus move the unit into TestIntervalArithmeticResultType and "+
				"update docs/front-end-gaps.md", query)
		}
	}
}

// TestIntervalArithmeticResultType pins the full measured matrix of
// "<temporal> + INTERVAL 1 <unit>". The rule that the matrix shows is:
// a unit of one day or more keeps the operand type, and a unit below one day
// promotes a date-only operand and raises the DateTime64 precision to at
// least the precision of the unit.
func TestIntervalArithmeticResultType(t *testing.T) {
	runTemporalTypeCases(t, []struct{ expr, want string }{
		// Date. A sub-day unit promotes to DateTime; one day or more
		// keeps Date.
		{"d + INTERVAL 1 SECOND", "DateTime"},
		{"d + INTERVAL 1 MINUTE", "DateTime"},
		{"d + INTERVAL 1 HOUR", "DateTime"},
		{"d + INTERVAL 1 DAY", "Date"},
		{"d + INTERVAL 1 WEEK", "Date"},
		{"d + INTERVAL 1 MONTH", "Date"},
		{"d + INTERVAL 1 QUARTER", "Date"},
		{"d + INTERVAL 1 YEAR", "Date"},

		// Date32 promotes to DateTime64(3), NOT to DateTime. Date32
		// spans years that DateTime cannot hold.
		{"d32 + INTERVAL 1 SECOND", "DateTime64(3)"},
		{"d32 + INTERVAL 1 HOUR", "DateTime64(3)"},
		{"d32 + INTERVAL 1 DAY", "Date32"},
		{"d32 + INTERVAL 1 YEAR", "Date32"},

		// DateTime keeps its type for every whole-second unit.
		{"dt + INTERVAL 1 SECOND", "DateTime"},
		{"dt + INTERVAL 1 DAY", "DateTime"},
		{"dt + INTERVAL 1 YEAR", "DateTime"},

		// A sub-second unit raises DateTime to DateTime64 of the
		// precision of that unit.
		{"dt + INTERVAL 1 MILLISECOND", "DateTime64(3)"},

		// DateTime64 takes max(operand precision, unit precision).
		{"dt64 + INTERVAL 1 SECOND", "DateTime64(3)"},
		{"dt64 + INTERVAL 1 MILLISECOND", "DateTime64(3)"},
		{"dt64n + INTERVAL 1 MILLISECOND", "DateTime64(9)"},

		// The subtraction operator gives the same type as addition.
		{"d - INTERVAL 1 SECOND", "DateTime"},
		{"d - INTERVAL 1 DAY", "Date"},
		{"d32 - INTERVAL 1 HOUR", "DateTime64(3)"},

		// An INTERVAL on the LEFT is legal for "+" only, and gives the
		// same type. "INTERVAL 1 DAY - dt" is rejected by the server
		// with ILLEGAL_TYPE_OF_ARGUMENT, so chgen never types it.
		{"INTERVAL 1 DAY + dt", "DateTime"},
		{"INTERVAL 1 HOUR + d", "DateTime"},
	})
}

// TestIntervalArithmeticKeepsTimezone pins the timezone behaviour. A
// DateTime or DateTime64 operand keeps its declared zone. A PROMOTED Date or
// Date32 operand gets a result with NO zone name, even under a session
// timezone: measured with session_timezone=Asia/Tokyo, d + INTERVAL 1 HOUR is
// still a bare DateTime. chgen must therefore never invent a zone.
func TestIntervalArithmeticKeepsTimezone(t *testing.T) {
	runTemporalTypeCases(t, []struct{ expr, want string }{
		{"dtz + INTERVAL 1 DAY", "DateTime('UTC')"},
		{"dtz + INTERVAL 1 SECOND", "DateTime('UTC')"},
		{"dtz + INTERVAL 1 MILLISECOND", "DateTime64(3, 'UTC')"},
		{"dtz64 + INTERVAL 1 MINUTE", "DateTime64(6, 'UTC')"},
		{"dtz64 + INTERVAL 1 MILLISECOND", "DateTime64(6, 'UTC')"},
		// The promotion of a date-only operand carries no zone.
		{"d + INTERVAL 1 HOUR", "DateTime"},
		{"d32 + INTERVAL 1 HOUR", "DateTime64(3)"},
	})
}

// TestIntervalArithmeticWrappers pins the wrapper propagation. Both Nullable
// and LowCardinality pass through unchanged, and the promotion happens inside
// them.
func TestIntervalArithmeticWrappers(t *testing.T) {
	runTemporalTypeCases(t, []struct{ expr, want string }{
		{"nd + INTERVAL 1 SECOND", "Nullable(DateTime)"},
		{"nd + INTERVAL 1 DAY", "Nullable(Date)"},
		{"nd32 + INTERVAL 1 SECOND", "Nullable(DateTime64(3))"},
		{"nd32 + INTERVAL 1 DAY", "Nullable(Date32)"},
		{"ndt + INTERVAL 1 DAY", "Nullable(DateTime)"},
		{"ndt64 + INTERVAL 1 SECOND", "Nullable(DateTime64(3))"},
		{"lcd + INTERVAL 1 SECOND", "LowCardinality(DateTime)"},
		{"lcd + INTERVAL 1 DAY", "LowCardinality(Date)"},
		{"lcdt + INTERVAL 1 DAY", "LowCardinality(DateTime)"},
		{"lcnd + INTERVAL 1 SECOND", "LowCardinality(Nullable(DateTime))"},
		{"lcnd + INTERVAL 1 DAY", "LowCardinality(Nullable(Date))"},
		{"lcndt + INTERVAL 1 SECOND", "LowCardinality(Nullable(DateTime))"},
	})
}

// TestDate32DifferenceStaysRefused pins the one cell where Date32 parts from
// Date. The two types share the offset rules: d32 + 1 and d32 - 1 are both
// Date32, exactly as for Date. They do NOT share the same-type difference.
//
// Measured on ClickHouse 25.8.29.51 against real columns, in the analysis
// form and over a real row:
//
//	d   - d      Int32
//	dt  - dt     Int32
//	d32 - d32    Code 43, "Illegal types Date32 and Date32 of arguments
//	             of function minus"
//	d32 - d      Code 43
//	d32 - dt     Code 43
//
// A literal pair behaves the same way, thus the refusal is a property of the
// operator and not of the column path: toDate32('2024-01-02') -
// toDate32('2024-01-01') is code 43 too.
//
// chgen used to answer Int32 for d32 - d32, because the Date rule was
// widened to every date-like type. That answer was silently wrong: the
// generated Go compiled, and the server then refused the query. An explicit
// refusal is the correct result, because the server has no type to report.
func TestDate32DifferenceStaysRefused(t *testing.T) {
	schema := intervalTestSchema(t)
	refused := []string{
		"d32 - d32",
		// The wrapper forms follow the inner rule.
		"nd32 - nd32",
		// Already refused before the fix; they stay refused.
		"d32 - d",
		"d - d32",
		"d32 - dt",
	}
	for _, expr := range refused {
		if got, err := inferCHTypeErr(t, schema, expr); err == nil {
			t.Errorf("type of %q = %q, but ClickHouse 25.8.29.51 answers "+
				"code 43 ILLEGAL_TYPE_OF_ARGUMENT, thus it must stay an "+
				"explicit refusal", expr, got)
		}
	}
	// The neighbouring cells must NOT move. Date32 keeps its offset rules,
	// and the Date and DateTime differences keep their Int32 result.
	runTemporalTypeCases(t, []struct{ expr, want string }{
		{"d32 + 1", "Date32"},
		{"d32 - 1", "Date32"},
		{"d - d", "Int32"},
		{"dt - dt", "Int32"},
	})
}

// TestDateTime64DifferenceKeepsPrecision pins the second cell where the
// date-like family parts from Date. A DateTime64 difference is not a count
// of seconds: it carries the sub-second scale of the operands.
//
// Measured on ClickHouse 25.8.29.51 against real columns, in the analysis
// form and over a real row. Every precision from 0 to 9 gives
// Decimal(18, precision), and a mixed pair takes the LARGER scale in both
// operand orders:
//
//	dt64  - dt64     Decimal(18, 3)
//	dtz64 - dtz64    Decimal(18, 6)
//	dt64n - dt64n    Decimal(18, 9)
//	dt64  - dtz64    Decimal(18, 6)
//	dtz64 - dt64     Decimal(18, 6)
//	dt64  - dt       Decimal(18, 3)
//	dt    - dt64     Decimal(18, 3)
//	dt64  - d        Code 43, "Illegal types DateTime64(3) and Date"
//	d     - dt64     Code 43
//	dt64  - d32      Code 43
//
// A DateTime operand behaves as a DateTime64 of precision 0, thus the
// DateTime64 scale wins. The timezone does NOT reach the result: dtz64 is
// DateTime64(6, 'UTC') and its difference is a plain Decimal(18, 6).
//
// chgen used to answer Int32 for all of these pairs, because the Date rule
// was widened to every date-like type. That is a silently wrong type: the
// generated Go compiled, and it then held a truncated value.
func TestDateTime64DifferenceKeepsPrecision(t *testing.T) {
	runTemporalTypeCases(t, []struct{ expr, want string }{
		// The same-type difference carries the column precision.
		{"dt64 - dt64", "Decimal(18, 3)"},
		{"dtz64 - dtz64", "Decimal(18, 6)"},
		{"dt64n - dt64n", "Decimal(18, 9)"},
		// A mixed pair takes the larger scale, in both orders. The
		// timezone of dtz64 does not reach the result.
		{"dt64 - dtz64", "Decimal(18, 6)"},
		{"dtz64 - dt64", "Decimal(18, 6)"},
		{"dt64 - dt64n", "Decimal(18, 9)"},
		// A DateTime operand behaves as precision 0, thus the
		// DateTime64 scale wins in both orders.
		{"dt64 - dt", "Decimal(18, 3)"},
		{"dt - dt64", "Decimal(18, 3)"},
		{"dtz64 - dt", "Decimal(18, 6)"},
		// The Nullable wrapper propagates over the new rule.
		{"ndt64 - ndt64", "Nullable(Decimal(18, 3))"},
		{"ndt64 - dt64", "Nullable(Decimal(18, 3))"},
	})
	// A date-only operand has no rule. The server answers code 43, thus an
	// explicit refusal is the only correct result.
	schema := intervalTestSchema(t)
	refused := []string{
		"dt64 - d",
		"d - dt64",
		"dt64 - d32",
		"d32 - dt64",
	}
	for _, expr := range refused {
		if got, err := inferCHTypeErr(t, schema, expr); err == nil {
			t.Errorf("type of %q = %q, but ClickHouse 25.8.29.51 answers "+
				"code 43 ILLEGAL_TYPE_OF_ARGUMENT, thus it must stay an "+
				"explicit refusal", expr, got)
		}
	}
	// The neighbouring cells must NOT move. The Date and DateTime
	// differences keep Int32, and the DateTime64 offset rules keep the
	// DateTime64 type.
	runTemporalTypeCases(t, []struct{ expr, want string }{
		{"d - d", "Int32"},
		{"dt - dt", "Int32"},
		{"dt64 + 1", "DateTime64(3)"},
		{"dt64 - 1", "DateTime64(3)"},
		{"1 + dt64", "DateTime64(3)"},
	})
	// Date32 keeps its refusal: it has no difference at all.
	if got, err := inferCHTypeErr(t, schema, "d32 - d32"); err == nil {
		t.Errorf("type of %q = %q, but Date32 has no difference at all",
			"d32 - d32", got)
	}
}

// TestIntervalArithmeticRefusals pins the combinations that MUST stay
// refusals. A refusal is correct here, because the server itself has no
// result type for these expressions. A guess would be silently wrong.
func TestIntervalArithmeticRefusals(t *testing.T) {
	schema := intervalTestSchema(t)
	// Measured: the server rejects a sub-second unit on a date-only
	// operand with ILLEGAL_TYPE_OF_ARGUMENT ("addMilliseconds cannot be
	// used with Date"). There is no result type to report.
	refused := []string{
		"d + INTERVAL 1 MILLISECOND",
		"d32 + INTERVAL 1 MILLISECOND",
		// A non-temporal left operand has no INTERVAL rule.
		"i32 + INTERVAL 1 DAY",
		"s + INTERVAL 1 DAY",
	}
	for _, expr := range refused {
		if _, err := inferCHTypeErr(t, schema, expr); err == nil {
			t.Errorf("type of %q was inferred, but it must stay an explicit refusal", expr)
		}
	}
}

// TestToStartOfIntervalResultType pins the measured matrix of
// toStartOfInterval. The result depends ONLY on the unit and on the timezone
// of the first argument. The WIDTH of the first argument does not reach the
// result at all: toStartOfInterval(dt64, INTERVAL 1 HOUR) is DateTime, not
// DateTime64(3), and toStartOfInterval(d, INTERVAL 1 DAY) is DateTime, not
// Date.
//
// This differs from INTERVAL arithmetic: there the DAY unit keeps the operand
// type, here the DAY unit still gives DateTime. The date-only row starts one
// unit later, at WEEK. The two boundaries were measured separately and must
// not be merged.
func TestToStartOfIntervalResultType(t *testing.T) {
	runTemporalTypeCases(t, []struct{ expr, want string }{
		// Every argument width gives the same row for a given unit.
		{"toStartOfInterval(d, INTERVAL 1 DAY)", "DateTime"},
		{"toStartOfInterval(d32, INTERVAL 1 DAY)", "DateTime"},
		{"toStartOfInterval(dt, INTERVAL 1 DAY)", "DateTime"},
		{"toStartOfInterval(dt64, INTERVAL 1 DAY)", "DateTime"},
		{"toStartOfInterval(dt64, INTERVAL 1 HOUR)", "DateTime"},
		{"toStartOfInterval(dt64, INTERVAL 1 SECOND)", "DateTime"},

		// A sub-second unit gives DateTime64 of that precision, again
		// whatever the argument WIDTH is -- but only for an argument
		// that holds a time of day.
		//
		// The case toStartOfInterval(d, INTERVAL 1 MILLISECOND) stood
		// here with the expected type DateTime64(3). That expectation
		// was WRONG. Re-measured on ClickHouse 25.8.29.51, with the
		// analysis form and over a real row: the server refuses the
		// call outright with
		//
		//	Code: 43 Illegal interval kind for argument data type Date
		//
		// A Date holds no time of day, so a millisecond boundary of a
		// Date has no meaning. The old case asserted a type for an
		// impossible call, thus it pinned exactly the silently wrong
		// answer that a domain rule must remove. The refusal now lives
		// in TestToStartOfIntervalRefusesAnIllegalUnit.
		{"toStartOfInterval(dt64, INTERVAL 1 MILLISECOND)", "DateTime64(3)"},

		// One week or more gives a bare Date, and DROPS the timezone.
		{"toStartOfInterval(d, INTERVAL 1 WEEK)", "Date"},
		{"toStartOfInterval(dt, INTERVAL 1 MONTH)", "Date"},
		{"toStartOfInterval(dt64, INTERVAL 1 MONTH)", "Date"},
		{"toStartOfInterval(d32, INTERVAL 1 QUARTER)", "Date"},
		{"toStartOfInterval(dtz, INTERVAL 1 YEAR)", "Date"},
		{"toStartOfInterval(dtz64, INTERVAL 1 WEEK)", "Date"},

		// A timezone on the argument survives every sub-week unit.
		{"toStartOfInterval(dtz, INTERVAL 1 HOUR)", "DateTime('UTC')"},
		{"toStartOfInterval(dtz64, INTERVAL 1 HOUR)", "DateTime('UTC')"},
		// The case toStartOfInterval(dtz, INTERVAL 1 MILLISECOND) stood
		// here with the expected type DateTime64(3, 'UTC'). That
		// expectation was WRONG, for the same reason as the Date case
		// above: a DateTime holds whole seconds, so a millisecond
		// boundary of a DateTime has no meaning and the server refuses
		// the call with
		//
		//	Code: 43 Illegal interval kind for argument data type DateTime
		//
		// Both wrong cases came from SELECT toTypeName(...), which
		// reports DateTime64(3) here. toTypeName does NOT execute the
		// inner function, thus it answers for a call that cannot run.
		// Re-measured by executing the expression over a real row: the
		// call fails. Measure a domain with an executed expression, not
		// with toTypeName.
		{"toStartOfInterval(dtz64, INTERVAL 1 MILLISECOND)", "DateTime64(3, 'UTC')"},

		// The wrappers pass through.
		{"toStartOfInterval(ndt, INTERVAL 1 HOUR)", "Nullable(DateTime)"},
	})
}

// TestToTimeZoneResultType pins toTimeZone. It keeps the width and the
// precision of its argument and replaces the timezone name.
func TestToTimeZoneResultType(t *testing.T) {
	runTemporalTypeCases(t, []struct{ expr, want string }{
		{"toTimeZone(dt, 'Asia/Tokyo')", "DateTime('Asia/Tokyo')"},
		{"toTimeZone(dtz, 'Asia/Tokyo')", "DateTime('Asia/Tokyo')"},
		{"toTimeZone(dt64, 'Asia/Tokyo')", "DateTime64(3, 'Asia/Tokyo')"},
		{"toTimeZone(dtz64, 'Asia/Tokyo')", "DateTime64(6, 'Asia/Tokyo')"},
		{"toTimeZone(dt64n, 'Asia/Tokyo')", "DateTime64(9, 'Asia/Tokyo')"},
		{"toTimeZone(ndt, 'Asia/Tokyo')", "Nullable(DateTime('Asia/Tokyo'))"},
		{"toTimeZone(ndt64, 'Asia/Tokyo')", "Nullable(DateTime64(3, 'Asia/Tokyo'))"},
		{"toTimeZone(lcdt, 'Asia/Tokyo')", "LowCardinality(DateTime('Asia/Tokyo'))"},
		{"toTimeZone(lcndt, 'Asia/Tokyo')", "LowCardinality(Nullable(DateTime('Asia/Tokyo')))"},
		{"toTimeZone(dt, 'UTC')", "DateTime('UTC')"},
	})
}

// TestToTimeZoneRefusals pins the two cases that MUST stay refusals, because
// the server itself has no result there.
func TestToTimeZoneRefusals(t *testing.T) {
	schema := intervalTestSchema(t)
	refused := []string{
		// Measured: ILLEGAL_TYPE_OF_ARGUMENT, "Illegal type Date of
		// argument of function toTimezone. Should be DateTime or
		// DateTime64".
		"toTimeZone(d, 'UTC')",
		"toTimeZone(d32, 'UTC')",
		// Measured: ILLEGAL_COLUMN, "Illegal column s of time zone
		// argument of function, must be a constant string". The result
		// type carries the zone NAME, so a non-constant zone has no
		// static type at all.
		"toTimeZone(dt, s)",
	}
	for _, expr := range refused {
		if _, err := inferCHTypeErr(t, schema, expr); err == nil {
			t.Errorf("type of %q was inferred, but it must stay an explicit refusal", expr)
		}
	}
}

// TestTimezoneCarryingConstructors pins the DateTime constructors whose
// result carries a timezone NAME. Before these rules chgen returned a bare
// DateTime or DateTime64 and dropped the zone, which is silently wrong
// whenever the argument or an explicit argument carries one.
func TestTimezoneCarryingConstructors(t *testing.T) {
	runTemporalTypeCases(t, []struct{ expr, want string }{
		{"now()", "DateTime"},
		{"now('UTC')", "DateTime('UTC')"},
		{"now64()", "DateTime64(3)"},
		{"now64(6)", "DateTime64(6)"},
		{"now64(3, 'UTC')", "DateTime64(3, 'UTC')"},

		{"toDateTime(dt)", "DateTime"},
		{"toDateTime(s)", "DateTime"},
		// The zone is carried from the ARGUMENT type.
		{"toDateTime(dtz)", "DateTime('UTC')"},
		// An explicit zone argument replaces it.
		{"toDateTime(dt, 'Europe/Berlin')", "DateTime('Europe/Berlin')"},
		{"toDateTime(dtz, 'Europe/Berlin')", "DateTime('Europe/Berlin')"},

		{"toDateTime64(dt, 6)", "DateTime64(6)"},
		{"toDateTime64(dt64, 1)", "DateTime64(1)"},
		{"toDateTime64(dtz64, 3)", "DateTime64(3, 'UTC')"},
		{"toDateTime64(dt, 3, 'Europe/Berlin')", "DateTime64(3, 'Europe/Berlin')"},

		// toStartOfDay always gives a whole-second DateTime. A
		// DateTime64 argument loses its precision but keeps its zone.
		{"toStartOfDay(d)", "DateTime"},
		{"toStartOfDay(dt)", "DateTime"},
		{"toStartOfDay(dt64)", "DateTime"},
		{"toStartOfDay(dtz)", "DateTime('UTC')"},
		{"toStartOfDay(d, 'UTC')", "DateTime('UTC')"},
		{"toStartOfDay(dtz64)", "DateTime('UTC')"},
		{"toStartOfDay(ndtz)", "Nullable(DateTime('UTC'))"},
		{"toStartOfDay(lcdtz)", "LowCardinality(DateTime('UTC'))"},
		{"toStartOfDay(lcndt)", "LowCardinality(Nullable(DateTime))"},
	})
}

// TestWindowFunctionResultType pins the window-only functions.
//
// The rank family counts rows, so the result is UInt64 whatever the partition
// and order expressions are. The value-frame family returns a value of the
// first argument type, and REMOVES LowCardinality exactly like an aggregate.
func TestWindowFunctionResultType(t *testing.T) {
	runTemporalTypeCases(t, []struct{ expr, want string }{
		{"row_number() OVER (ORDER BY i32)", "UInt64"},
		{"rank() OVER (ORDER BY i32)", "UInt64"},
		{"dense_rank() OVER (ORDER BY i32)", "UInt64"},

		{"first_value(i32) OVER (ORDER BY i32)", "Int32"},
		{"first_value(ns) OVER (ORDER BY i32)", "Nullable(String)"},
		{"last_value(i32) OVER (ORDER BY i32)", "Int32"},
		{"last_value(dt64) OVER (ORDER BY i32)", "DateTime64(3)"},

		{"lagInFrame(i32, 1) OVER (ORDER BY i32)", "Int32"},
		{"lagInFrame(ni32, 1) OVER (ORDER BY i32)", "Nullable(Int32)"},
		{"lagInFrame(s, 1) OVER (ORDER BY i32)", "String"},
		{"leadInFrame(i32, 1) OVER (ORDER BY i32)", "Int32"},
		{"leadInFrame(ni32, 1) OVER (ORDER BY i32)", "Nullable(Int32)"},
		{"leadInFrame(s, 1) OVER (ORDER BY i32)", "String"},
		{"lagInFrame(d, 1) OVER (ORDER BY i32)", "Date"},

		// LowCardinality is REMOVED, like an aggregate. Measured:
		// first_value(lcdt) is DateTime and lagInFrame(lc, 1) is
		// String, with no LowCardinality wrapper.
		{"first_value(lcdt) OVER (ORDER BY i32)", "DateTime"},
		{"lagInFrame(lc, 1) OVER (ORDER BY i32)", "String"},

		// The optional third argument of lagInFrame is a default
		// value. It cannot widen the result: ClickHouse rejects any
		// default whose supertype with the first argument differs from
		// the first argument type (measured: lagInFrame(i32, 1, NULL)
		// and lagInFrame(i32, 1, 3000000000) are both BAD_ARGUMENTS).
		// The first argument is therefore the whole rule.
		{"lagInFrame(s, 1, 'x') OVER (ORDER BY i32)", "String"},
	})
}

// inferCHTypeErr is the refusal-side companion of inferCHTypeString. It
// returns the inference error instead of failing the test, so a test can
// assert that a construct STAYS an explicit refusal.
func inferCHTypeErr(t *testing.T, schema *Schema, exprSQL string) (string, error) {
	t.Helper()
	statements, err := clickhouseParseSelect(exprSQL)
	if err != nil {
		// A parse error is also a refusal: chgen produces no type.
		return "", err
	}
	scope, _, err := resolveScope(statements, schema)
	if err != nil {
		return "", err
	}
	inferred, err := inferExprType(statements.SelectItems[0].Expr, scope)
	if err != nil {
		return "", err
	}
	return inferred.String(), nil
}

// clickhouseParseSelect parses "SELECT <expr> FROM t" and returns the SELECT.
func clickhouseParseSelect(exprSQL string) (*clickhouse.SelectQuery, error) {
	statements, err := clickhouse.NewParser("SELECT " + exprSQL + " FROM t").ParseStmts()
	if err != nil {
		return nil, err
	}
	selectQuery, ok := statements[0].(*clickhouse.SelectQuery)
	if !ok {
		return nil, fmt.Errorf("not a SELECT")
	}
	return selectQuery, nil
}
