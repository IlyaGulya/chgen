package engine

import (
	"regexp"
	"strings"
	"testing"

	clickhouse "github.com/AfterShip/clickhouse-sql-parser/parser"
)

// combinatorSchema carries the columns that the combinator rules need. The
// AggregateFunction and SimpleAggregateFunction columns let the -Merge rules
// be tested against a real state column, which is the only place where an
// AggregateFunction value can come from.
const combinatorSchema = `
CREATE TABLE t (
    i8    Int8,
    i32   Int32,
    u8    UInt8,
    f32   Float32,
    dec   Decimal(18, 4),
    b     Bool,
    s     String,
    lc    LowCardinality(String),
    lcn   LowCardinality(Nullable(String)),
    ni32  Nullable(Int32),
    dt    DateTime,
    arr_i Array(Int32),
    arr_n Array(Nullable(Int32)),
    arr_lc Array(LowCardinality(String))
);
CREATE TABLE agg (
    ss  AggregateFunction(sum, Int32),
    us  AggregateFunction(uniq, String),
    qs  AggregateFunction(quantile(0.5), Int32),
    as_ AggregateFunction(avg, Int32),
    ms  AggregateFunction(max, String),
    sif AggregateFunction(sumIf, Int32, UInt8),
    qif AggregateFunction(quantileIf(0.5), Int32, UInt8),
    sss SimpleAggregateFunction(sum, Int64),
    mss SimpleAggregateFunction(min, String),
    nss SimpleAggregateFunction(any, Nullable(Int32)),
    ass SimpleAggregateFunction(groupArrayArray, Array(Int32))
);
`

// combinatorCHType infers the ClickHouse type of one expression over the
// named table of combinatorSchema.
func combinatorCHType(t *testing.T, schema *Schema, table, exprSQL string) (string, error) {
	t.Helper()
	statements, err := clickhouse.NewParser("SELECT " + exprSQL + " FROM " + table).ParseStmts()
	if err != nil {
		return "", err
	}
	selectQuery, ok := statements[0].(*clickhouse.SelectQuery)
	if !ok {
		t.Fatalf("parse %q: not a SELECT", exprSQL)
	}
	scope, _, err := resolveScope(selectQuery, schema)
	if err != nil {
		return "", err
	}
	inferred, err := inferExprType(selectQuery.SelectItems[0].Expr, scope)
	if err != nil {
		return "", err
	}
	return inferred.String(), nil
}

// inferSelectItemCHType takes a full "SELECT <expr> AS a FROM <table>"
// statement, so each test case reads as the SQL that was measured.
func inferSelectItemCHType(t *testing.T, schema *Schema, sql string) (string, error) {
	t.Helper()
	match := regexp.MustCompile(`(?is)^SELECT\s+(.*?)\s+AS\s+a\s+FROM\s+(\w+)$`).FindStringSubmatch(strings.TrimSpace(sql))
	if match == nil {
		t.Fatalf("cannot split %q into an expression and a table", sql)
	}
	return combinatorCHType(t, schema, match[2], match[1])
}

func combinatorResultCHType(t *testing.T, sql string) (string, error) {
	t.Helper()
	schema, err := schemaFromDDLErr(t, combinatorSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	queries, err := parseQueriesWithSchema(t, "-- name: Q :one\n"+sql, schema)
	if err != nil {
		return "", err
	}
	return queries[0].Results[0].GoType, nil
}

// TestAggregateFunctionResultRefusesGoMapping locks the measured driver fact:
// clickhouse-go v2.47.0 cannot decode an AggregateFunction column at all. The
// read fails in the block decoder with `unsupported column type`, before any
// Go value is built, thus no Go type is correct for it. The refusal must name
// the construct.
func TestAggregateFunctionResultRefusesGoMapping(t *testing.T) {
	for _, sql := range []string{
		"SELECT ss AS a FROM agg",
		"SELECT us AS a FROM agg",
		"SELECT qs AS a FROM agg",
		"SELECT sumState(i32) AS a FROM t",
		"SELECT uniqState(s) AS a FROM t",
		"SELECT quantileState(0.5)(i32) AS a FROM t",
	} {
		got, err := combinatorResultCHType(t, sql)
		if err == nil {
			t.Errorf("%s: got Go type %q, want a refusal", sql, got)
			continue
		}
		if !strings.Contains(err.Error(), "AggregateFunction") {
			t.Errorf("%s: refusal %v does not name AggregateFunction", sql, err)
		}
	}
}

// TestAggregateStateCHTypes checks the ClickHouse type of a -State call, which
// stays useful for the INSERT path even though the read path refuses. The
// measured rule: -State keeps the data argument types verbatim, with
// LowCardinality removed and Nullable kept.
func TestAggregateStateCHTypes(t *testing.T) {
	schema, err := schemaFromDDLErr(t, combinatorSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	cases := []struct{ sql, want string }{
		{"SELECT sumState(i32) AS a FROM t", "AggregateFunction(sum, Int32)"},
		{"SELECT sumState(ni32) AS a FROM t", "AggregateFunction(sum, Nullable(Int32))"},
		{"SELECT uniqState(lc) AS a FROM t", "AggregateFunction(uniq, String)"},
		{"SELECT avgState(i32) AS a FROM t", "AggregateFunction(avg, Int32)"},
		{"SELECT minState(s) AS a FROM t", "AggregateFunction(min, String)"},
		{"SELECT maxState(dt) AS a FROM t", "AggregateFunction(max, DateTime)"},
		{"SELECT quantileState(0.5)(i32) AS a FROM t", "AggregateFunction(quantile(0.5), Int32)"},
		{"SELECT argMaxState(s, i32) AS a FROM t", "AggregateFunction(argMax, String, Int32)"},
	}
	for _, testCase := range cases {
		got, err := inferSelectItemCHType(t, schema, testCase.sql)
		if err != nil {
			t.Errorf("%s: error = %v", testCase.sql, err)
			continue
		}
		if got != testCase.want {
			t.Errorf("%s: CH type = %q, want %q", testCase.sql, got, testCase.want)
		}
	}
}

// TestIfStateCombinatorKeepsTheCondition locks the measured rule for the
// chained -IfState combinator, which is the MIRROR of -StateIf. The two
// suffix orders type differently: -IfState keeps the trailing condition
// argument inside the state and tags it "<base>If", while -StateIf (see
// TestAggregateStateCHTypes and the "dropsTopLevelNullable" cases) drops the
// condition and the top-level Nullable. Before this rule existed,
// quantileIfState had no dispatch rule at all: splitAggregateCombinator
// found no combinable base for "quantileif", so chgen refused the call as an
// unknown function.
func TestIfStateCombinatorKeepsTheCondition(t *testing.T) {
	schema, err := schemaFromDDLErr(t, combinatorSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	cases := []struct{ sql, want string }{
		{"SELECT quantileIfState(0.5)(f32, b) AS a FROM t", "AggregateFunction(quantileIf(0.5), Float32, Bool)"},
		{"SELECT sumIfState(i32, b) AS a FROM t", "AggregateFunction(sumIf, Int32, Bool)"},
		{"SELECT sumIfState(ni32, b) AS a FROM t", "AggregateFunction(sumIf, Nullable(Int32), Bool)"},
		// The condition argument keeps ITS OWN type verbatim: UInt8
		// survives unchanged and is not canonicalized to Bool.
		{"SELECT sumIfState(i32, u8) AS a FROM t", "AggregateFunction(sumIf, Int32, UInt8)"},
		// LowCardinality on the data argument is removed, as for plain
		// -State, and Nullable underneath it is kept.
		{"SELECT anyIfState(lc, b) AS a FROM t", "AggregateFunction(anyIf, String, Bool)"},
		{"SELECT anyIfState(lcn, b) AS a FROM t", "AggregateFunction(anyIf, Nullable(String), Bool)"},
	}
	for _, testCase := range cases {
		got, err := inferSelectItemCHType(t, schema, testCase.sql)
		if err != nil {
			t.Errorf("%s: error = %v", testCase.sql, err)
			continue
		}
		if got != testCase.want {
			t.Errorf("%s: CH type = %q, want %q", testCase.sql, got, testCase.want)
		}
	}
}

// TestIfStateAndStateIfDisagree pins BOTH halves of the mirror pair in one
// place, so a future change cannot collapse the two orders onto the same
// answer. Measured on ClickHouse 25.8.29.51: quantileIfState KEEPS the
// condition, quantileStateIf DROPS it.
func TestIfStateAndStateIfDisagree(t *testing.T) {
	schema, err := schemaFromDDLErr(t, combinatorSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	ifState, err := inferSelectItemCHType(t, schema, "SELECT quantileIfState(0.5)(f32, b) AS a FROM t")
	if err != nil {
		t.Fatalf("quantileIfState: error = %v", err)
	}
	stateIf, err := inferSelectItemCHType(t, schema, "SELECT quantileStateIf(0.5)(f32, b) AS a FROM t")
	if err != nil {
		t.Fatalf("quantileStateIf: error = %v", err)
	}
	if want := "AggregateFunction(quantileIf(0.5), Float32, Bool)"; ifState != want {
		t.Errorf("quantileIfState: CH type = %q, want %q", ifState, want)
	}
	if want := "AggregateFunction(quantile(0.5), Float32)"; stateIf != want {
		t.Errorf("quantileStateIf: CH type = %q, want %q", stateIf, want)
	}
	if ifState == stateIf {
		t.Errorf("quantileIfState and quantileStateIf must type differently, both gave %q", ifState)
	}
}

// TestIfStateCombinatorRefusesTheBaseDomain locks the second half of the
// -IfState rule: the domain check on the base aggregate still applies to
// the DATA argument, so a string argument to sumIfState refuses with the
// same Code 43 as sum(s) itself. Measured on ClickHouse 25.8.29.51.
func TestIfStateCombinatorRefusesTheBaseDomain(t *testing.T) {
	schema, err := schemaFromDDLErr(t, combinatorSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	got, err := inferSelectItemCHType(t, schema, "SELECT sumIfState(s, b) AS a FROM t")
	if err == nil {
		t.Fatalf("sumIfState(s, b): got CH type %q, want a refusal", got)
	}
}

// TestSimpleAggregateFunctionBehavesAsInnerType locks the measured fact that a
// SimpleAggregateFunction(f, T) column decodes exactly as T. The driver reports
// the scan type of T, so the Go mapping is the Go mapping of T.
func TestSimpleAggregateFunctionBehavesAsInnerType(t *testing.T) {
	cases := []struct{ sql, want string }{
		{"SELECT sss AS a FROM agg", "int64"},
		{"SELECT mss AS a FROM agg", "string"},
		{"SELECT nss AS a FROM agg", "*int32"},
		{"SELECT ass AS a FROM agg", "[]int32"},
	}
	for _, testCase := range cases {
		got, err := combinatorResultCHType(t, testCase.sql)
		if err != nil {
			t.Errorf("%s: error = %v", testCase.sql, err)
			continue
		}
		if got != testCase.want {
			t.Errorf("%s: Go type = %q, want %q", testCase.sql, got, testCase.want)
		}
	}
}

// TestMergeCombinatorReturnsAggregateResult locks the measured rule: -Merge
// gives the ordinary result type of the base aggregate over the argument type
// of the state, not the argument type itself.
func TestMergeCombinatorReturnsAggregateResult(t *testing.T) {
	schema, err := schemaFromDDLErr(t, combinatorSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	cases := []struct{ sql, want string }{
		{"SELECT sumMerge(ss) AS a FROM agg", "Int64"},
		{"SELECT uniqMerge(us) AS a FROM agg", "UInt64"},
		{"SELECT quantileMerge(0.5)(qs) AS a FROM agg", "Float64"},
		{"SELECT avgMerge(as_) AS a FROM agg", "Float64"},
		{"SELECT maxMerge(ms) AS a FROM agg", "String"},
		// quantileMerge with a DIFFERENT parameter than the state that
		// built qs is still legal: the server checks the aggregate
		// NAME, not the parameter. Measured on ClickHouse 25.8.29.51:
		// quantileMerge(0.9)(AggregateFunction(quantile(0.5), Int32))
		// gives Float64, the same as quantileMerge(0.5) would.
		{"SELECT quantileMerge(0.9)(qs) AS a FROM agg", "Float64"},
	}
	for _, testCase := range cases {
		got, err := inferSelectItemCHType(t, schema, testCase.sql)
		if err != nil {
			t.Errorf("%s: error = %v", testCase.sql, err)
			continue
		}
		if got != testCase.want {
			t.Errorf("%s: CH type = %q, want %q", testCase.sql, got, testCase.want)
		}
	}
}

// TestMergeCombinatorRefusesMismatchedAggregateName locks the measured rule:
// -Merge reads the aggregate NAME stored in Params[0] of the state, and
// refuses a state built by a different aggregate. Measured on ClickHouse
// 25.8.29.51 over a real AggregatingMergeTree table: every one of these
// calls gives Code 43 (ILLEGAL_TYPE_OF_ARGUMENT), regardless of family or
// of any parameter on either side.
func TestMergeCombinatorRefusesMismatchedAggregateName(t *testing.T) {
	schema, err := schemaFromDDLErr(t, combinatorSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	cases := []string{
		"SELECT sumMerge(as_) AS a FROM agg",          // sum over an avg state
		"SELECT avgMerge(ss) AS a FROM agg",           // avg over a sum state
		"SELECT uniqMerge(ss) AS a FROM agg",          // uniq over a sum state
		"SELECT maxMerge(as_) AS a FROM agg",          // max over an avg state (wrong family entirely)
		"SELECT quantileMerge(0.5)(ss) AS a FROM agg", // quantile over a sum state
	}
	for _, sql := range cases {
		_, err := inferSelectItemCHType(t, schema, sql)
		if err == nil {
			t.Errorf("%s: want an error for a mismatched aggregate name, got none", sql)
		}
	}
}

// TestIfMergeCombinatorChecksTheStateName is the regression test for
// the regression. -IfMerge is the CHAIN sum + If + Merge, and it must check the
// state against the name "<base>If", not "<base>". Measured on ClickHouse
// 25.8.29.51 over a real AggregatingMergeTree table with
// s AggregateFunction(sum, Int32) and si AggregateFunction(sumIf, Int32,
// UInt8):
//
//	sumIfMerge(si)   Int64    (name matches: sumIf)
//	sumIfMerge(s)    Code 43  (name differs: sum, not sumIf)
//
// Before the fix, sumIfMerge answered Int32 for BOTH rows: it fell through
// to a generic "ends in merge" fallback that skipped the name check and
// always answered Params[1] of the state, unchecked.
func TestIfMergeCombinatorChecksTheStateName(t *testing.T) {
	schema, err := schemaFromDDLErr(t, combinatorSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	got, err := inferSelectItemCHType(t, schema, "SELECT sumIfMerge(sif) AS a FROM agg")
	if err != nil {
		t.Fatalf("sumIfMerge(sif): error = %v", err)
	}
	if got != "Int64" {
		t.Errorf("sumIfMerge(sif): CH type = %q, want %q", got, "Int64")
	}
	if _, err := inferSelectItemCHType(t, schema, "SELECT sumIfMerge(ss) AS a FROM agg"); err == nil {
		t.Error("sumIfMerge(ss): want an error for a state built by sum, not sumIf; got none")
	}
}

// TestIfMergeCombinatorAcceptsAnAlias locks the fact that the name check
// of -IfMerge goes through the SAME alias table as plain -Merge: median is
// an alias of quantile, thus medianIfMerge must accept a state that
// quantileIf built. Measured on ClickHouse 25.8.29.51:
//
//	medianIfMerge(AggregateFunction(quantileIf(0.5), Int32, UInt8))  Float64
//	quantileIfMerge(0.9)(AggregateFunction(quantileIf(0.5), Int32, UInt8))  Float64
//
// The second row is legal with a DIFFERENT parameter than the state that
// built it: the server checks the name, quantileIf, and not the
// parameter.
func TestIfMergeCombinatorAcceptsAnAlias(t *testing.T) {
	schema, err := schemaFromDDLErr(t, combinatorSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	cases := []struct{ sql, want string }{
		{"SELECT medianIfMerge(qif) AS a FROM agg", "Float64"},
		{"SELECT quantileIfMerge(0.9)(qif) AS a FROM agg", "Float64"},
	}
	for _, testCase := range cases {
		got, err := inferSelectItemCHType(t, schema, testCase.sql)
		if err != nil {
			t.Errorf("%s: error = %v", testCase.sql, err)
			continue
		}
		if got != testCase.want {
			t.Errorf("%s: CH type = %q, want %q", testCase.sql, got, testCase.want)
		}
	}
	// quantileIfMerge over a state built WITHOUT the If form must still
	// refuse: quantile is not quantileIf. Measured Code 43.
	if _, err := inferSelectItemCHType(t, schema, "SELECT quantileIfMerge(0.5)(qs) AS a FROM agg"); err == nil {
		t.Error("quantileIfMerge(qs): want an error for a state built by quantile, not quantileIf; got none")
	}
}

// TestMergeStateCombinatorReturnsTheStateVerbatim locks the measured rule
// for -MergeState, which the regression found to have NO entry at all: it reads
// a state, checks the state's name the same way as -Merge, and gives the
// state back with its value types UNCHANGED, but with the aggregate name's
// own parametric literal (if any) taken from THIS call, not from the
// state. Measured on ClickHouse 25.8.29.51:
//
//	sumMergeState(AggregateFunction(sum, Int32))
//	    -> AggregateFunction(sum, Int32)
//	avgMergeState(AggregateFunction(sum, Int32))
//	    -> Code 43 (name differs: sum, not avg)
//	quantileMergeState(0.9)(AggregateFunction(quantile(0.5), Int32))
//	    -> AggregateFunction(quantile(0.9), Int32)   (RE-TAGS the parameter)
//	sumIfMergeState(AggregateFunction(sumIf, Int32, UInt8))
//	    -> AggregateFunction(sumIf, Int32, UInt8)
func TestMergeStateCombinatorReturnsTheStateVerbatim(t *testing.T) {
	schema, err := schemaFromDDLErr(t, combinatorSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	cases := []struct{ sql, want string }{
		{"SELECT sumMergeState(ss) AS a FROM agg", "AggregateFunction(sum, Int32)"},
		{"SELECT quantileMergeState(0.9)(qs) AS a FROM agg", "AggregateFunction(quantile(0.9), Int32)"},
		{"SELECT sumIfMergeState(sif) AS a FROM agg", "AggregateFunction(sumIf, Int32, UInt8)"},
	}
	for _, testCase := range cases {
		got, err := inferSelectItemCHType(t, schema, testCase.sql)
		if err != nil {
			t.Errorf("%s: error = %v", testCase.sql, err)
			continue
		}
		if got != testCase.want {
			t.Errorf("%s: CH type = %q, want %q", testCase.sql, got, testCase.want)
		}
	}
	if _, err := inferSelectItemCHType(t, schema, "SELECT avgMergeState(ss) AS a FROM agg"); err == nil {
		t.Error("avgMergeState(ss): want an error for a state built by sum, not avg; got none")
	}
}

// TestGenericMergeFallbackAlwaysRefuses locks the tightened fallback of
// infer_function.go: a name that ends in "merge" but that
// inferAggregateCombinatorType did not handle must ALWAYS refuse, never
// guess Params[1] of the state. Measured on ClickHouse 25.8.29.51:
// sumOrNullMerge over a state that a BARE sum built is Code 43,
// "corresponds to different aggregate function: sum instead of
// sumOrNull", because the name check requires a state named sumOrNull.
// Before the fix this path had no name check at all and answered Int32.
func TestGenericMergeFallbackAlwaysRefuses(t *testing.T) {
	schema, err := schemaFromDDLErr(t, combinatorSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	if got, err := inferSelectItemCHType(t, schema, "SELECT sumOrNullMerge(ss) AS a FROM agg"); err == nil {
		t.Errorf("sumOrNullMerge(ss): want an error, got %q", got)
	}
}

// TestIfCombinatorMatchesBaseAggregate locks the measured rule: -If has the
// type of the base aggregate. The trailing condition argument never changes
// the result type, and it never contributes a Nullable wrapper.
func TestIfCombinatorMatchesBaseAggregate(t *testing.T) {
	schema, err := schemaFromDDLErr(t, combinatorSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	cases := []struct{ sql, want string }{
		{"SELECT sumIf(i32, b) AS a FROM t", "Int64"},
		{"SELECT avgIf(i32, b) AS a FROM t", "Float64"},
		{"SELECT uniqIf(s, b) AS a FROM t", "UInt64"},
		{"SELECT anyIf(lc, b) AS a FROM t", "String"},
		{"SELECT anyIf(lcn, b) AS a FROM t", "Nullable(String)"},
		{"SELECT anyIf(ni32, b) AS a FROM t", "Nullable(Int32)"},
		{"SELECT sumIf(dec, b) AS a FROM t", "Decimal(38, 4)"},
		{"SELECT medianIf(i32, b) AS a FROM t", "Float64"},
		{"SELECT groupArrayIf(ni32, b) AS a FROM t", "Array(Int32)"},
		{"SELECT argMaxIf(s, i32, b) AS a FROM t", "String"},
	}
	for _, testCase := range cases {
		got, err := inferSelectItemCHType(t, schema, testCase.sql)
		if err != nil {
			t.Errorf("%s: error = %v", testCase.sql, err)
			continue
		}
		if got != testCase.want {
			t.Errorf("%s: CH type = %q, want %q", testCase.sql, got, testCase.want)
		}
	}
}

// TestArrayCombinatorAggregatesOverElements locks the measured rule: -Array
// applies the base aggregate to the element type of the array argument.
func TestArrayCombinatorAggregatesOverElements(t *testing.T) {
	schema, err := schemaFromDDLErr(t, combinatorSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	cases := []struct{ sql, want string }{
		{"SELECT sumArray(arr_i) AS a FROM t", "Int64"},
		{"SELECT avgArray(arr_i) AS a FROM t", "Float64"},
		{"SELECT maxArray(arr_lc) AS a FROM t", "String"},
		{"SELECT uniqArray(arr_lc) AS a FROM t", "UInt64"},
		{"SELECT sumArray(arr_n) AS a FROM t", "Nullable(Int64)"},
		{"SELECT maxArray(arr_n) AS a FROM t", "Nullable(Int32)"},
		{"SELECT anyArray(arr_n) AS a FROM t", "Nullable(Int32)"},
	}
	for _, testCase := range cases {
		got, err := inferSelectItemCHType(t, schema, testCase.sql)
		if err != nil {
			t.Errorf("%s: error = %v", testCase.sql, err)
			continue
		}
		if got != testCase.want {
			t.Errorf("%s: CH type = %q, want %q", testCase.sql, got, testCase.want)
		}
	}
}

// TestOrNullAndOrDefaultCombinators locks the measured rule: -OrNull always
// adds Nullable, and -OrDefault keeps the base result type unchanged.
func TestOrNullAndOrDefaultCombinators(t *testing.T) {
	schema, err := schemaFromDDLErr(t, combinatorSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	cases := []struct{ sql, want string }{
		{"SELECT sumOrNull(i32) AS a FROM t", "Nullable(Int64)"},
		{"SELECT avgOrNull(i32) AS a FROM t", "Nullable(Float64)"},
		{"SELECT maxOrNull(s) AS a FROM t", "Nullable(String)"},
		{"SELECT uniqOrNull(s) AS a FROM t", "Nullable(UInt64)"},
		{"SELECT sumOrNull(ni32) AS a FROM t", "Nullable(Int64)"},
		{"SELECT quantileOrNull(0.5)(i32) AS a FROM t", "Nullable(Float64)"},
		{"SELECT minOrNull(lc) AS a FROM t", "Nullable(String)"},
		{"SELECT sumOrDefault(i32) AS a FROM t", "Int64"},
		{"SELECT maxOrDefault(s) AS a FROM t", "String"},
		{"SELECT avgOrDefault(i32) AS a FROM t", "Float64"},
		{"SELECT sumOrDefault(ni32) AS a FROM t", "Nullable(Int64)"},
	}
	for _, testCase := range cases {
		got, err := inferSelectItemCHType(t, schema, testCase.sql)
		if err != nil {
			t.Errorf("%s: error = %v", testCase.sql, err)
			continue
		}
		if got != testCase.want {
			t.Errorf("%s: CH type = %q, want %q", testCase.sql, got, testCase.want)
		}
	}
}

// TestResampleCombinatorDropsNullable locks a measured rule that the function
// name does not suggest: -Resample gives an Array of the base result and it
// REMOVES the Nullable wrapper that the plain aggregate keeps. Measured on
// ClickHouse 25.8.29.51: max(ni32) is Nullable(Int32) while
// maxResample(0, 10, 1)(ni32, i8) is Array(Int32).
func TestResampleCombinatorDropsNullable(t *testing.T) {
	schema, err := schemaFromDDLErr(t, combinatorSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	cases := []struct{ sql, want string }{
		{"SELECT sumResample(0, 10, 1)(i32, i8) AS a FROM t", "Array(Int64)"},
		{"SELECT avgResample(0, 10, 1)(i32, i8) AS a FROM t", "Array(Float64)"},
		{"SELECT uniqResample(0, 10, 1)(s, i8) AS a FROM t", "Array(UInt64)"},
		{"SELECT maxResample(0, 10, 1)(ni32, i8) AS a FROM t", "Array(Int32)"},
		{"SELECT sumResample(0, 10, 1)(ni32, i8) AS a FROM t", "Array(Int64)"},
		{"SELECT anyResample(0, 10, 1)(ni32, i8) AS a FROM t", "Array(Int32)"},
	}
	for _, testCase := range cases {
		got, err := inferSelectItemCHType(t, schema, testCase.sql)
		if err != nil {
			t.Errorf("%s: error = %v", testCase.sql, err)
			continue
		}
		if got != testCase.want {
			t.Errorf("%s: CH type = %q, want %q", testCase.sql, got, testCase.want)
		}
	}
}

// TestSimpleStateCombinator locks the measured rule: -SimpleState gives
// SimpleAggregateFunction(base, R), where R is the ordinary result type of the
// base aggregate. It is not the argument type: sumSimpleState(Int8) gives
// SimpleAggregateFunction(sum, Int64).
func TestSimpleStateCombinator(t *testing.T) {
	schema, err := schemaFromDDLErr(t, combinatorSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	cases := []struct{ sql, want string }{
		{"SELECT sumSimpleState(i8) AS a FROM t", "SimpleAggregateFunction(sum, Int64)"},
		{"SELECT sumSimpleState(u8) AS a FROM t", "SimpleAggregateFunction(sum, UInt64)"},
		{"SELECT sumSimpleState(f32) AS a FROM t", "SimpleAggregateFunction(sum, Float64)"},
		{"SELECT maxSimpleState(i8) AS a FROM t", "SimpleAggregateFunction(max, Int8)"},
		{"SELECT anySimpleState(ni32) AS a FROM t", "SimpleAggregateFunction(any, Nullable(Int32))"},
		{"SELECT anySimpleState(lc) AS a FROM t", "SimpleAggregateFunction(any, String)"},
		{"SELECT sumSimpleState(ni32) AS a FROM t", "SimpleAggregateFunction(sum, Nullable(Int64))"},
	}
	for _, testCase := range cases {
		got, err := inferSelectItemCHType(t, schema, testCase.sql)
		if err != nil {
			t.Errorf("%s: error = %v", testCase.sql, err)
			continue
		}
		if got != testCase.want {
			t.Errorf("%s: CH type = %q, want %q", testCase.sql, got, testCase.want)
		}
	}
}

// TestSimpleStateResultGoTypeIsInnerType checks that a -SimpleState result
// maps to the Go type of its inner type, because the driver decodes a
// SimpleAggregateFunction exactly as its inner type.
func TestSimpleStateResultGoTypeIsInnerType(t *testing.T) {
	cases := []struct{ sql, want string }{
		{"SELECT sumSimpleState(i8) AS a FROM t", "int64"},
		{"SELECT minSimpleState(s) AS a FROM t", "string"},
		{"SELECT anySimpleState(ni32) AS a FROM t", "*int32"},
	}
	for _, testCase := range cases {
		got, err := combinatorResultCHType(t, testCase.sql)
		if err != nil {
			t.Errorf("%s: error = %v", testCase.sql, err)
			continue
		}
		if got != testCase.want {
			t.Errorf("%s: Go type = %q, want %q", testCase.sql, got, testCase.want)
		}
	}
}

// TestUnknownCombinatorBaseStillRefuses guards the policy: a combinator over a
// base aggregate that has no type rule must stay a refusal, not become a guess.
func TestUnknownCombinatorBaseStillRefuses(t *testing.T) {
	schema, err := schemaFromDDLErr(t, combinatorSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	for _, sql := range []string{
		"SELECT topKIf(3)(s, b) AS a FROM t",
		"SELECT skewPopArray(arr_i) AS a FROM t",
		"SELECT corrOrNull(i32, i32) AS a FROM t",
	} {
		if got, err := inferSelectItemCHType(t, schema, sql); err == nil {
			t.Errorf("%s: got %q, want a refusal for an unknown base aggregate", sql, got)
		}
	}
}

// TestCombinableBaseAggregatesHaveARule is the guard for the combinator
// path. It mirrors TestFunctionWrapperClassCoversEveryRule: a combinator
// rule derives its answer from the type rule and the wrapper class of its
// base aggregate, thus every base named in isCombinableBaseAggregate must
// have both. A base without them would make the combinator guess.
func TestCombinableBaseAggregatesHaveARule(t *testing.T) {
	bases := []string{
		"sum", "min", "max", "any", "anylast", "avg",
		"argmin", "argmax", "count", "uniq", "uniqexact", "uniqcombined",
		"quantile", "median", "grouparray", "groupuniqarray",
		"groupbitand", "groupbitor", "groupbitxor",
	}
	for _, base := range bases {
		if !isCombinableBaseAggregate(base) {
			t.Errorf("base %q is not listed as combinable, but the guard expects it", base)
			continue
		}
		if _, ok := functionRuleFor(base); !ok {
			t.Errorf("combinable base %q has no type rule", base)
		}
		if _, ok := functionRegistry[base]; !ok {
			t.Errorf("combinable base %q has no registry spec", base)
		}
	}
	// The reverse direction: nothing may be combinable without appearing
	// in the list above, so the list cannot fall behind the function.
	for name := range functionRegistry {
		if !isCombinableBaseAggregate(name) {
			continue
		}
		found := false
		for _, base := range bases {
			if base == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("function %q is combinable but the guard list does not name it", name)
		}
	}
}

// TestCombinatorDoesNotSplitOrdinaryFunctions guards against a name that
// merely ends in the letters of a combinator. `if`, `array` and `arrayIf`
// have their own meaning and must not be read as a combinator over a base
// aggregate named "" or "arr".
func TestCombinatorDoesNotSplitOrdinaryFunctions(t *testing.T) {
	for _, name := range []string{"if", "array", "multiif", "tostring", "arraysort", "isnull", "notempty"} {
		if base, _, ok := splitAggregateCombinator(name); ok {
			t.Errorf("splitAggregateCombinator(%q) split into base %q, want no split", name, base)
		}
	}
}

// TestUnsupportedSimpleStateRefuses locks the measured restriction:
// ClickHouse accepts -SimpleState only for a named list of aggregates.
// medianSimpleState and uniqSimpleState are BAD_ARGUMENTS on the server,
// thus chgen must refuse instead of inventing a type.
func TestUnsupportedSimpleStateRefuses(t *testing.T) {
	schema, err := schemaFromDDLErr(t, combinatorSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	for _, sql := range []string{
		"SELECT medianSimpleState(i32) AS a FROM t",
		"SELECT uniqSimpleState(s) AS a FROM t",
		"SELECT avgSimpleState(i32) AS a FROM t",
		"SELECT countSimpleState(i32) AS a FROM t",
	} {
		if got, err := inferSelectItemCHType(t, schema, sql); err == nil {
			t.Errorf("%s: got %q, want a refusal for an unsupported SimpleState base", sql, got)
		}
	}
}

// TestMedianStateUsesQuantileName locks a measured canonicalization:
// median is an alias of quantile, and ClickHouse prints the canonical name
// inside the state type. medianState(i32) is AggregateFunction(quantile,
// Int32), not AggregateFunction(median, Int32).
func TestMedianStateUsesQuantileName(t *testing.T) {
	schema, err := schemaFromDDLErr(t, combinatorSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	cases := []struct{ sql, want string }{
		{"SELECT medianState(i32) AS a FROM t", "AggregateFunction(quantile, Int32)"},
		{"SELECT medianState(ni32) AS a FROM t", "AggregateFunction(quantile, Nullable(Int32))"},
	}
	for _, testCase := range cases {
		got, err := inferSelectItemCHType(t, schema, testCase.sql)
		if err != nil {
			t.Errorf("%s: error = %v", testCase.sql, err)
			continue
		}
		if got != testCase.want {
			t.Errorf("%s: CH type = %q, want %q", testCase.sql, got, testCase.want)
		}
	}
}

// TestCompositeCombinatorFormsThatAlwaysRefuse pins the ClickHouse composite
// combinator spellings that this repository has ZERO test coverage for
// today, measured on ClickHouse 25.8.29.51 to REFUSE with no wrong type.
// chgen already refuses each of them (no dispatch rule matches the whole
// name), and this test freezes that refusal so it cannot silently turn into
// a typed answer later, for example if a future combinator rule widens by
// accident.
//
// Each refusal below was checked at the SERVER, not assumed from the chgen
// answer alone:
//
//	sumIfArray(arr_i, b)         Code 43  "of argument for aggregate function
//	                                       with Array suffix. Must be array"
//	                                       (the trailing Bool condition lands
//	                                       where -Array expects an Array)
//	sumIfResample(0,10,1)(i32,b) Code 43  "of last argument for aggregate
//	                                       function with If suffix" (the
//	                                       Resample bounds consume the slot
//	                                       that -If needs for its own
//	                                       trailing condition)
//	sumIfSimpleState(i32, b)     Code 36  BAD_ARGUMENTS, "Unsupported
//	                                       aggregate function sumIf"
//	                                       (-SimpleState accepts only a
//	                                       fixed list of bases, and no -If
//	                                       form is in it)
//
// The MIRROR orders of the first two are NOT pinned as gaps: they are
// genuine, final refusals, and chgen now types the other order.
// sumArrayIf(arr_i, b) is Int64 and sumResampleIf(0,10,1)(i32, i32, b) is
// Array(Int64) on the live server (see TestCompositeCombinatorFormsChgenNowTypes),
// while sumIfArray and sumIfResample stay Code 43 on the server itself, so
// chgen's refusal of those two is correct and permanent, not a coverage
// gap.
func TestCompositeCombinatorFormsThatAlwaysRefuse(t *testing.T) {
	schema, err := schemaFromDDLErr(t, combinatorSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	for _, sql := range []string{
		"SELECT sumIfArray(arr_i, b) AS a FROM t",
		"SELECT sumIfResample(0, 10, 1)(i32, b) AS a FROM t",
		"SELECT sumIfSimpleState(i32, b) AS a FROM t",
	} {
		if got, err := inferSelectItemCHType(t, schema, sql); err == nil {
			t.Errorf("%s: got CH type %q, want a refusal", sql, got)
		}
	}
}

// TestCompositeCombinatorFormsChgenNowTypes locks the five chained
// combinator forms that the live server ACCEPTS and that chgen used to
// refuse (see TestCompositeCombinatorGapsStillRefuse, replaced by this
// test once the dispatch rules landed). Each measurement was taken on
// ClickHouse 25.8.29.51 over real columns of combinatorSchema, never over
// a literal, with DESCRIBE / a materialized column type, never
// toTypeName:
//
//	sumArrayIf(arr_i, b)              Int64            (base+Array+If chain)
//	sumArrayIf(arr_n, b)              Nullable(Int64)   (Array(Nullable) element)
//	sumResampleIf(0,10,1)(i32,i32,b)  Array(Int64)      (base+Resample+If chain)
//	sumIfOrNull(i32, b)               Nullable(Int64)   (base+If+OrNull chain)
//	sumIfOrDefault(i32, b)            Int64             (base+If+OrDefault chain)
//	sumStateOrNull(i32)               AggregateFunction(sumOrNull, Int32)
//	                                                    (base+State+OrNull
//	                                                     chain; the state's
//	                                                     own NAME is tagged
//	                                                     sumOrNull, not sum)
//
// The MIRROR orders (sumIfArray, sumIfResample) and -SimpleState chained
// with -If stay pinned as refusals in
// TestCompositeCombinatorFormsThatAlwaysRefuse above: the server itself
// refuses those, with Code 43 or Code 36, so chgen's refusal is correct
// and final, not a gap.
func TestCompositeCombinatorFormsChgenNowTypes(t *testing.T) {
	schema, err := schemaFromDDLErr(t, combinatorSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	cases := []struct{ sql, want string }{
		{"SELECT sumArrayIf(arr_i, b) AS a FROM t", "Int64"},
		{"SELECT sumArrayIf(arr_n, b) AS a FROM t", "Nullable(Int64)"},
		{"SELECT sumResampleIf(0, 10, 1)(i32, i32, b) AS a FROM t", "Array(Int64)"},
		{"SELECT sumIfOrNull(i32, b) AS a FROM t", "Nullable(Int64)"},
		{"SELECT sumIfOrDefault(i32, b) AS a FROM t", "Int64"},
		{"SELECT sumStateOrNull(i32) AS a FROM t", "AggregateFunction(sumOrNull, Int32)"},
	}
	for _, testCase := range cases {
		got, err := inferSelectItemCHType(t, schema, testCase.sql)
		if err != nil {
			t.Errorf("%s: error = %v, want CH type %q", testCase.sql, err, testCase.want)
			continue
		}
		if got != testCase.want {
			t.Errorf("%s: CH type = %q, want %q", testCase.sql, got, testCase.want)
		}
	}
}

// TestStateOrNullRefusesUnsupportedBases locks a domain restriction found
// while measuring -StateOrNull for the regression: it is NOT the same
// accept-set as plain -OrNull. uniqOrNull(i32) is Nullable(UInt64) and is
// accepted, but uniqStateOrNull(i32) is refused by the server itself with
// Code 43, "Nested type AggregateFunction(uniq, Int32) cannot be inside
// Nullable type", because the refusal is on whether the aggregate's
// STATE implementation supports the wrapper, not on the ordinary result
// shape that canBeInsideNullable checks. Measured on ClickHouse
// 25.8.29.51 over real columns of combinatorSchema. Without this test, a
// naive reuse of the -OrNull rule for -StateOrNull would answer a typed
// AggregateFunction where the server refuses: the worst defect class.
func TestStateOrNullRefusesUnsupportedBases(t *testing.T) {
	schema, err := schemaFromDDLErr(t, combinatorSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	for _, sql := range []string{
		"SELECT countStateOrNull(i32) AS a FROM t",
		"SELECT uniqStateOrNull(i32) AS a FROM t",
		"SELECT uniqExactStateOrNull(i32) AS a FROM t",
		"SELECT groupArrayStateOrNull(i32) AS a FROM t",
		"SELECT groupUniqArrayStateOrNull(i32) AS a FROM t",
	} {
		if got, err := inferSelectItemCHType(t, schema, sql); err == nil {
			t.Errorf("%s: got CH type %q, want a refusal (server refuses with Code 43)", sql, got)
		}
	}
	// The accepted half, so this test cannot pass merely by having
	// stateornull ALWAYS refuse. sumStateOrNull is already pinned in
	// TestCompositeCombinatorFormsChgenNowTypes; this adds two more bases
	// to prove the check really is per-base and not a coincidence.
	accepted := []struct{ sql, want string }{
		{"SELECT avgStateOrNull(i32) AS a FROM t", "AggregateFunction(avgOrNull, Int32)"},
		{"SELECT maxStateOrNull(i32) AS a FROM t", "AggregateFunction(maxOrNull, Int32)"},
	}
	for _, testCase := range accepted {
		got, err := inferSelectItemCHType(t, schema, testCase.sql)
		if err != nil {
			t.Errorf("%s: error = %v, want CH type %q", testCase.sql, err, testCase.want)
			continue
		}
		if got != testCase.want {
			t.Errorf("%s: CH type = %q, want %q", testCase.sql, got, testCase.want)
		}
	}
}
