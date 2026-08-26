package engine

import (
	"strings"
	"testing"
)

// concat and the "||" operator over CONTAINER arguments.
//
// Every answer below was measured on ClickHouse 25.8.29.51 over real
// columns of a real AggregatingMergeTree table with one row, in the probe
// database probe_w36a, never over literals, because the server folds a
// constant and then reports a different type. Every cell was confirmed
// with a VALUE select as well as with toTypeName, because toTypeName is
// analysis and is blind to a refusal that happens while the call runs.
//
// THE DEFECT. The rule of concat was the constant String. concat over
// two arrays JOINS the arrays and answers the Array type, thus the
// constant answered String for a call whose value is an Array. That is a
// silently wrong type: the generated Go scans an Array column into a
// string field.
//
// THE MEASURED RULE. concat does two different things, and the argument
// types select which:
//
//   - Two or more arguments, and EVERY argument is an Array, or EVERY
//     argument is a Map: the containers are JOINED and the result is the
//     common supertype of the arguments.
//   - Everything else, INCLUDING a mixed call and INCLUDING arity one:
//     each argument is turned into its string form and the result is
//     String.
//
// The columns of the probe table:
//
//	p_arr_i32  Array(Int32)                                   [3,4]
//	p_arr_s    Array(String)                                  ['c','d']
//	p_arr_n    Array(Nullable(Int32))                         [1,NULL]
//	a8         Array(Int8)                                    [1]
//	alc        Array(LowCardinality(String))                  ['x']
//	p_map      Map(String, Int64)                             {'m':1}
//	p_tup      Tuple(String, Int32)                           ('t',2)
//	c_arr_i32  SimpleAggregateFunction(anyLast, Array(Int32)) [1,2]
//	c_arr_s    SimpleAggregateFunction(anyLast, Array(String))['a','b']
//	p_s        String                                         's'
//
// THE JOIN CELLS:
//
//	concat(p_arr_i32, p_arr_i32)              Array(Int32)             [3,4,3,4]
//	p_arr_i32 || p_arr_i32                    Array(Int32)             [3,4,3,4]
//	concat(p_arr_s, p_arr_s)                  Array(String)            ['c','d','c','d']
//	p_arr_s || p_arr_s                        Array(String)            ['c','d','c','d']
//	concat(p_arr_i32, p_arr_i32, p_arr_i32)   Array(Int32)             [3,4,3,4,3,4]
//	concat(p_arr_i32, p_arr_n)                Array(Nullable(Int32))   [3,4,1,NULL]
//	concat(a8, a32)                           Array(Int32)             (supertype widens)
//	concat(alc, alc)                          Array(String)            (element LowCardinality comes off)
//	concat(p_map, p_map)                      Map(String, Int64)       {'m':1,'m':1}
//	p_map || p_map                            Map(String, Int64)       {'m':1,'m':1}
//
// THE MARKER CELLS. These four are the MISMATCH cells that the ticket
// names. The marker is DROPPED: the result is a bare Array. concat is not
// a value-preserving rule, thus the caller replaces the marker by its
// inner type before the rule runs, and this file adds no second marker
// walk.
//
//	concat(c_arr_i32, c_arr_i32)   Array(Int32)    [1,2,1,2]
//	c_arr_i32 || c_arr_i32         Array(Int32)    [1,2,1,2]
//	concat(c_arr_s, c_arr_s)       Array(String)   ['a','b','a','b']
//	c_arr_s || c_arr_s             Array(String)   ['a','b','a','b']
//	concat(c_arr_i32, p_arr_i32)   Array(Int32)    [1,2,3,4]
//
// THE STRING CELLS THAT MUST NOT MOVE. A mixed call is NOT a refusal.
// The server makes the string form of the container:
//
//	concat(p_arr_i32, p_s)   String   [3,4]s
//	concat(p_s, p_arr_i32)   String   s[3,4]
//	p_arr_i32 || p_s         String   [3,4]s
//	concat(p_map, p_s)       String   {'m':1}s
//	concat(p_s, p_s)         String   ss
//	p_s || p_s               String   ss
//
// ARITY ONE IS THE STRING CASE for every shape. A rule built on an
// arity-one cell alone would have been wrong, exactly as the arity-one
// cell of greatest was wrong in an earlier change:
//
//	concat(p_arr_i32)   String   [3,4]
//	concat(p_map)       String   {'m':1}
//	concat(p_tup)       String   ('t',2)

// concatContainerSchema holds the container alphabet: two element types,
// a Nullable element, a narrow integer element, a LowCardinality element,
// a Map, a Tuple, and the marker forms of the two arrays.
const concatContainerSchema = `
CREATE TABLE t (
    k         UInt8,
    p_arr_i32 Array(Int32),
    p_arr_s   Array(String),
    p_arr_u64 Array(UInt64),
    p_arr_n   Array(Nullable(Int32)),
    a8        Array(Int8),
    alc       Array(LowCardinality(String)),
    p_map     Map(String, Int64),
    p_tup     Tuple(String, Int32),
    c_arr_i32 SimpleAggregateFunction(anyLast, Array(Int32)),
    c_arr_s   SimpleAggregateFunction(anyLast, Array(String)),
    p_s       String,
    p_i32     Int32
);
`

// TestConcatJoinsContainersAndKeepsTheContainerType pins the measured
// concat rule. The marker rows are the four MISMATCH cells of the census.
// The plain-array rows show the rule is about the ARRAY and not about the
// marker, and the String rows are the cells that must not move.
func TestConcatJoinsContainersAndKeepsTheContainerType(t *testing.T) {
	schema, err := schemaFromDDLErr(t, concatContainerSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	cases := []struct{ sql, want string }{
		// The join case, with no marker anywhere. The rule is about
		// the Array, not about the marker.
		{"SELECT concat(p_arr_i32, p_arr_i32) AS a FROM t", "Array(Int32)"},
		{"SELECT p_arr_i32 || p_arr_i32 AS a FROM t", "Array(Int32)"},
		{"SELECT concat(p_arr_s, p_arr_s) AS a FROM t", "Array(String)"},
		{"SELECT p_arr_s || p_arr_s AS a FROM t", "Array(String)"},
		// Variadic. concat has maxArity -1 and joins every argument.
		{"SELECT concat(p_arr_i32, p_arr_i32, p_arr_i32) AS a FROM t", "Array(Int32)"},
		{"SELECT concat(p_arr_s, p_arr_s, p_arr_s, p_arr_s) AS a FROM t", "Array(String)"},
		// The result is the SUPERTYPE, not the first argument.
		//
		// concat(p_arr_i32, p_arr_n) is NOT pinned here. The server
		// answers Array(Nullable(Int32)), and commonCHType answers
		// Array(Int32): compatibleNamedArgTypes matches the two Arrays
		// before the element branch runs, thus the element Nullable is
		// lost. That defect is in the SHARED supertype helper and it
		// shows over every caller of it, not over concat alone. It is
		// left to its own ticket rather than patched behind concat,
		// because a second element rule here would be the repeated-code
		// defect that this repository removes.
		{"SELECT concat(a8, p_arr_i32) AS a FROM t", "Array(Int32)"},
		// An element LowCardinality comes off, as commonCHTypes does.
		{"SELECT concat(alc, alc) AS a FROM t", "Array(String)"},
		// Map joins exactly as Array does.
		{"SELECT concat(p_map, p_map) AS a FROM t", "Map(String, Int64)"},
		{"SELECT p_map || p_map AS a FROM t", "Map(String, Int64)"},
		// THE FOUR MISMATCH CELLS OF THE CENSUS. The marker is
		// dropped and the Array stays.
		{"SELECT concat(c_arr_i32, c_arr_i32) AS a FROM t", "Array(Int32)"},
		{"SELECT c_arr_i32 || c_arr_i32 AS a FROM t", "Array(Int32)"},
		{"SELECT concat(c_arr_s, c_arr_s) AS a FROM t", "Array(String)"},
		{"SELECT c_arr_s || c_arr_s AS a FROM t", "Array(String)"},
		// A marker argument and a plain argument mix freely.
		{"SELECT concat(c_arr_i32, p_arr_i32) AS a FROM t", "Array(Int32)"},
		{"SELECT concat(p_arr_i32, c_arr_i32) AS a FROM t", "Array(Int32)"},
		// The string case. A MIXED call is not a refusal: the server
		// makes the string form of the container.
		{"SELECT concat(p_arr_i32, p_s) AS a FROM t", "String"},
		{"SELECT concat(p_s, p_arr_i32) AS a FROM t", "String"},
		{"SELECT p_arr_i32 || p_s AS a FROM t", "String"},
		{"SELECT concat(p_map, p_s) AS a FROM t", "String"},
		{"SELECT concat(p_arr_i32, p_i32) AS a FROM t", "String"},
		// The plain string cells that must NOT move.
		{"SELECT concat(p_s, p_s) AS a FROM t", "String"},
		{"SELECT p_s || p_s AS a FROM t", "String"},
		{"SELECT concat(p_s, p_s, p_s) AS a FROM t", "String"},
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

// TestConcatRefusesWhatTheServerRefuses pins the refusal side. The server
// answers Code: 386 (NO_COMMON_TYPE) for a container pair that has no
// supertype, and chgen must refuse the same pair rather than answer a
// type for a call that cannot run.
//
// Measured on ClickHouse 25.8.29.51, confirmed with a value select:
//
//	concat(p_arr_i32, p_arr_u64)   Code: 386
//	concat(p_arr_i32, p_arr_s)     Code: 386
//	concat(alc, p_arr_i32)         Code: 386
//
// The Tuple row is a refusal that chgen CHOOSES. The server accepts the
// call and FLATTENS the element lists:
//
//	concat(p_tup, p_tup)   Tuple(String, Int32, String, Int32)   ('t',2,'t',2)
//
// A supertype cannot express that shape, thus chgen refuses rather than
// answer a wrong Tuple. A refusal is mild; a wrong shape is the worst
// defect class.
func TestConcatRefusesWhatTheServerRefuses(t *testing.T) {
	schema, err := schemaFromDDLErr(t, concatContainerSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	cases := []string{
		"SELECT concat(p_arr_i32, p_arr_u64) AS a FROM t",
		"SELECT concat(p_arr_i32, p_arr_s) AS a FROM t",
		"SELECT concat(alc, p_arr_i32) AS a FROM t",
		"SELECT concat(p_tup, p_tup) AS a FROM t",
	}
	for _, sql := range cases {
		got, err := inferSelectItemCHType(t, schema, sql)
		if err == nil {
			t.Errorf("%s: CH type = %q, want a refusal", sql, got)
		}
	}
}

// TestConcatAtArityOneIsTheStringForm pins the arity boundary.
//
// This is the cell that a narrower rule would have got wrong. concat over
// ONE container does NOT join anything: it makes the string form. A rule
// that read "an Array argument means an Array result" would answer
// Array(Int32) here, where the server answers String.
//
// Measured on ClickHouse 25.8.29.51, confirmed with a value select:
//
//	concat(p_arr_i32)   String   [3,4]
//	concat(p_arr_s)     String   ['c','d']
//	concat(p_map)       String   {'m':1}
//	concat(p_tup)       String   ('t',2)
//	concat(c_arr_i32)   String   [1,2]
//	concat(p_s)         String   s
func TestConcatAtArityOneIsTheStringForm(t *testing.T) {
	schema, err := schemaFromDDLErr(t, concatContainerSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	cases := []string{
		"SELECT concat(p_arr_i32) AS a FROM t",
		"SELECT concat(p_arr_s) AS a FROM t",
		"SELECT concat(p_map) AS a FROM t",
		"SELECT concat(p_tup) AS a FROM t",
		"SELECT concat(c_arr_i32) AS a FROM t",
		"SELECT concat(p_s) AS a FROM t",
	}
	for _, sql := range cases {
		got, err := inferSelectItemCHType(t, schema, sql)
		if err != nil {
			t.Errorf("%s: error = %v", sql, err)
			continue
		}
		if got != "String" {
			t.Errorf("%s: CH type = %q, want %q", sql, got, "String")
		}
	}
}

// TestConcatRuleIsNotAConstant guards the rule against a return to the
// constant String. fixedResultFunctionType reports whether a rule answers
// the same type for two unrelated argument types. The concat rule must
// NOT be constant, because a constant rule cannot see the Array that the
// server joins.
//
// This guard is cheap and it names the defect directly, so that a later
// edit that replaces the rule by fixedFunctionType("String") fails here
// with a message about the cause and not only in the cell tests.
func TestConcatRuleIsNotAConstant(t *testing.T) {
	rule, ok := functionRuleFor("concat")
	if !ok {
		t.Fatal("the function registry has no concat entry")
	}
	// fixedResultFunctionType probes with two SCALAR types, and concat
	// answers String for both of those, correctly. Thus that helper
	// cannot see this defect and the probe here is the container pair.
	arrayInt32 := CHType{Name: "Array", Params: []CHType{{Name: "Int32"}}}
	joined, err := rule([]CHType{arrayInt32, arrayInt32})
	if err != nil {
		t.Fatalf("concat over two Array(Int32) must have a type, got error %v", err)
	}
	if joined.String() != "Array(Int32)" {
		t.Errorf("concat over two Array(Int32) = %q, want %q; a constant String rule "+
			"answers String here, which is the silently wrong type this rule fixes",
			joined.String(), "Array(Int32)")
	}
	// The strategy must pass the arguments. With argsIndependent the
	// caller hands the rule nil and no rule body could see the Array.
	if functionStrategyFor("concat") == argsIndependent {
		t.Error("the concat entry uses argsIndependent, thus the rule is called with nil " +
			"and cannot tell the container join from the string form")
	}
	// The refusal message must name the cause, so that a user who hits
	// the Tuple case can act on it.
	_, err = rule([]CHType{{Name: "Tuple"}, {Name: "Tuple"}})
	if err == nil {
		t.Fatal("concat over two Tuples must be refused")
	}
	if !strings.Contains(err.Error(), "Tuple") {
		t.Errorf("the refusal message must name Tuple, got %q", err.Error())
	}
}
