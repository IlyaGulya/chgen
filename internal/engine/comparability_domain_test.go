package engine

import (
	"strings"
	"testing"
)

// The tests below cover the comparability domain: the measured rule that
// says which pairs of operand base types ClickHouse can compare.
//
// Every case was measured on ClickHouse 25.8.29.51 with a REAL COLUMN of
// the oracle fixture and a real "SELECT a <= b FROM t", never over a
// literal, because the server folds constants and a rule measured over a
// literal reports the wrong boundary.
//
// The refused pairs reach chgen through the type oracle as the class
// CH_ERROR_43_CHGEN_TYPED: the server refuses and chgen answers a type.
// A comparison result is always Bool or UInt8, thus the shape of the
// result cannot report the defect; only the operand pair can.

// TestComparisonRefusesIncomparableOperands checks that a comparison of
// two operand types from different comparison classes is a refusal.
//
// The three entries marked "oracle" are the shapes that the type oracle
// found blind before this rule existed.
func TestComparisonRefusesIncomparableOperands(t *testing.T) {
	schema := argumentDomainTestSchema(t)
	for _, expr := range []string{
		// oracle, seed 99, kind cmp:<= :
		// "No operation lessOrEquals between Decimal(38, 2) and String"
		"dec <= s",
		"s <= dec",
		"d32 < lc",
		"d64 > ns",
		// A number against a String is Code: 386, "There is no
		// supertype for types String, Int8".
		"i8 = s",
		"s != i64",
		"f64 >= lc",
		"u64 <= ns",
		"i128 = fs",
		"b > s",
		// A temporal type against a String.
		"d = s",
		"dt < ns",
		"dt64 >= lc",
		"d32t != fs",
		// A UUID compares with a UUID only.
		"uid = i8",
		"uid <= s",
		"uid > d",
		"i64 = uid",
		// An IP address compares with an IP address only.
		"ip4 = s",
		"ip6 <= i8",
		"ip6 != d",
		"s = ip4",
		// The composite types compare inside their own family only.
		"arr_i = i8",
		"arr_i <= s",
		"tup = i8",
		"tup > s",
		"m = i8",
		"m != s",
		"arr_i = tup",
		"arr_i = m",
		"tup <= m",
		// A composite type against a UUID or an IP address.
		"arr_i = uid",
		"tup != ip4",
		// A temporal type against a UUID or a composite type.
		"d = uid",
		"dt <= arr_i",
	} {
		if inferred, err := inferTestExprType(t, schema, expr); err == nil {
			t.Errorf("%s: want a refusal, got type %s", expr, inferred)
		} else if !strings.Contains(err.Error(), "cannot compare") {
			t.Errorf("%s: want a comparability refusal, got %v", expr, err)
		}
	}
}

// TestNullIfRefusesIncomparableArguments checks the same rule for
// nullIf. nullIf(a, b) is "a = b ? NULL : a", thus it compares its two
// arguments, and its result type comes from the FIRST argument alone.
// Without this rule chgen answers the type of the first argument for a
// call that the server refuses.
//
// The two entries marked "oracle" are the shapes that the type oracle
// found blind before this rule existed.
func TestNullIfRefusesIncomparableArguments(t *testing.T) {
	schema := argumentDomainTestSchema(t)
	for _, expr := range []string{
		// oracle, seed 1234, inside argMax:
		// "No operation equals between Decimal(18, 4) and String"
		"nullIf(dec, s)",
		// oracle, seed 1234, inside sumSimpleState:
		// "No operation equals between Decimal(76, 4) and String"
		"nullIf(d64, lc)",
		"nullIf(s, dec)",
		"nullIf(i8, s)",
		"nullIf(ns, i64)",
		"nullIf(f64, fs)",
		"nullIf(d, s)",
		"nullIf(dt64, ns)",
		"nullIf(uid, i8)",
		"nullIf(s, uid)",
		"nullIf(ip6, i8)",
		"nullIf(ip4, s)",
	} {
		if inferred, err := inferTestExprType(t, schema, expr); err == nil {
			t.Errorf("%s: want a refusal, got type %s", expr, inferred)
		} else if !strings.Contains(err.Error(), "cannot compare") {
			t.Errorf("%s: want a comparability refusal, got %v", expr, err)
		}
	}
}

// TestComparisonKeepsComparableOperands is the guard against a rule that
// became too wide. A false refusal breaks a query that works today, thus
// every pair below was measured as ACCEPTED on the same server and must
// keep its type.
func TestComparisonKeepsComparableOperands(t *testing.T) {
	schema := argumentDomainTestSchema(t)
	for _, testCase := range []struct {
		expr string
		want string
	}{
		// The numeric family compares inside itself.
		{"i8 = i64", "UInt8"},
		{"i8 <= f64", "UInt8"},
		{"u64 > i128", "UInt8"},
		{"i8 < dec", "UInt8"},
		{"f64 >= dec", "UInt8"},
		{"dec != d32", "UInt8"},
		{"b = i8", "UInt8"},
		{"u256 > i256", "UInt8"},
		// An Enum compares with a number AND with a String, thus it is
		// in two classes ("e8 <= i8" is 1 and "e8 <= s" is 1).
		{"e8 = i8", "UInt8"},
		{"e8 <= s", "UInt8"},
		{"e16 > lc", "UInt8"},
		{"e8 != e16", "UInt8"},
		{"s = e8", "UInt8"},
		// The string family compares inside itself.
		{"s = fs", "UInt8"},
		{"s <= lc", "UInt8"},
		{"fs > lc", "UInt8"},
		// The temporal family compares inside itself.
		{"d = dt", "UInt8"},
		{"dt <= dt64", "UInt8"},
		{"d < d32t", "UInt8"},
		{"d32t >= dt64", "UInt8"},
		// A UUID with a UUID, and an IP address with an IP address.
		{"uid = uid", "UInt8"},
		{"ip4 <= ip6", "UInt8"},
		{"ip6 = ip6", "UInt8"},
		// The composite types compare inside their own family.
		{"arr_i = arr_i", "UInt8"},
		{"tup != tup", "UInt8"},
		{"m = m", "UInt8"},
		// A Nullable operand makes the result Nullable, and the base
		// type under the wrapper decides the comparability.
		{"ni32 = i8", "Nullable(UInt8)"},
		{"ns <= s", "Nullable(UInt8)"},
		{"ni32 < f64", "Nullable(UInt8)"},
		{"lcn = s", "Nullable(UInt8)"},
	} {
		inferred, err := inferTestExprType(t, schema, testCase.expr)
		if err != nil {
			t.Errorf("%s: want type %s, got refusal %v", testCase.expr, testCase.want, err)
			continue
		}
		if inferred != testCase.want {
			t.Errorf("%s: want type %s, got %s", testCase.expr, testCase.want, inferred)
		}
	}
}

// TestComparisonFunctionSpellingRefusesIncomparableOperands checks the
// FUNCTION spelling of the six comparison operators: equals, notEquals,
// less, lessOrEquals, greater and greaterOrEquals. Each is the same
// comparison as its operator, and the registry marks all six with
// comparesArgPair so that the shared check runs for the function
// spelling too.
//
// Before this rule, these six names took only the argsIndependent
// fixed-UInt8 path and never reached checkComparableOperandExprs, thus
// chgen answered UInt8 for a call that the server refuses. Measured on
// ClickHouse 25.8.29.51 with real columns and a value select:
//
//	equals(dec, s)             Code: 43   No operation equals
//	greater(uid, ip4)          Code: 43   Illegal types of arguments
//	                                      (UUID, IPv4) of function
//	                                      greater
func TestComparisonFunctionSpellingRefusesIncomparableOperands(t *testing.T) {
	schema := argumentDomainTestSchema(t)
	for _, expr := range []string{
		"equals(dec, s)",
		"notEquals(dec, s)",
		"less(dec, s)",
		"lessOrEquals(dec, s)",
		"greater(uid, ip4)",
		"greaterOrEquals(uid, ip4)",
		// The pair refusal reaches every base type family that the
		// operator sweep already covers, not only Decimal/String and
		// UUID/IPv4.
		"equals(i8, s)",
		"less(d, s)",
		"greater(arr_i, i8)",
	} {
		if inferred, err := inferTestExprType(t, schema, expr); err == nil {
			t.Errorf("%s: want a refusal, got type %s", expr, inferred)
		} else if !strings.Contains(err.Error(), "cannot compare") {
			t.Errorf("%s: want a comparability refusal, got %v", expr, err)
		}
	}
}

// TestComparisonFunctionSpellingKeepsComparableOperands guards the other
// side: the function spelling must still accept a pair that its operator
// accepts, and a constant operand must still fold as it does for the
// operator, so that this rule does not become a false refusal.
func TestComparisonFunctionSpellingKeepsComparableOperands(t *testing.T) {
	schema := argumentDomainTestSchema(t)
	for _, testCase := range []struct {
		expr string
		want string
	}{
		{"equals(i8, i64)", "UInt8"},
		{"less(uid, uid)", "UInt8"},
		{"greater(arr_i, arr_i)", "UInt8"},
		// A constant operand folds into the type of the other side
		// and keeps the pair legal, exactly as "dec <= '1.5'" does.
		{"equals(dec, '1.5')", "UInt8"},
		{"less(i8, '1')", "UInt8"},
		// A Nullable operand makes the result Nullable, exactly as
		// the operator spelling does.
		{"equals(ni32, i8)", "Nullable(UInt8)"},
	} {
		inferred, err := inferTestExprType(t, schema, testCase.expr)
		if err != nil {
			t.Errorf("%s: want type %s, got refusal %v", testCase.expr, testCase.want, err)
			continue
		}
		if inferred != testCase.want {
			t.Errorf("%s: want type %s, got %s", testCase.expr, testCase.want, inferred)
		}
	}
}

// TestNullIfKeepsComparableArguments is the same guard for nullIf.
func TestNullIfKeepsComparableArguments(t *testing.T) {
	schema := argumentDomainTestSchema(t)
	for _, testCase := range []struct {
		expr string
		want string
	}{
		{"nullIf(i8, i64)", "Nullable(Int8)"},
		{"nullIf(i8, f64)", "Nullable(Int8)"},
		{"nullIf(dec, i8)", "Nullable(Decimal(18, 4))"},
		{"nullIf(s, fs)", "Nullable(String)"},
		{"nullIf(e8, s)", "Nullable(Enum8('a' = 1, 'zz' = 2))"},
		{"nullIf(d, dt)", "Nullable(Date)"},
		{"nullIf(dt, dt64)", "Nullable(DateTime)"},
		{"nullIf(uid, uid)", "Nullable(UUID)"},
		{"nullIf(ip4, ip6)", "Nullable(IPv4)"},
		{"nullIf(ni32, i8)", "Nullable(Int32)"},
		{"nullIf(ns, s)", "Nullable(String)"},
	} {
		inferred, err := inferTestExprType(t, schema, testCase.expr)
		if err != nil {
			t.Errorf("%s: want type %s, got refusal %v", testCase.expr, testCase.want, err)
			continue
		}
		if inferred != testCase.want {
			t.Errorf("%s: want type %s, got %s", testCase.expr, testCase.want, inferred)
		}
	}
}

// TestComparabilityAcceptsAConstantOperand is the guard against the
// constant-folding trap. ClickHouse parses a CONSTANT of one type into
// the type of the other operand, thus a constant is comparable with
// everything and a rule that reads only the base types over-refuses.
//
// Every case below was measured with the SECOND witness, that is a real
// "SELECT e FROM t" and never toTypeName alone. The first entry is the
// false refusal that the type oracle found after the first form of this
// rule: the server answers 1.2345 for it.
func TestComparabilityAcceptsAConstantOperand(t *testing.T) {
	schema := argumentDomainTestSchema(t)
	for _, expr := range []string{
		// The measured false refusal, from the oracle on seed 1234.
		"nullIf(-(dec), toString(-129))",
		// A constant String against a Decimal or a number.
		"dec <= '1.5'",
		"'1.5' >= dec",
		"i8 = '1'",
		"i8 <= toString(1)",
		"nullIf(dec, '1.5')",
		"nullIf(i8, '1')",
		// A constant String against a UUID, an IP address or a date.
		"uid = '61f0c404-5cb3-11e7-907b-a6006ad3dba0'",
		"ip4 = '1.2.3.4'",
		"d = '2024-01-02'",
		"dt64 <= '2024-01-02 03:04:05'",
		"nullIf(uid, '61f0c404-5cb3-11e7-907b-a6006ad3dba0')",
	} {
		if _, err := inferTestExprType(t, schema, expr); err != nil {
			t.Errorf("%s: want a type, got refusal %v", expr, err)
		}
	}
}

// TestComparabilityLeavesTheNonComparingOperators checks that the rule
// did not spread to the operators that do NOT compare their two operands
// against each other. AND and OR read each operand as a condition, IN
// reads the right operand as a set, and the LIKE family reads both
// operands as text. A comparability rule on those would be a false
// refusal.
func TestComparabilityLeavesTheNonComparingOperators(t *testing.T) {
	schema := argumentDomainTestSchema(t)
	for _, expr := range []string{
		// A String pattern against a String column stays legal, and the
		// LIKE family never compares the pair as values.
		"s LIKE '%a%'",
		"lc NOT LIKE '%a%'",
		"s ILIKE '%a%'",
		// IN reads its right operand as a set.
		"i8 IN (1, 2)",
		"s IN ('a', 'b')",
		// AND and OR read each operand as a condition.
		"(i8 = i8) AND (s = s)",
		"(d = d) OR (uid = uid)",
	} {
		if _, err := inferTestExprType(t, schema, expr); err != nil {
			t.Errorf("%s: want a type, got refusal %v", expr, err)
		}
	}
}

// TestComparabilityAcceptsAnUncoveredType checks the deliberate gap. A
// base type that the sweep did not cover keeps every pair accepted,
// because an unknown is not evidence of a refusal and the governing rule
// refuses only what a measurement refused.
//
// A bare positional placeholder has no result type by construction, thus
// "i8 = ?" must keep its type. That is "not known yet" and never
// "impossible".
//
// nullIf with a placeholder is NOT in this list. It was already a
// refusal before the comparability rule existed, with the different
// message "positional placeholder has no result type", because
// firstFunctionArgument runs before any domain check. The comparability
// rule must not change that message, and the test below proves it.
func TestComparabilityAcceptsAnUncoveredType(t *testing.T) {
	schema := argumentDomainTestSchema(t)
	for _, expr := range []string{
		"i8 = ?",
		"s <= ?",
		"uid = ?",
		"arr_i = ?",
	} {
		if _, err := inferTestExprType(t, schema, expr); err != nil {
			t.Errorf("%s: want a type, got refusal %v", expr, err)
		}
	}
	// The placeholder refusal of nullIf keeps its own cause. A
	// comparability message here would name the wrong reason and send
	// the user to the wrong fix.
	_, err := inferTestExprType(t, schema, "nullIf(s, ?)")
	if err == nil {
		t.Fatalf("nullIf(s, ?): want the placeholder refusal, got a type")
	}
	if !strings.Contains(err.Error(), "positional placeholder") {
		t.Errorf("nullIf(s, ?): want the placeholder refusal, got %v", err)
	}
}

// A date-only type and a DateTime do NOT share the rule against a
// number. Measured on ClickHouse 25.8.29.51 with real columns:
//
//	dt <= i8    0          dt64 <= i8   0
//	dt <= f64   0          dt64 <= f64  0
//	d  <= i8    Code: 43   d32t <= i8   Code: 43
//	d  <= f64   Code: 43   d32t <= f64  Code: 43
//	d  <= dec   Code: 43   d32t <= dec  Code: 43
//
// In this probe schema d32t is the Date32 column, because d32 is
// already the Decimal32 column.
//
// One temporal class for all four types made chgen refuse "dt <= i8",
// which the server accepts. That was a false refusal, and a false
// refusal breaks a query that works.
func TestDateTimeComparesWithANumberAndADateDoesNot(t *testing.T) {
	schema := argumentDomainTestSchema(t)
	accepted := []string{
		"dt <= i8", "dt <= f64", "dt64 <= i8", "dt64 <= f64",
		// DateTime64 compares with a Decimal. A plain DateTime does
		// not. The pair rule must keep this split.
		"dt64 <= dec", "dec >= dt64",
	}
	for _, expression := range accepted {
		if _, err := inferTestExprType(t, schema, expression); err != nil {
			t.Errorf("%s must keep its type: the server accepts it, thus a refusal breaks a working query; got %v",
				expression, err)
		}
	}
	refused := []string{
		"d <= i8", "d <= f64", "d <= dec",
		"d32t <= i8", "d32t <= f64", "d32t <= dec",
		"dt <= dec", "dec >= dt",
	}
	for _, expression := range refused {
		if _, err := inferTestExprType(t, schema, expression); err == nil {
			t.Errorf("%s must stay refused: the server answers Code: 43", expression)
		}
	}
	// The temporal family still compares inside itself.
	for _, expression := range []string{"d <= dt", "d <= d32t", "dt <= dt64"} {
		if _, err := inferTestExprType(t, schema, expression); err != nil {
			t.Errorf("%s must keep its type: the temporal family compares inside itself; got %v",
				expression, err)
		}
	}
}

// TestComparisonFunctionsUseTheMeasuredPairBoundary checks the cells
// that the regression found in the function spelling. Each call below was
// measured with a real value SELECT on ClickHouse 25.8.29.51.
func TestComparisonFunctionsUseTheMeasuredPairBoundary(t *testing.T) {
	const ddl = `CREATE TABLE probe (
		dec Decimal(18, 4),
		dt DateTime,
		dt64 DateTime64(3),
		dyn Dynamic,
		i32 Int32,
		tup Tuple(Int32, String),
		m_tup Map(String, Tuple(Int32, String))
	) ENGINE = Memory`
	schema, err := schemaFromDDLErr(t, ddl)
	if err != nil {
		t.Fatalf("parse probe schema: %v", err)
	}

	for _, expression := range []string{
		"equals(dec, dt)", "notEquals(dt, dec)",
		"less(dec, dt)", "lessOrEquals(dt, dec)",
		"greater(dec, dt)", "greaterOrEquals(dt, dec)",
		"equals(dyn, tup)", "greaterOrEquals(dyn, m_tup)",
	} {
		if inferred, inferErr := inferTestExprType(t, schema, expression); inferErr == nil {
			t.Errorf("%s: want a refusal, got %s", expression, inferred)
		}
	}

	for _, expression := range []string{
		"equals(dec, dt64)", "lessOrEquals(dt64, dec)",
		"equals(dyn, dec)", "less(dyn, i32)",
	} {
		if _, inferErr := inferTestExprType(t, schema, expression); inferErr != nil {
			t.Errorf("%s: want a type, got refusal %v", expression, inferErr)
		}
	}
}

// TestAggregateFunctionStateNeverCompares pins the measured rule that an
// AggregateFunction state is incomparable with EVERY type, itself
// included, while a SimpleAggregateFunction compares as its inner value.
//
// Measured on ClickHouse 25.8.29.51 over real columns of a Memory table,
// never over a literal:
//
//	agg = agg2 -> Code: 43     agg = i64  -> Code: 43
//	agg = s    -> Code: 43     agg > agg2 -> Code: 43
//	sagg = i64 -> ACCEPTED     sagg = s   -> Code: 386 (no supertype)
//
// Before this rule the pair reached the "not covered by the sweep"
// branch and kept the accept, thus chgen answered UInt8 where the server
// refuses. The type oracle reported it as the blindness signatures
// v3-fn-groupBitOr and v3-window-leadInFrame, whose inner expressions
// are less(agg, i64) and greater(e8, agg).
func TestAggregateFunctionStateNeverCompares(t *testing.T) {
	const ddl = `CREATE TABLE probe (
		i64 Int64,
		s String,
		agg AggregateFunction(uniq, UInt64),
		agg2 AggregateFunction(uniq, UInt64),
		sagg SimpleAggregateFunction(sum, Int64)
	) ENGINE = Memory`
	schema, err := schemaFromDDLErr(t, ddl)
	if err != nil {
		t.Fatalf("parse probe schema: %v", err)
	}

	// An AggregateFunction state refuses every partner, including a
	// second state of the same aggregate.
	for _, expression := range []string{
		"agg = agg2", "agg > agg2", "agg = i64", "agg = s",
		"i64 = agg", "s = agg",
		"less(agg, i64)", "greater(agg, i64)", "nullIf(agg, agg2)",
	} {
		if _, err := inferTestExprType(t, schema, expression); err == nil {
			t.Errorf("%s must be refused: the server answers Code: 43, thus a type here is a "+
				"silently wrong answer", expression)
		}
	}

	// A SimpleAggregateFunction is a value and keeps comparing. This is
	// the half that a rule written on the substring "AggregateFunction"
	// alone would break.
	for _, expression := range []string{"sagg = i64", "sagg > i64", "less(sagg, i64)"} {
		if _, err := inferTestExprType(t, schema, expression); err != nil {
			t.Errorf("%s must keep its type: the server accepts it, thus a refusal breaks a "+
				"working query; got %v", expression, err)
		}
	}
}
