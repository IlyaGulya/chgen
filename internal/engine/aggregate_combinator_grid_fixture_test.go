package engine

import (
	"fmt"
	"strings"
)

// The fixture of the combinator grid.
//
// It has NO build tag, for the same reason as wrappergrid_fixture_test.go:
// the fixture must be readable by the default `go test ./...` run so the
// shape gates can run without a server, while the measuring test itself
// stays behind the fuzzoracle tag.
//
// The table is `cg` ("combinator grid"), a name distinct from the wrapper
// grid's `g` and from the oracle's `t`, so the three fixtures cannot be
// confused inside a database that happens to hold more than one of them.
// The table needs AggregatingMergeTree, not MergeTree: it stores
// AggregateFunction state columns, and the server refuses those on a
// plain MergeTree.

// combGridSchemaDDL builds the CREATE TABLE of the combinator grid
// fixture.
//
// Every column is either a plain value column that a combinator's data
// argument reads, or a state column that a -Merge cell reads. The state
// columns are the fixture's own measured limit. Each of the 19 bases has
// a bare state and an -If state. Median uses the canonical quantile state
// identity because the server records the alias in that form. The chained
// suffixes -IfMerge and -IfMergeState read a state that an -If form built.
// A state that the bare form builds does not satisfy them. Measured on
// ClickHouse 25.8.29.51 over the sum columns:
//
//	sumIfMerge(c_state_sumif)       Int64
//	sumIfMergeState(c_state_sumif)  AggregateFunction(sumIf, Int32, UInt8)
//	sumMergeState(c_state_sumif)    Code 42, sum takes one argument
//	avgIfMerge(c_state_sumif)       Code 43, the state name does not match
//
// The full state fixture makes every Merge and IfMerge product cell
// measurable.
type combGridBaseFixture struct {
	base         string
	key          string
	parameter    string
	dataArgs     []string
	arrayArgs    []string
	stateType    string
	stateBuild   string
	ifStateType  string
	ifStateBuild string
}

func combGridBaseFixtures() []combGridBaseFixture {
	unary := func(base, key, stateName string) combGridBaseFixture {
		return combGridBaseFixture{
			base: base, key: key, dataArgs: []string{"c_i32"}, arrayArgs: []string{"c_arr_i32"},
			stateType:    fmt.Sprintf("AggregateFunction(%s, Int32)", stateName),
			stateBuild:   fmt.Sprintf("%sState(toInt32(1))", base),
			ifStateType:  fmt.Sprintf("AggregateFunction(%sIf, Int32, UInt8)", stateName),
			ifStateBuild: fmt.Sprintf("%sIfState(toInt32(1), toUInt8(1))", base),
		}
	}
	result := []combGridBaseFixture{
		unary("sum", "sum", "sum"),
		unary("min", "min", "min"),
		unary("max", "max", "max"),
		unary("any", "any", "any"),
		unary("anyLast", "anylast", "anyLast"),
		unary("avg", "avg", "avg"),
	}
	for _, base := range []string{"argMin", "argMax"} {
		key := strings.ToLower(base)
		result = append(result, combGridBaseFixture{
			base: base, key: key,
			dataArgs: []string{"c_i32", "c_i32"}, arrayArgs: []string{"c_arr_i32", "c_arr_i32"},
			stateType:    fmt.Sprintf("AggregateFunction(%s, Int32, Int32)", base),
			stateBuild:   fmt.Sprintf("%sState(toInt32(1), toInt32(2))", base),
			ifStateType:  fmt.Sprintf("AggregateFunction(%sIf, Int32, Int32, UInt8)", base),
			ifStateBuild: fmt.Sprintf("%sIfState(toInt32(1), toInt32(2), toUInt8(1))", base),
		})
	}
	result = append(result, combGridBaseFixture{
		base: "count", key: "count",
		stateType: "AggregateFunction(count)", stateBuild: "countState()",
		ifStateType: "AggregateFunction(countIf, UInt8)", ifStateBuild: "countIfState(toUInt8(1))",
	})
	for _, base := range []string{"uniq", "uniqExact", "uniqCombined"} {
		result = append(result, unary(base, strings.ToLower(base), base))
	}
	for _, base := range []string{"quantile", "median"} {
		result = append(result, combGridBaseFixture{
			base: base, key: strings.ToLower(base), parameter: "0.5",
			dataArgs: []string{"c_f64"}, arrayArgs: []string{"c_arr_i32"},
			stateType:    "AggregateFunction(quantile(0.5), Float64)",
			stateBuild:   fmt.Sprintf("%sState(0.5)(toFloat64(1))", base),
			ifStateType:  "AggregateFunction(quantileIf(0.5), Float64, UInt8)",
			ifStateBuild: fmt.Sprintf("%sIfState(0.5)(toFloat64(1), toUInt8(1))", base),
		})
	}
	for _, base := range []string{"groupArray", "groupUniqArray", "groupBitAnd", "groupBitOr", "groupBitXor"} {
		result = append(result, unary(base, strings.ToLower(base), base))
	}
	return result
}

func combGridBaseFixtureFor(base string) (combGridBaseFixture, bool) {
	for _, fixture := range combGridBaseFixtures() {
		if fixture.base == base {
			return fixture, true
		}
	}
	return combGridBaseFixture{}, false
}

func combGridSchemaDDL() string {
	columns := []string{
		"c_i32 Int32", "c_u8 UInt8", "c_bool Bool", "c_f64 Float64", "c_arr_i32 Array(Int32)",
	}
	for _, fixture := range combGridBaseFixtures() {
		columns = append(columns,
			fmt.Sprintf("c_state_%s %s", fixture.key, fixture.stateType),
			fmt.Sprintf("c_state_%s_if %s", fixture.key, fixture.ifStateType),
		)
	}
	return "CREATE TABLE cg (\n    " + strings.Join(columns, ",\n    ") + "\n) ENGINE = AggregatingMergeTree ORDER BY tuple()"
}

// combGridSeedRow builds the one seed row. One row is enough: the grid
// measures TYPES, and a type does not depend on the row count.
//
// The state columns are built with the *State combinator of their own
// base aggregate over a plain literal, then CAST to pin the exact
// declared column type. A VALUES literal cannot write an
// AggregateFunction column at all, thus INSERT ... SELECT with an
// explicit build expression is required.
func combGridSeedRow() string {
	values := []string{"1", "1", "true", "1.0", "[1, 2]"}
	for _, fixture := range combGridBaseFixtures() {
		values = append(values,
			fmt.Sprintf("CAST(%s, '%s')", fixture.stateBuild, fixture.stateType),
			fmt.Sprintf("CAST(%s, '%s')", fixture.ifStateBuild, fixture.ifStateType),
		)
	}
	return "INSERT INTO cg SELECT\n    " + strings.Join(values, ",\n    ")
}
