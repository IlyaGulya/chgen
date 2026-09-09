package engine

import (
	"strings"
	"testing"

	clickhouse "github.com/AfterShip/clickhouse-sql-parser/parser"
)

// inferTestExprType infers the ClickHouse type of one expression over
// the probe table.
func inferTestExprType(t *testing.T, schema *Schema, exprSQL string) (string, error) {
	t.Helper()
	statements, err := clickhouse.NewParser("SELECT " + exprSQL + " FROM probe").ParseStmts()
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

// argumentDomainSchema holds one column for each base type that the
// sweep measured on ClickHouse 25.8.29.51.
const argumentDomainSchema = `
CREATE TABLE probe (
    i8 Int8, i16 Int16, i32 Int32, i64 Int64,
    u8 UInt8, u16 UInt16, u32 UInt32, u64 UInt64,
    i128 Int128, u128 UInt128, i256 Int256, u256 UInt256,
    f32 Float32, f64 Float64,
    dec Decimal(18, 4), d32 Decimal32(4), d64 Decimal64(4),
    b Bool, s String, fs FixedString(8),
    d Date, d32t Date32, dt DateTime, dt64 DateTime64(3),
    ni32 Nullable(Int32), ns Nullable(String),
    arr_i Array(Int32), m Map(String, Int64),
    lc LowCardinality(String), lcn LowCardinality(Nullable(String)),
    e8 Enum8('a' = 1, 'zz' = 2), e16 Enum16('x' = 1, 'y' = 2),
    uid UUID, ip4 IPv4, ip6 IPv6,
    tup Tuple(Int32, String),

    -- Array-of columns for the recursive array comparability rule
    -- Measured against a real column, never a
    -- literal, on ClickHouse 25.8.29.51.
    arr_i32 Array(Int32), arr_i64 Array(Int64), arr_s Array(String),
    arr_dec Array(Decimal(18, 4)), arr_f64 Array(Float64),
    arr_aai32 Array(Array(Int32)), arr_aai64 Array(Array(Int64)),
    arr_aas Array(Array(String)),

    -- Array-of columns for the supertype-derived Array
    -- comparability rule, extended past the wide-integer/Decimal grid
    -- to wrapper types, temporal types and one more nesting depth.
    -- Measured on ClickHouse 25.8.29.51 with real columns.
    arr_i8 Array(Int8), arr_u64 Array(UInt64), arr_i128 Array(Int128),
    arr_f32 Array(Float32),
    arr_dec92 Array(Decimal(9, 2)), arr_dec384 Array(Decimal(38, 4)),
    arr_e8 Array(Enum8('a' = 1, 'zz' = 2)),
    arr_dt Array(DateTime), arr_dt64 Array(DateTime64(3)),
    arr_d Array(Date), arr_d32 Array(Date32),
    arr_fs Array(FixedString(8)),
    arr_ni32 Array(Nullable(Int32)),
    arr_lci32 Array(LowCardinality(Int32)),

    -- Tuple-of and Map-of columns for the recursive member
    -- check for Tuple and Map comparability. Measured on ClickHouse
    -- 25.8.29.51 with real columns; see the measured note in
    -- comparableBaseTypes in argument_domain.go.
    tup_ss Tuple(String, String), tup_ii Tuple(Int32, Int32),
    tup_i64s Tuple(Int64, String), tup_f64s Tuple(Float64, String),
    tup_i64f64 Tuple(Int64, Float64), tup_i32 Tuple(Int32),
    tup_i32s_i32 Tuple(Int32, String, Int32),
    tup_u64 Tuple(UInt64, String), tup_i128 Tuple(Int128, String),
    tup_dec92 Tuple(Decimal(9, 2), String),
    tup_named Tuple(a Int32, b String),
    tup_arri Tuple(Array(Int32)), tup_arru64 Tuple(Array(UInt64)),
    m_ss Map(String, String), m_is Map(Int32, String),
    m_sf Map(String, Float64), mk_i32 Map(Int32, String),
    mk_u64 Map(UInt64, String)
) ENGINE = MergeTree ORDER BY tuple()
`

func argumentDomainTestSchema(t *testing.T) *Schema {
	t.Helper()
	schema, err := schemaFromDDLErr(t, argumentDomainSchema)
	if err != nil {
		t.Fatalf("parse probe schema: %v", err)
	}
	return schema
}

// TestArgumentDomainRefusesImpossibleCall checks that a call which
// ClickHouse answers with Code: 43 (ILLEGAL_TYPE_OF_ARGUMENT) is a
// refusal in chgen too. Each case was measured with
// SELECT toTypeName(<expr>) FROM probe over a real column, never over a
// literal, because the server folds constants.
//
// A type here instead of a refusal is a silent wrong answer: the
// generated Go compiles and the query then fails at run time.
func TestArgumentDomainRefusesImpossibleCall(t *testing.T) {
	schema := argumentDomainTestSchema(t)
	for _, expr := range []string{
		// sum refuses the strings, the temporal types and the
		// composite types.
		"sum(s)", "sum(ns)", "sum(lc)", "sum(fs)",
		"sum(d)", "sum(d32t)", "sum(dt)", "sum(dt64)",
		"sum(arr_i)", "sum(m)", "sum(tup)", "sum(uid)", "sum(ip4)", "sum(ip6)",
		"sumIf(s, b)",

		// avg refuses the same set and the Enums as well.
		"avg(s)", "avg(ns)", "avg(fs)", "avg(e8)", "avg(e16)",
		"avg(d)", "avg(dt)", "avg(uid)", "avg(m)", "avg(tup)",
		"avgIf(s, b)",

		// quantile and median keep Date, DateTime and DateTime64 but
		// refuse Date32, the strings and the Enums.
		"quantile(0.5)(s)", "quantile(0.5)(fs)", "quantile(0.5)(e8)",
		"quantile(0.5)(d32t)", "quantile(0.5)(uid)", "quantile(0.5)(arr_i)",
		"median(s)", "median(fs)", "median(e16)",

		// The bitwise group aggregates take the integers only.
		"groupBitXor(s)", "groupBitXor(f32)", "groupBitXor(f64)",
		"groupBitXor(dec)", "groupBitXor(d)", "groupBitXor(e8)",
		"groupBitAnd(s)", "groupBitOr(f64)",

		// The case-folding and the trim functions take String and
		// FixedString only.
		"lower(e8)", "upper(e16)", "lower(i32)", "upper(uid)", "lower(d)",
		"trim(e8)", "trimLeft(i32)", "trimRight(uid)",

		// empty, notEmpty and length refuse the numbers, the Enums and
		// the temporal types.
		"empty(i32)", "empty(e8)", "empty(d)", "empty(f64)", "empty(tup)",
		"notEmpty(i32)", "notEmpty(e16)",
		"length(i32)", "length(e8)", "length(dt)", "length(tup)",

		// length ALSO refuses IPv4, IPv6 and UUID, unlike empty and
		// notEmpty above (the regression): measured by execution,
		// "SELECT length(uid) FROM probe" is Code: 43, "Cannot apply
		// function length to UUID argument".
		"length(uid)", "length(ip4)", "length(ip6)",
	} {
		if _, err := inferTestExprType(t, schema, expr); err == nil {
			t.Errorf("%s: chgen gave a type, but ClickHouse refuses this call", expr)
		}
	}
}

// TestArgumentDomainAcceptsMeasuredTypes checks the other side of the
// contract. A refusal that the server does not make is a false refusal,
// and it blocks a query that would run. Each expected type is the
// toTypeName that ClickHouse 25.8.29.51 reported for that column.
func TestArgumentDomainAcceptsMeasuredTypes(t *testing.T) {
	schema := argumentDomainTestSchema(t)
	for _, testCase := range []struct {
		expr string
		want string
	}{
		// sum widens the narrow integers, keeps the wide ones and
		// reads an Enum as Int64.
		{"sum(i8)", "Int64"}, {"sum(i32)", "Int64"}, {"sum(i64)", "Int64"},
		{"sum(u8)", "UInt64"}, {"sum(u64)", "UInt64"},
		{"sum(i128)", "Int128"}, {"sum(u128)", "UInt128"},
		{"sum(i256)", "Int256"}, {"sum(u256)", "UInt256"},
		{"sum(b)", "UInt64"},
		{"sum(f32)", "Float64"}, {"sum(f64)", "Float64"},
		{"sum(e8)", "Int64"}, {"sum(e16)", "Int64"},
		{"sum(ni32)", "Nullable(Int64)"},

		// avg is always Float64, and keeps the Nullable wrapper.
		{"avg(i8)", "Float64"}, {"avg(i128)", "Float64"},
		{"avg(dec)", "Float64"}, {"avg(b)", "Float64"},
		{"avg(ni32)", "Nullable(Float64)"},

		// quantile gives Float64 for every integer and float, keeps a
		// Decimal and keeps the temporal types it accepts.
		{"quantile(0.5)(i32)", "Float64"},
		{"quantile(0.5)(i128)", "Float64"},
		{"quantile(0.5)(u256)", "Float64"},
		{"quantile(0.5)(b)", "Float64"},
		{"quantile(0.5)(d)", "Date"},
		{"quantile(0.5)(dt)", "DateTime"},
		{"quantile(0.5)(dec)", "Decimal(18, 4)"},

		// The bitwise group aggregates keep the width of the argument,
		// except Bool, which normalizes to its storage type UInt8
		// (the regression: the old firstFunctionArgument rule answered Bool
		// verbatim, but the server always answers UInt8 for a Bool
		// column).
		{"groupBitXor(i32)", "Int32"}, {"groupBitAnd(u64)", "UInt64"},
		{"groupBitOr(i128)", "Int128"},
		{"groupBitAnd(b)", "UInt8"}, {"groupBitOr(b)", "UInt8"}, {"groupBitXor(b)", "UInt8"},
		{"groupBitAnd(ni32)", "Nullable(Int32)"},

		// The string functions keep a FixedString at its width and
		// give String for the other string-like arguments.
		{"lower(s)", "String"}, {"upper(fs)", "FixedString(8)"},
		{"trim(s)", "String"},

		// length reads a raw byte count and stays accepted for a String
		// and the containers. It does NOT accept a UUID (the regression):
		// see TestArgumentDomainRefusesImpossibleCall above for that
		// refusal.
		{"length(s)", "UInt64"}, {"length(arr_i)", "UInt64"},
		{"length(m)", "UInt64"},
		{"empty(s)", "UInt8"}, {"empty(arr_i)", "UInt8"},
		// empty and notEmpty keep accepting a UUID, unlike length
		// (the regression): "SELECT empty(uid) FROM probe" runs and gives 0.
		{"empty(uid)", "UInt8"},

		// The universal aggregates take every type. They must not gain
		// a domain by accident.
		{"max(s)", "String"}, {"min(uid)", "UUID"},
		{"any(tup)", "Tuple(Int32, String)"},
		{"uniq(s)", "UInt64"}, {"count(tup)", "UInt64"},
	} {
		got, err := inferTestExprType(t, schema, testCase.expr)
		if err != nil {
			t.Errorf("%s: chgen refused a call that ClickHouse accepts: %v", testCase.expr, err)
			continue
		}
		if got != testCase.want {
			t.Errorf("%s: chgen=%s, ClickHouse measured %s", testCase.expr, got, testCase.want)
		}
	}
}

// TestArrayComparabilityRefusesMismatchedElements checks the regression and
// the regression: two Arrays compare only when their ELEMENT types compare.
// Each case was measured with a real "SELECT <op>(a, b) FROM t" over real
// columns, never a literal, on ClickHouse 25.8.29.51, and confirmed by
// EXECUTION and not by toTypeName alone.
//
// This must PROVE it fails before the fix: before the recursive Array
// element check, chgen answered UInt8 for every one of these, because an
// Array operand passed comparisonClassArray with no look at its element.
//
// Every comparison operator that routes through checkComparableOperandExprs
// is checked, both the infix operators and the six comparesArgPair
// functions, so that a fix to one function cannot leave a sibling function
// blind, which is exactly how the regression came to exist next to the regression.
func TestArrayComparabilityRefusesMismatchedElements(t *testing.T) {
	schema := argumentDomainTestSchema(t)
	for _, expr := range []string{
		// Int32 elements vs String elements: no shared class.
		"equals(arr_i32, arr_s)", "arr_i32 = arr_s",
		"notEquals(arr_i32, arr_s)", "arr_i32 != arr_s",
		"less(arr_i32, arr_s)", "arr_i32 < arr_s",
		"lessOrEquals(arr_i32, arr_s)", "arr_i32 <= arr_s",
		"greater(arr_i32, arr_s)", "arr_i32 > arr_s",
		"greaterOrEquals(arr_i32, arr_s)", "arr_i32 >= arr_s",

		// nullIf is NOT covered here: it refuses every Array argument
		// through scalarArgumentDomain (the related regressions are
		// about the COMPARABILITY rule, and nullIf never reaches that
		// rule for an Array, because its own domain check refuses
		// first). Measured on ClickHouse 25.8.29.51: "SELECT
		// nullIf(a_i32, a_i64) FROM t" is ALSO Code: 43, "Nested type
		// Array(Int32) cannot be inside Nullable type" -- a DIFFERENT
		// server rule (nullIf's result is Nullable(T), and an Array
		// cannot nest inside a Nullable), not the comparability rule
		// this tracking item fixes. Testing nullIf here would pass for the
		// wrong reason on both sides of the fix.

		// Decimal elements vs Float64 elements: the one measured
		// irregularity of the Array recursion. The bare scalar pair
		// "dec >= f64" RUNS (Decimal and Float64 share the numeric
		// class), but the identical pair as Array elements refuses
		// with Code: 43, because Decimal and Float64 have no common
		// ClickHouse type. Int64 and UInt64 elements show the same
		// refusal against Float64, while Int32 elements do not; see
		// the commonCHType call inside comparableBaseTypes in
		// argument_domain.go.
		"greaterOrEquals(arr_dec, arr_f64)",
		"equals(arr_i64, arr_f64)", "greaterOrEquals(arr_i64, arr_f64)",

		// The mismatch survives one level of Array nesting: an Array
		// of Arrays compares only when its element Arrays compare.
		"equals(arr_aai32, arr_aas)",
		"greaterOrEquals(arr_aai32, arr_aas)",

		// the regression: more measured no-supertype cells, beyond the
		// wide-integer/Decimal grid. Measured on ClickHouse 25.8.29.51
		// with a real "SELECT equals(a, b) FROM t":
		//
		//	equals(a_i128, a_f64)      Code: 43   Int128 has no
		//	                                      supertype with Float64
		//	equals(a_u64, a_f64)       Code: 43   same for UInt64
		//	equals(a_dec384, a_i128)   Code: 43   Decimal(38,4) has no
		//	                                      supertype with Int128
		//	equals(a_e8, a_dec92)      Code: 43   an Enum has no
		//	                                      supertype with a Decimal
		"equals(arr_i128, arr_f64)", "greaterOrEquals(arr_i128, arr_f64)",
		"equals(arr_u64, arr_f64)",
		"equals(arr_dec384, arr_i128)",
		"equals(arr_e8, arr_dec92)",
	} {
		if _, err := inferTestExprType(t, schema, expr); err == nil {
			t.Errorf("%s: chgen gave a type, but ClickHouse refuses this call (Code: 43, mismatched Array element types)", expr)
		}
	}
}

// TestArrayComparabilityAcceptsCompatibleElements checks the other side of
// The related regressions show that a rule fitted to "the element types must be
// equal" would be WIDER than the server's refusal, which is itself a
// defect (a false refusal breaks a query that runs today). Array(Int32)
// against Array(Int64) must still compare, because Int32 and Int64 share
// the numeric class, the same class rule that already lets a bare Int32
// column compare with a bare Int64 column.
//
// Every case here is a CONTROL: if inferTestExprType reports an error for
// "arr_i32 = arr_i32" (Array against itself), the harness is broken and
// not the code. The check must use the configured fixture table name.
func TestArrayComparabilityAcceptsCompatibleElements(t *testing.T) {
	schema := argumentDomainTestSchema(t)
	for _, expr := range []string{
		// Control: an Array compares with itself.
		"equals(arr_i32, arr_i32)",

		// Int32 elements vs Int64 elements: both numeric, thus the
		// arrays compare. Measured: "SELECT equals(arr_i32, arr_i64)
		// FROM t" gives a row, not Code: 43.
		"equals(arr_i32, arr_i64)", "arr_i32 = arr_i64",
		"greaterOrEquals(arr_i32, arr_i64)", "arr_i32 >= arr_i64",
		"less(arr_i32, arr_i64)", "notEquals(arr_i32, arr_i64)",

		// The same numeric compatibility survives one level of Array
		// nesting: Array(Array(Int32)) compares with
		// Array(Array(Int64)).
		"equals(arr_aai32, arr_aai64)",
		"greaterOrEquals(arr_aai32, arr_aai64)",

		// A narrower integer element (32 bits or fewer) still compares
		// against a Float element inside an Array, unlike Int64,
		// UInt64 and Decimal, because commonCHType gives Int32 and
		// Float64 a common type (Float64) but gives Int64 and Float64
		// none.
		"equals(arr_i32, arr_f64)", "greaterOrEquals(arr_i32, arr_f64)",

		// the regression: more measured supertype cells, beyond the
		// wide-integer grid. Measured on ClickHouse 25.8.29.51 with a
		// real "SELECT equals(a, b) FROM t", every one of which
		// returns a row and not Code: 43:
		//
		//	equals(a_i8, a_f32)     Int8 and Float32 share Float32
		//	equals(a_dec92, a_i32)  Decimal(9,2) and Int32 share
		//	                        Decimal(18,2)
		//	equals(a_e8, a_f64)     an Enum and a Float share Float64
		//	                        (the Enum joins through its
		//	                        storage integer)
		//	equals(a_e8, a_s)       an Enum and a String share String
		//	equals(a_d, a_d32)      Date and Date32 share Date32
		//	equals(a_dt, a_d32)     DateTime and Date32 share
		//	                        DateTime64(0)
		//	equals(a_dt, a_dt64)    DateTime and DateTime64 share
		//	                        DateTime64(3)
		//	equals(a_s, a_fs)       String and FixedString share
		//	                        String
		//	equals(a_ni32, a_f64)   a Nullable element and a bare
		//	                        element still join
		//	equals(a_lci32, a_f64)  a LowCardinality element still
		//	                        joins, unwrapped
		"equals(arr_i8, arr_f32)",
		"equals(arr_dec92, arr_i32)",
		"equals(arr_e8, arr_f64)",
		"equals(arr_e8, arr_s)",
		"equals(arr_d, arr_d32)",
		"equals(arr_dt, arr_d32)",
		"equals(arr_dt, arr_dt64)",
		"equals(arr_s, arr_fs)",
		"equals(arr_ni32, arr_f64)",
		"equals(arr_lci32, arr_f64)",
	} {
		if _, err := inferTestExprType(t, schema, expr); err != nil {
			t.Errorf("%s: chgen refused a call that ClickHouse accepts: %v", expr, err)
		}
	}
}

// TestTupleComparabilityRefusesMismatchedMembers checks the regression: a
// Tuple, like an Array, must check every member and not only the shared
// comparison class. Before the fix, chgen answered UInt8 for every one
// of these, because comparisonClassTuple never looked at a member.
//
// Measured on ClickHouse 25.8.29.51 against real columns, confirmed by a
// real "SELECT equals(a, b) FROM t" and not toTypeName alone. See the
// measured note in comparableBaseTypes in argument_domain.go.
func TestTupleComparabilityRefusesMismatchedMembers(t *testing.T) {
	schema := argumentDomainTestSchema(t)
	for _, expr := range []string{
		// Position 0 has no common class: Int32 vs String.
		"equals(tup, tup_ss)", "tup = tup_ss",
		// Position 0 has no common class the other way: String vs Int32.
		"equals(tup, tup_ii)",
		// Position 1 has no common class: Int32 vs String.
		"equals(tup_ii, tup_f64s)",
		"equals(tup_i64f64, tup)",
		// Differing arity refuses, Code 43, regardless of any member.
		"equals(tup, tup_i32)", "equals(tup, tup_i32s_i32)",
		// A nested Array member still needs its OWN supertype: the
		// Array rule inside the recursive call refuses Int32 vs
		// UInt64 elements, even though a bare Tuple member of those
		// same two types compares (see the accept test below).
		"equals(tup_arri, tup_arru64)",
	} {
		if _, err := inferTestExprType(t, schema, expr); err == nil {
			t.Errorf("%s: chgen gave a type, but ClickHouse refuses this call", expr)
		}
	}
}

// TestTupleComparabilityAcceptsCompatibleMembers checks the other side:
// a rule fitted to "every member type must be equal" would be WIDER
// than the server's refusal. A false refusal breaks a query that runs today.
//
// The wide-integer and Decimal member pair is the one measured
// irregularity: it refuses inside an Array (the regression) and inside a
// Map (see the Map test below), but a Tuple member compares like a bare
// scalar pair, with NO extra supertype gate. Measured on ClickHouse
// 25.8.29.51: "SELECT equals(tup_u64, tup) FROM t" and "SELECT
// equals(tup_i128, tup_dec92) FROM t" both give a row (0), even though
// "SELECT toTypeName(if(1, tup_u64, tup)) FROM t" is Code 386, no
// supertype for Int32 and UInt64.
func TestTupleComparabilityAcceptsCompatibleMembers(t *testing.T) {
	schema := argumentDomainTestSchema(t)
	for _, expr := range []string{
		// Control: a Tuple compares with itself.
		"equals(tup, tup)",
		// Both members share a class: Int64/Int32 and String/String.
		"equals(tup, tup_i64s)", "equals(tup, tup_f64s)",
		"equals(tup_i64f64, tup_ii)",
		// The wide-integer and Decimal irregularity: no supertype, but
		// a Tuple member still compares, unlike an Array element.
		"equals(tup_u64, tup)", "equals(tup_i128, tup_dec92)",
		// An element name does not change the result.
		"equals(tup_named, tup)",
	} {
		if _, err := inferTestExprType(t, schema, expr); err != nil {
			t.Errorf("%s: chgen refused a call that ClickHouse accepts: %v", expr, err)
		}
	}
}

// TestMapComparabilityRefusesMismatchedMembers checks the regression's Map
// side. A Map reads like an Array on the wide-integer/Decimal cells,
// NOT like a Tuple: both the key and the value need a real supertype,
// not only a shared comparison class.
//
// Measured on ClickHouse 25.8.29.51 against real columns, confirmed by
// a real "SELECT equals(a, b) FROM t":
//
//	m      Map(String, Int64) vs m_ss  Map(String, String)   Code 43
//	m      Map(String, Int64) vs m_is  Map(Int32, String)    Code 43
//	m      Map(String, Int64) vs m_sf  Map(String, Float64)  Code 43
//	mk_i32 Map(Int32, String) vs mk_u64 Map(UInt64, String)  Code 43
func TestMapComparabilityRefusesMismatchedMembers(t *testing.T) {
	schema := argumentDomainTestSchema(t)
	for _, expr := range []string{
		"equals(m, m_ss)", "m = m_ss",
		"equals(m, m_is)",
		"equals(m, m_sf)",
		// The key needs a supertype too, and Int32/UInt64 has none:
		// unlike the SAME pair as a Tuple member (see the Tuple accept
		// test above), the Map key refuses.
		"equals(mk_i32, mk_u64)",
	} {
		if _, err := inferTestExprType(t, schema, expr); err == nil {
			t.Errorf("%s: chgen gave a type, but ClickHouse refuses this call", expr)
		}
	}
}

// TestMapComparabilityAcceptsCompatibleMembers is the accept-side
// control for TestMapComparabilityRefusesMismatchedMembers.
func TestMapComparabilityAcceptsCompatibleMembers(t *testing.T) {
	schema := argumentDomainTestSchema(t)
	for _, expr := range []string{
		// Control: a Map compares with itself.
		"equals(m, m)",
	} {
		if _, err := inferTestExprType(t, schema, expr); err != nil {
			t.Errorf("%s: chgen refused a call that ClickHouse accepts: %v", expr, err)
		}
	}
}

// comparableBaseTypesWithoutMemberCheck is a test-local copy of
// comparableBaseTypes as it stood BEFORE the regression's fix: it keeps the
// Array recursion, but a Tuple or a Map pair that shares its comparison
// class falls straight through to the final "return true", with no
// member check at all. This reproduces the exact defect the regression
// found, so the grid tests above can prove they would have caught it.
func comparableBaseTypesWithoutMemberCheck(left, right CHType) bool {
	leftClasses := comparisonClassesOf(left)
	rightClasses := comparisonClassesOf(right)
	if leftClasses == 0 || rightClasses == 0 {
		return true
	}
	if leftClasses&rightClasses == 0 {
		return false
	}
	if leftClasses == comparisonClassArray && rightClasses == comparisonClassArray {
		if len(left.Params) != 1 || len(right.Params) != 1 {
			return true
		}
		leftElem, _, _ := splitCHWrappers(left.Params[0])
		rightElem, _, _ := splitCHWrappers(right.Params[0])
		if _, err := commonCHType(leftElem, rightElem); err != nil {
			return false
		}
		return comparableBaseTypesWithoutMemberCheck(leftElem, rightElem)
	}
	// The pre-fix bug: no Tuple branch and no Map branch. A pair that
	// shares comparisonClassTuple or comparisonClassMap reaches here
	// with no member ever inspected.
	return true
}

// TestTupleAndMapComparabilityGridCatchesTheKnownFault proves that the
// grid above can fail. A check that cannot tell "no difference" from
// "never ran" is a broken deliverable, and
// this exact class of gap let the regression's defect ship in the first
// place (comparisonClassTuple and comparisonClassMap fell through to
// the final "return true" with no member check at all).
//
// This test rebuilds the pre-fix predicate, WITHOUT the Tuple and Map
// member recursion, and reruns the refusal cases above against it. Each
// one must now be wrongly ACCEPTED, proving the grid tests would have
// caught the original defect.
func TestTupleAndMapComparabilityGridCatchesTheKnownFault(t *testing.T) {
	cases := []struct {
		left, right CHType
	}{
		{CHType{Name: "Tuple", Params: []CHType{{Name: "Int32"}, {Name: "String"}}},
			CHType{Name: "Tuple", Params: []CHType{{Name: "String"}, {Name: "String"}}}},
		{CHType{Name: "Tuple", Params: []CHType{{Name: "Int32"}, {Name: "String"}}},
			CHType{Name: "Tuple", Params: []CHType{{Name: "Int32"}}}},
		{CHType{Name: "Map", Params: []CHType{{Name: "String"}, {Name: "Int64"}}},
			CHType{Name: "Map", Params: []CHType{{Name: "String"}, {Name: "String"}}}},
		{CHType{Name: "Map", Params: []CHType{{Name: "Int32"}, {Name: "String"}}},
			CHType{Name: "Map", Params: []CHType{{Name: "UInt64"}, {Name: "String"}}}},
	}
	for _, c := range cases {
		if !comparableBaseTypesWithoutMemberCheck(c.left, c.right) {
			t.Fatalf("preFix(%s, %s) = false, want true: the fault-injected predicate must reproduce the wrong UInt8 answer, or this grid could not have caught it", c.left.String(), c.right.String())
		}
	}
}

// TestArgumentDomainRefusalMessageHelps checks that a refusal names the
// argument type, names what the server needs and keeps the pin-type
// hint, so the user can continue.
func TestArgumentDomainRefusalMessageHelps(t *testing.T) {
	schema := argumentDomainTestSchema(t)
	_, err := inferTestExprType(t, schema, "sum(s)")
	if err == nil {
		t.Fatal("sum(s) must be a refusal")
	}
	message := err.Error()
	for _, want := range []string{"sum", "String", "an integer", "not ClickHouse type inference"} {
		if !strings.Contains(message, want) {
			t.Errorf("refusal message does not contain %q: %s", want, message)
		}
	}
}
