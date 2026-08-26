package engine

import "testing"

// aggregateStateProbeDDL creates a probe table whose agg column is a
// real AggregateFunction state, so the schema parser stores it with the
// BARE name "AggregateFunction" (see holdsAggregateFunctionState). A
// test that only builds a CHType by hand never exercises this parser
// path, and this project has already paid once for that gap.
const aggregateStateProbeDDL = `CREATE TABLE probe (
	i32 Int32, i64 Int64, u8 UInt8, f64 Float64, s String,
	agg AggregateFunction(uniq, UInt64),
	aggif AggregateFunction(sumIf, Int32, UInt8),
	sagg SimpleAggregateFunction(sum, Int64)
) ENGINE = AggregatingMergeTree ORDER BY tuple()`

// TestAggregateStateOverParsedSchemaEndToEnd runs the fix through the
// full pipeline (DDL parse, resolveScope, inferExprType), not only
// through checkArgumentDomain in isolation, for both a refused cast and
// the ACCEPTED family that the regression must not touch.
//
// Measured on ClickHouse 25.8.29.51, confirmed by a real SELECT:
//
//	toUInt8(aggif)             Code: 43   (refused, this fix)
//	max(agg)                   Code: 43   (refused, this fix)
//	argMax(i32, agg)           Code: 43   (refused, this fix: index 1)
//	argMax(agg, i32)           RUNS       (accepted: index 0, unchanged)
//	any(agg)                   RUNS       (accepted, no domain, unchanged)
//	count(agg)                 RUNS       (accepted, unchanged)
//	isNull(agg)                RUNS       (accepted, unchanged)
//	toString(agg)              RUNS       (accepted, unchanged)
//	groupArray(agg)            RUNS       (accepted, unchanged)
//	tuple(agg)                 RUNS       (accepted, unchanged)
//	if(u8 = 1, agg, agg)       RUNS       (accepted, unchanged)
//	uniqMerge(agg)             RUNS       (accepted, -Merge combinator, unchanged)
//	sumIfMerge(aggif)          RUNS       (accepted, -Merge combinator, unchanged)
//	uniqState(u8)              RUNS       (accepted, -State combinator, unchanged)
//	max(sagg)                  SimpleAggregateFunction(sum, Int64)  (unchanged)
func TestAggregateStateOverParsedSchemaEndToEnd(t *testing.T) {
	schema, err := schemaFromDDLErr(t, aggregateStateProbeDDL)
	if err != nil {
		t.Fatalf("parse probe schema: %v", err)
	}

	refused := []string{
		"toUInt8(aggif)",
		"toInt16(agg)",
		"toFloat32(agg)",
		"toDate(agg)",
		"toDateTime64(agg, 3)",
		"max(agg)",
		"min(aggif)",
		"maxIf(agg, u8 = 1)",
		"argMax(i32, agg)",
		"argMinIf(isNull(i64), aggif, f64 < 2.5)",
	}
	for _, expression := range refused {
		if _, err := inferTestExprType(t, schema, expression); err == nil {
			t.Errorf("%s must be refused: the server answers Code 43, thus a type here "+
				"is a silently wrong answer", expression)
		}
	}

	accepted := []struct {
		expression string
		want       string
	}{
		{"any(agg)", "AggregateFunction(uniq, UInt64)"},
		{"count(agg)", "UInt64"},
		{"isNull(agg)", "UInt8"},
		{"toString(agg)", "String"},
		{"groupArray(agg)", "Array(AggregateFunction(uniq, UInt64))"},
		{"tuple(agg)", "Tuple(AggregateFunction(uniq, UInt64))"},
		{"if(u8 = 1, agg, agg)", "AggregateFunction(uniq, UInt64)"},
		{"uniqMerge(agg)", "UInt64"},
		{"sumIfMerge(aggif)", "Int64"},
		{"uniqState(u8)", "AggregateFunction(uniq, UInt8)"},
		{"argMax(agg, i32)", "AggregateFunction(uniq, UInt64)"},
		{"max(sagg)", "SimpleAggregateFunction(sum, Int64)"},
	}
	for _, testCase := range accepted {
		got, err := inferTestExprType(t, schema, testCase.expression)
		if err != nil {
			t.Errorf("%s must be accepted: the server runs it; got error %v", testCase.expression, err)
			continue
		}
		if got != testCase.want {
			t.Errorf("%s: got %s, want %s", testCase.expression, got, testCase.want)
		}
	}
}

// TestAggregateFunctionStateRefusedAsCastArgument holds the measured
// domain of the numeric and date/time conversions, the regression.
//
// Measured on ClickHouse 25.8.29.51 over real columns of an
// AggregatingMergeTree table (agg is AggregateFunction(uniq, UInt64),
// aggif is AggregateFunction(sumIf, Int32, UInt8)), and confirmed by a
// real SELECT, not only DESCRIBE:
//
//	toUInt8(aggif)        Code: 43
//	toInt16(agg)          Code: 43
//	toInt32(aggif)        Code: 43
//	toFloat32(agg)        Code: 43
//	toFloat64(agg)        Code: 43
//	toDateTime64(agg, 3)  Code: 43
//	toDate(agg)           Code: 43
//	toDate32(agg)         Code: 43
//	toDateTime(agg)       Code: 43
//
// Before this fix these functions carried no domain, or a domain that
// checked only for a container, thus chgen answered a type (UInt8,
// Int16, Float32, ...) for a call the server refuses: a silently wrong
// type.
func TestAggregateFunctionStateRefusedAsCastArgument(t *testing.T) {
	agg := CHType{Name: "AggregateFunction", Params: []CHType{{Name: "uniq"}, {Name: "UInt64"}}}
	aggWithParens := CHType{Name: "AggregateFunction(uniq, UInt64)"}

	for _, name := range []string{
		"toUInt8", "toUInt16", "toUInt32", "toUInt64",
		"toInt8", "toInt16", "toInt32", "toInt64",
		"toFloat32", "toFloat64",
		"toDate", "toDate32", "toDateTime",
	} {
		domain, constrained := argumentDomainFor(name)
		if !constrained {
			t.Fatalf("%s declares no argument domain, thus it types a call the server refuses", name)
		}
		// Both spellings the schema parser and type inference can
		// produce must refuse. A predicate written against one
		// spelling alone reads as working and never fires on a real
		// schema column; see holdsAggregateFunctionState.
		if err := checkArgumentDomain(name, domain, agg); err == nil {
			t.Errorf("%s(%s) is accepted, but the server answers Code 43 (bare-name spelling)", name, agg.String())
		}
		if err := checkArgumentDomain(name, domain, aggWithParens); err == nil {
			t.Errorf("%s(%s) is accepted, but the server answers Code 43 (parenthesized spelling)", name, aggWithParens.String())
		}
	}

	// toDateTime64 is a separate domain (castArgumentDomain, not
	// dateFromValueArgumentDomain), because unlike toDate, toDate32 and
	// toDateTime it keeps accepting a Decimal.
	domain, constrained := argumentDomainFor("toDateTime64")
	if !constrained {
		t.Fatalf("toDateTime64 declares no argument domain")
	}
	if err := checkArgumentDomain("toDateTime64", domain, agg); err == nil {
		t.Errorf("toDateTime64(agg) is accepted, but the server answers Code 43")
	}
	if err := checkArgumentDomain("toDateTime64", domain, CHType{Name: "Decimal", Params: []CHType{{Name: "18"}, {Name: "4"}}}); err != nil {
		t.Errorf("toDateTime64(dec) must stay accepted: the server RUNS it; got %v", err)
	}
}

// TestCastArgumentDomainKeepsMeasuredAccepts is the negative half of
// TestAggregateFunctionStateRefusedAsCastArgument: every case that was
// already accepted before this fix must stay accepted. A rule that
// widens the refusal past its measured boundary breaks a query that
// runs, which this project treats as a defect too, just a milder one.
func TestCastArgumentDomainKeepsMeasuredAccepts(t *testing.T) {
	accepted := []CHType{
		{Name: "Int32"}, {Name: "UInt64"}, {Name: "Float64"},
		{Name: "String"}, {Name: "FixedString", LiteralParams: []string{"8"}},
	}
	for _, name := range []string{
		"toUInt8", "toInt16", "toFloat32", "toDate", "toDateTime64",
	} {
		domain, constrained := argumentDomainFor(name)
		if !constrained {
			t.Fatalf("%s declares no argument domain", name)
		}
		for _, argument := range accepted {
			if err := checkArgumentDomain(name, domain, argument); err != nil {
				t.Errorf("%s(%s) must stay accepted; got %v", name, argument.String(), err)
			}
		}
	}

	// hex and toString are the accepting exception: they read raw
	// bytes and never interpret them (measured: hex(agg) is String,
	// "00012CCBC234", RUNS).
	agg := CHType{Name: "AggregateFunction", Params: []CHType{{Name: "uniq"}, {Name: "UInt64"}}}
	hexDomain, constrained := argumentDomainFor("hex")
	if !constrained {
		t.Fatalf("hex declares no argument domain")
	}
	if err := checkArgumentDomain("hex", hexDomain, agg); err != nil {
		t.Errorf("hex(agg) must stay accepted: the server RUNS it; got %v", err)
	}
	if _, constrained := argumentDomainFor("tostring"); constrained {
		// toString has no domain at all; nothing to check, but a
		// future change that gives it one must not narrow past what
		// the server accepts. This branch documents the fact rather
		// than asserting it, since there is nothing to call yet.
		t.Log("toString now has a domain; re-verify it accepts an AggregateFunction state")
	}
}

// TestAggregateFunctionStateRefusedAsOrderingArgument holds the
// measured domain of min, max and the value-comparing position of
// argMin and argMax, the regression.
//
// Measured on ClickHouse 25.8.29.51 over real columns of an
// AggregatingMergeTree table, confirmed by a real SELECT:
//
//	max(aggif)               Code: 43  "... not comparable"
//	min(agg)                 Code: 43  same message
//	maxIf(agg, u8 = 1)       Code: 43  same message
//	argMax(i32, agg)         Code: 43  agg is the SECOND argument
//	argMinIf(isNull(i64), aggif, f64 < 2.5)
//	                         Code: 43  aggif is the SECOND argument
//
// argMax(agg, i32), the FIRST argument, keeps its type: an
// AggregateFunction state can be the value that argMax outputs, only
// not the key it compares by.
func TestAggregateFunctionStateRefusedAsOrderingArgument(t *testing.T) {
	agg := CHType{Name: "AggregateFunction", Params: []CHType{{Name: "uniq"}, {Name: "UInt64"}}}
	aggWithParens := CHType{Name: "AggregateFunction(uniq, UInt64)"}
	i32 := CHType{Name: "Int32"}

	for _, name := range []string{"max", "min", "maxif", "minif"} {
		domain, constrained := argumentDomainFor(name)
		if !constrained {
			t.Fatalf("%s declares no argument domain, thus it types a call the server refuses", name)
		}
		if err := checkArgumentDomain(name, domain, agg); err == nil {
			t.Errorf("%s(%s) is accepted, but the server answers Code 43 (bare-name spelling)", name, agg.String())
		}
		if err := checkArgumentDomain(name, domain, aggWithParens); err == nil {
			t.Errorf("%s(%s) is accepted, but the server answers Code 43 (parenthesized spelling)", name, aggWithParens.String())
		}
		// A normal value must keep comparing.
		if err := checkArgumentDomain(name, domain, i32); err != nil {
			t.Errorf("%s(i32) must stay accepted; got %v", name, err)
		}
	}

	for _, name := range []string{"argmax", "argmin", "argmaxif", "argminif"} {
		domain, constrained := argumentDomainFor(name)
		if !constrained {
			t.Fatalf("%s declares no argument domain", name)
		}
		indexes := domainArgumentIndexes(name)
		if len(indexes) != 1 || indexes[0] != 1 {
			t.Fatalf("%s must pin its domain to argument index 1 (the comparison key), got %v", name, indexes)
		}
		if err := checkArgumentDomain(name, domain, agg); err == nil {
			t.Errorf("%s: the comparison-key position must refuse an AggregateFunction state (bare name)", name)
		}
		if err := checkArgumentDomain(name, domain, aggWithParens); err == nil {
			t.Errorf("%s: the comparison-key position must refuse an AggregateFunction state (parenthesized)", name)
		}
	}
}

// TestArgMaxAcceptsAggregateStateAsOutputValue is the negative case
// that proves the domainArgs pin, not a blanket refusal, is what makes
// TestAggregateFunctionStateRefusedAsOrderingArgument pass. The FIRST
// argument of argMax and argMin, the value to output, must keep
// accepting an AggregateFunction state.
//
// Measured: argMax(agg, i32) is AggregateFunction(uniq, UInt64), RUNS.
func TestArgMaxAcceptsAggregateStateAsOutputValue(t *testing.T) {
	for _, name := range []string{"argmax", "argmin", "argmaxif", "argminif"} {
		indexes := domainArgumentIndexes(name)
		for _, index := range indexes {
			if index == 0 {
				t.Fatalf("%s must not constrain argument index 0, the output value", name)
			}
		}
	}
}
