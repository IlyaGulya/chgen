package engine

import (
	"testing"

	clickhouse "github.com/AfterShip/clickhouse-sql-parser/parser"
)

// These tests pin the ClickHouse result types for two rule families:
//
//  1. A bare integer literal takes the smallest type that holds its value.
//  2. The Nullable and LowCardinality wrappers of the arguments move
//     through functions and operators by an explicit per-function class.
//
// Each expected type was measured on ClickHouse 25.8.29.51 (image
// clickhouse/clickhouse-server:25.8, disposable container) with
// DESCRIBE (SELECT <expression> FROM t).
func wrapperTestSchema(t *testing.T) *Schema {
	t.Helper()
	schema, err := schemaFromDDLErr(t, `CREATE TABLE t (
    i8   Int8, i16 Int16, i32 Int32, i64 Int64,
    u8   UInt8, u16 UInt16, u32 UInt32, u64 UInt64,
    f32  Float32, f64 Float64, dec Decimal(18, 4), b Bool,
    s    String, fs FixedString(8),
    d    Date, dt DateTime, dt64 DateTime64(3),
    ni32 Nullable(Int32), nf64 Nullable(Float64), ns Nullable(String),
    nb   Nullable(Bool),
    arr_i Array(Int32), arr_s Array(String),
    m    Map(String, Int64),
    lc   LowCardinality(String), lcn LowCardinality(Nullable(String)),
    i128 Int128, i256 Int256, u128 UInt128, u256 UInt256,
    d32  Decimal32(4), d64 Decimal64(6), d128 Decimal128(10),
    uu   UUID
) ENGINE = MergeTree ORDER BY tuple();`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	return schema
}

// inferCHTypeString parses "SELECT <expr> FROM t" and returns the inferred
// ClickHouse type as a string.
func inferCHTypeString(t *testing.T, schema *Schema, exprSQL string) string {
	t.Helper()
	statements, err := clickhouse.NewParser("SELECT " + exprSQL + " FROM t").ParseStmts()
	if err != nil {
		t.Fatalf("parse %q: %v", exprSQL, err)
	}
	selectQuery, ok := statements[0].(*clickhouse.SelectQuery)
	if !ok {
		t.Fatalf("parse %q: not a SELECT", exprSQL)
	}
	scope, _, err := resolveScope(selectQuery, schema)
	if err != nil {
		t.Fatalf("resolveScope(%q): %v", exprSQL, err)
	}
	inferred, err := inferExprType(selectQuery.SelectItems[0].Expr, scope)
	if err != nil {
		t.Fatalf("inferExprType(%q): %v", exprSQL, err)
	}
	return inferred.String()
}

func TestBareIntegerLiteralType(t *testing.T) {
	schema := wrapperTestSchema(t)
	cases := []struct{ expr, want string }{
		{"0", "UInt8"},
		{"1", "UInt8"},
		{"255", "UInt8"},
		{"256", "UInt16"},
		{"65535", "UInt16"},
		{"65536", "UInt32"},
		{"4294967295", "UInt32"},
		{"4294967296", "UInt64"},
		{"9223372036854775807", "UInt64"},
		{"18446744073709551615", "UInt64"},
		{"1.5", "Float64"},
		{"1e10", "Float64"},
	}
	for _, testCase := range cases {
		if got := inferCHTypeString(t, schema, testCase.expr); got != testCase.want {
			t.Errorf("type of %q = %s, want %s", testCase.expr, got, testCase.want)
		}
	}
}

func TestWrapperPropagation(t *testing.T) {
	schema := wrapperTestSchema(t)
	cases := []struct{ expr, want string }{
		// Transparent functions: Nullable moves through from any
		// argument; LowCardinality moves through only when exactly one
		// argument is LowCardinality and every other argument is a
		// constant literal.
		{"toString(ni32)", "Nullable(String)"},
		{"toString(lc)", "LowCardinality(String)"},
		{"toString(lcn)", "LowCardinality(Nullable(String))"},
		{"toString(s)", "String"},
		{"length(ns)", "Nullable(UInt64)"},
		{"length(lc)", "LowCardinality(UInt64)"},
		{"lower(lcn)", "LowCardinality(Nullable(String))"},
		{"trimLeft(ns)", "Nullable(String)"},
		{"toInt32(lc)", "LowCardinality(Int32)"},
		{"toUInt8(ni32)", "Nullable(UInt8)"},
		{"cityHash64(lc)", "LowCardinality(UInt64)"},
		{"empty(lc)", "LowCardinality(UInt8)"},
		{"notEmpty(ns)", "Nullable(UInt8)"},
		// Aggregate functions: Nullable moves through from the data
		// arguments; LowCardinality is always removed.
		{"avg(ni32)", "Nullable(Float64)"},
		{"avgIf(i32, nb)", "Float64"},
		{"sum(ni32)", "Nullable(Int64)"},
		{"min(lc)", "String"},
		{"min(lcn)", "Nullable(String)"},
		{"any(lc)", "String"},
		{"any(lcn)", "Nullable(String)"},
		{"argMax(f64, ni32)", "Nullable(Float64)"},
		{"argMax(lc, i32)", "String"},
		{"quantile(0.5)(ni32)", "Nullable(Float64)"},
		{"quantile(0.5)(toInt32(lc))", "Float64"},
		{"greatest(i32, ni32)", "Nullable(Int32)"},
		{"greatest(lc, s)", "String"},
		{"least(s, ns)", "Nullable(String)"},
		{"groupBitXor(ni32)", "Nullable(Int32)"},
		// the regression: Bool normalizes to UInt8 even under Nullable, and
		// LowCardinality is removed same as every other aggregate.
		{"groupBitAnd(nb)", "Nullable(UInt8)"},
		{"groupBitOr(toInt32(lc))", "Int32"},
		// The || concatenation: Nullable from any operand;
		// LowCardinality only when the other operand is a constant.
		{"'a' || lc", "LowCardinality(String)"},
		{"lc || 'a'", "LowCardinality(String)"},
		{"lc || lc", "String"},
		{"s || lc", "String"},
		{"lcn || 'a'", "LowCardinality(Nullable(String))"},
		{"lcn || lcn", "Nullable(String)"},
		{"'a' || ns", "Nullable(String)"},
		{"ns || s", "Nullable(String)"},
		// Comparisons and logical operators: Nullable from any operand.
		// A comparison and IN are the predicate family and always
		// answer UInt8. AND and OR preserve Bool when an operand has a
		// Bool base.
		{"ns = s", "Nullable(UInt8)"},
		{"b OR nb", "Nullable(Bool)"},
		{"b AND b", "Bool"},
		{"ni32 < i32", "Nullable(UInt8)"},
		{"ni32 IN (1, 2)", "Nullable(UInt8)"},
		{"lc = 'a'", "LowCardinality(UInt8)"},
		{"lc = lc", "UInt8"},
		// Arithmetic: the same constant rule decides LowCardinality;
		// Nullable moves through from any operand.
		{"length(lc) + 300", "LowCardinality(UInt64)"},
		{"length(lcn) + 300", "LowCardinality(Nullable(UInt64))"},
		{"u8 * length(lcn)", "Nullable(UInt64)"},
		{"toInt32(lc) % 7", "LowCardinality(Int16)"},
		// groupArray skips NULL values, so its element drops both
		// wrappers. The plain array constructor keeps them.
		{"groupArray(ns)", "Array(String)"},
		{"groupArray(lcn)", "Array(String)"},
		{"groupArray(length(lc))", "Array(UInt64)"},
		{"groupUniqArray(lc)", "Array(String)"},
		{"array(ns)", "Array(Nullable(String))"},
		{"array(lc)", "Array(LowCardinality(String))"},
		// Functions whose result never carries a wrapper.
		{"isNull(ns)", "UInt8"},
		{"count(ns)", "UInt64"},
		{"uniq(ns)", "UInt64"},
		{"ifNull(ni32, i32)", "Int32"},
		{"coalesce(ni32, i32)", "Int32"},
		{"assumeNotNull(ni32)", "Int32"},
	}
	for _, testCase := range cases {
		if got := inferCHTypeString(t, schema, testCase.expr); got != testCase.want {
			t.Errorf("type of %q = %s, want %s", testCase.expr, got, testCase.want)
		}
	}
}

// The registry makes the rule and the wrapper class one lexical unit, so
// a rule can no longer exist without a class. What is still worth a
// guard is the reverse of the old pairing question: a spec that carries
// no rule must be a name that inferFunctionType routes elsewhere, and
// not a forgotten entry.
func TestSpecWithoutARuleIsRoutedElsewhere(t *testing.T) {
	// These names have no rule, because their result depends on an
	// argument EXPRESSION and not only on the argument types.
	// inferFunctionType sends each of them to its own inference
	// function before the registry lookup.
	routedElsewhere := map[string]bool{
		"fromunixtimestamp64milli": true,
		"tostartofinterval":        true,
		"totimezone":               true,
		"tupleelement":             true,
		// and, or and xor route to inferLogicOperatorFunctionType,
		// because the result depends on the argument EXPRESSIONS (a
		// bare, or for xor a Nullable, Bool argument changes the base
		// type) and not only on the argument types. See the regression.
		"and": true,
		"or":  true,
		"xor": true,
	}
	for name := range higherOrderArrayFunctions {
		if spec, ok := functionRegistry[name]; ok && spec.rule == nil {
			routedElsewhere[name] = true
		}
	}
	for name, spec := range functionRegistry {
		if spec.rule != nil {
			continue
		}
		if !routedElsewhere[name] {
			t.Errorf("function %q has a registry spec with no type rule and no route of its own", name)
		}
	}
	for name := range routedElsewhere {
		spec, ok := functionRegistry[name]
		if !ok {
			t.Errorf("function %q is named as routed elsewhere but has no registry spec", name)
			continue
		}
		if spec.rule != nil {
			t.Errorf("function %q is routed elsewhere, so its spec must not carry a rule", name)
		}
	}
}

// The bug that the registry removes. Before the registry, membership of
// functionsWithIndependentResultType and of functionsWithFirstArgumentResult
// was a separate set, and nothing checked it. A new rule that belonged in
// functionsWithFirstArgumentResult but was absent from it took the generic
// path in silence: the generic path resolves every argument, thus a call
// such as argMax(f64, (a, b)) was refused although ClickHouse types it.
//
// The strategy now lives in the same spec as the rule. Its zero value
// argsUnset is never legal, so a spec that forgets the strategy fails
// here instead of taking the wrong path without a word.
func TestEverySpecDeclaresAStrategy(t *testing.T) {
	for name, spec := range functionRegistry {
		switch spec.strategy {
		case argsIndependent, argsFirstOnly, argsGeneric:
		case argsUnset:
			t.Errorf("function %q has a registry spec with no argument strategy; "+
				"give it argsIndependent, argsFirstOnly or argsGeneric", name)
		default:
			t.Errorf("function %q has an unknown argument strategy %d", name, spec.strategy)
		}
	}
}

// A function that takes no argument at all can only be argsIndependent.
// The generic path and the first-argument path both need an argument, so
// either would refuse the call.
func TestNoArgumentFunctionsAreIndependent(t *testing.T) {
	for _, name := range []string{"now", "now64", "today", "row_number", "rank", "dense_rank"} {
		spec, ok := functionRegistry[name]
		if !ok {
			t.Errorf("function %q has no registry spec", name)
			continue
		}
		if spec.strategy != argsIndependent {
			t.Errorf("function %q takes no arguments, so its strategy must be argsIndependent", name)
		}
	}
}

// A spec that carries an argument domain must also carry a rule, OR must
// be one of the names that inferFunctionType routes to its own inference
// function before the registry lookup. The domain is checked on the way
// to the rule for every other name, thus a domain without a rule and
// without a route would never be read.
//
// and, or and xor are the routed exception: inferLogicOperatorFunctionType
// reads logicOperatorArgumentDomain directly, against EVERY argument, and
// never through the generic rule path (which only ever checks argument
// zero, too narrow for this variadic family). See the regression.
func TestEveryDomainHasARule(t *testing.T) {
	routedElsewhere := map[string]bool{"and": true, "or": true, "xor": true, "fromunixtimestamp64milli": true}
	for name, spec := range functionRegistry {
		if spec.domain != nil && spec.rule == nil && !routedElsewhere[name] {
			t.Errorf("function %q has an argument domain but no type rule, so the domain is never read", name)
		}
	}
}

// The higher-order array table stays separate from the registry, because
// seven of its nine names have no type rule at all. Folding them in would
// add seven rule-less specs and would break the invariant that
// TestSpecWithoutARuleIsRoutedElsewhere depends on: a spec without a rule
// is a name with a route of its own.
//
// The two names that are in both tables are the risk, because a reader
// can change one table and miss the other. arrayExists and arraySort each
// have two paths: with a lambda first argument they go to
// inferHigherOrderArrayType, without one they go to the registry. This
// guard pins that both paths still exist for both names.
func TestHigherOrderNamesThatAlsoHaveASpec(t *testing.T) {
	bothPaths := []string{"arrayexists", "arraysort"}
	for _, name := range bothPaths {
		if _, ok := higherOrderArrayFunctions[name]; !ok {
			t.Errorf("function %q must stay in higherOrderArrayFunctions: it accepts a lambda", name)
		}
		if _, ok := functionRuleFor(name); !ok {
			t.Errorf("function %q must keep a registry rule: it also accepts a plain array argument", name)
		}
	}
	for name := range higherOrderArrayFunctions {
		_, hasRule := functionRuleFor(name)
		listed := false
		for _, both := range bothPaths {
			if both == name {
				listed = true
			}
		}
		if hasRule && !listed {
			t.Errorf("function %q is higher-order and has a registry rule, but the guard list does not name it", name)
		}
	}
}

// The predicate body domain is a property of the higher-order function,
// not of its result shape.
func TestPredicateBodyNamesAreHigherOrder(t *testing.T) {
	for name, rule := range higherOrderArrayFunctions {
		if rule.bodyDomain == hofBodyDomainPredicate && rule.result == hofArrayOfBody {
			t.Errorf("function %q has an incompatible predicate result rule", name)
		}
	}
}

// TestConcatOperatorBaseTypeIsAlwaysString pins the base result type of
// the || concatenation operator.
//
// || is the concat function, thus every operand is converted to its
// string form and the base result is always String. No operand base type
// survives. Each case below was measured on ClickHouse 25.8.29.51 (image
// clickhouse/clickhouse-server:25.8, disposable container) with
// DESCRIBE (SELECT <expression> AS x FROM t) on real table columns, never
// on literals alone, because ClickHouse folds constants.
//
// The rule that this test replaces kept the left operand base type, with
// a special case for Float and for FixedString. It answered
// Enum8('a' = 1, 'zz' = 2) for e8 || s and Int64 for i64 || s, where the
// server answers String. That is a silently wrong type: the generated Go
// would scan a String column into an enum or integer field.
//
// The Nullable and LowCardinality wrappers are a separate rule and still
// move from the operands into the result, thus the Enum cases with a
// Nullable operand are here as well.
func TestConcatOperatorBaseTypeIsAlwaysString(t *testing.T) {
	schema, err := schemaFromDDLErr(t, `CREATE TABLE t (
    e8   Enum8('a' = 1, 'zz' = 2),
    e16  Enum16('a' = 1, 'zz' = 2),
    e8b  Enum8('q' = 3, 'w' = 4),
    s    String,
    fs   FixedString(5),
    lcs  LowCardinality(String),
    ns   Nullable(String),
    lcns LowCardinality(Nullable(String)),
    i64  Int64,
    f64  Float64,
    dec  Decimal(10, 2)
) ENGINE = MergeTree ORDER BY tuple();`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	cases := []struct {
		expr string
		want string
	}{
		// An Enum operand decays to String. This is the defect that
		// the test exists for.
		{"e8 || s", "String"},
		{"e8 || lcs", "String"},
		{"e8 || fs", "String"},
		{"e8 || e8", "String"},
		{"e8 || e8b", "String"},
		{"e8 || 'x'", "String"},
		{"'x' || e8", "String"},
		{"e16 || s", "String"},
		{"e16 || lcs", "String"},
		{"e16 || e8b", "String"},
		{"e8 || i64", "String"},
		{"e8 || f64", "String"},
		// A numeric operand decays to String as well. The earlier
		// Float special case answered Float64 here.
		{"i64 || s", "String"},
		{"i64 || i64", "String"},
		{"f64 || s", "String"},
		{"dec || s", "String"},
		// FixedString keeps the measured String result.
		{"fs || fs", "String"},
		{"fs || s", "String"},
		// The wrappers still move through, over an Enum operand too.
		{"e8 || ns", "Nullable(String)"},
		{"e8 || lcns", "Nullable(String)"},
		{"e16 || ns", "Nullable(String)"},
		{"'a' || lcs", "LowCardinality(String)"},
		{"lcs || 'a'", "LowCardinality(String)"},
		{"lcs || lcs", "String"},
	}
	for _, testCase := range cases {
		t.Run(testCase.expr, func(t *testing.T) {
			if got := inferCHTypeString(t, schema, testCase.expr); got != testCase.want {
				t.Errorf("inferExprType(%q) = %q, want %q", testCase.expr, got, testCase.want)
			}
		})
	}
}

// TestNeverNullableResultKeepsNoNullable pins the rule for a result type
// that cannot go inside Nullable.
//
// ClickHouse refuses Nullable(Array(...)), Nullable(Map(...)) and
// Nullable(Tuple(...)). When an argument is Nullable but the result type
// is one of these, the server gives back the bare type. It does not
// refuse the call and it does not add a Nullable wrapper.
//
// Every expected type below was measured on ClickHouse 25.8.29.51 with
// real columns in a real table, never with literals, because the server
// folds a literal and a folded result gives a different type. The
// commands had this shape:
//
//	SELECT toTypeName(argMin(arr_i, ni32)) FROM w GROUP BY g
//	Array(Int32)
//
// Before the fix chgen answered Nullable(Array(Int32)) here. That type
// does not exist on the server.
func TestNeverNullableResultKeepsNoNullable(t *testing.T) {
	schema := wrapperTestSchema(t)
	cases := []struct{ expr, want string }{
		// The ordering argument is Nullable. The result is an Array,
		// thus the Nullable goes away.
		{"argMin(arr_i, ni32)", "Array(Int32)"},
		{"argMax(arr_i, ni32)", "Array(Int32)"},
		{"argMin(arr_s, ni32)", "Array(String)"},
		// The same rule for a Map result.
		{"argMin(m, ni32)", "Map(String, Int64)"},
		{"argMax(m, ni32)", "Map(String, Int64)"},
		// The same rule for a Tuple result.
		// A LowCardinality member is kept out of this tuple on
		// purpose. This test is about Nullable only. The separate
		// rule for a LowCardinality member inside a container is now
		// measured and pinned in
		// TestAggregateStripsLowCardinalityInsideContainer.
		{"argMin((i32, arr_s), ni32)", "Tuple(Int32, Array(String))"},
		// The If combinator keeps the same rule.
		{"argMinIf(arr_i, ni32, i32 > 0)", "Array(Int32)"},
		// A scalar result still takes the Nullable of the ordering
		// argument. This is the control case: the fix must not remove
		// a Nullable that the server keeps.
		{"argMin(i32, ni32)", "Nullable(Int32)"},
		{"argMax(f64, ni32)", "Nullable(Float64)"},
		// A scalar result still takes the Nullable of the first
		// argument.
		{"argMin(ni32, i32)", "Nullable(Int32)"},
		// A one-argument aggregate over an Array keeps the bare type.
		{"min(arr_i)", "Array(Int32)"},
		{"any(m)", "Map(String, Int64)"},
	}
	for _, testCase := range cases {
		t.Run(testCase.expr, func(t *testing.T) {
			if got := inferCHTypeString(t, schema, testCase.expr); got != testCase.want {
				t.Errorf("inferExprType(%q) = %q, want %q", testCase.expr, got, testCase.want)
			}
		})
	}
}

// TestCanBeInsideNullable pins which base types accept a Nullable wrapper.
//
// Each answer was measured on ClickHouse 25.8.29.51 with a CAST:
//
//	SELECT toTypeName(CAST(NULL AS Nullable(Array(Int32))))
//	Code: 43. Nested type Array(Int32) cannot be inside Nullable type
//	SELECT toTypeName(CAST(NULL AS Nullable(SimpleAggregateFunction(sum, Int64))))
//	Nullable(SimpleAggregateFunction(sum, Int64))
//
// SimpleAggregateFunction is in this table because its name starts with
// the name of a refused type. It must stay accepted.
func TestCanBeInsideNullable(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"Int64", true},
		{"String", true},
		{"Decimal", true},
		{"SimpleAggregateFunction", true},
		{"Array", false},
		{"Map", false},
		{"Tuple", false},
		{"AggregateFunction", false},
		// The name test does not depend on letter case.
		{"array", false},
		{"AGGREGATEFUNCTION", false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := canBeInsideNullable(CHType{Name: testCase.name})
			if got != testCase.want {
				t.Errorf("canBeInsideNullable(%q) = %v, want %v", testCase.name, got, testCase.want)
			}
		})
	}
}
