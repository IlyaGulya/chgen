package engine

import (
	"strings"
	"testing"
)

// Tuple element access. Every rule below is measured on ClickHouse
// 25.8.29.51 with DESCRIBE (SELECT <expression> FROM t) over real table
// columns, never over literals, because ClickHouse folds a constant and a
// folded constant reports a different LowCardinality wrapper.
//
//	tup Tuple(Int32, String)                     tup.1  -> Int32
//	                                             tup.2  -> String
//	                                             tup.0  -> CH error
//	                                             tup.3  -> CH error
//	nn  Tuple(a Nullable(Int32), b LowCardinality(String))
//	                                             nn.a   -> LowCardinality kept
//	                                             nn.2   -> String (LowCardinality dropped)
//	                                    tupleElement(nn, 'b') -> String
//
// A Tuple can never carry Nullable or LowCardinality itself: ClickHouse
// rejects both wrappers around a Tuple (ILLEGAL_TYPE_OF_ARGUMENT).

const tupleSchemaDDL = `CREATE TABLE t (
    id UInt64,
    tup Tuple(Int32, String),
    named Tuple(a Nullable(Int32), b LowCardinality(String)),
    nested Tuple(Int32, Tuple(String, Int64)),
    withArray Tuple(Array(Int32), String)
) ENGINE = MergeTree ORDER BY tuple();`

func tupleResultType(t *testing.T, expression string) (Query, error) {
	t.Helper()
	schema, err := schemaFromDDLErr(t, tupleSchemaDDL)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	queries, err := parseQueriesWithSchema(t, `-- name: Probe :one
SELECT `+expression+` AS value
FROM t
WHERE id = chgen.arg('ID')`, schema)
	if err != nil {
		return Query{}, err
	}
	return queries[0], nil
}

func TestParseSchemaKeepsTupleElementTypes(t *testing.T) {
	schema, err := schemaFromDDLErr(t, tupleSchemaDDL)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	if got, want := schema.Tables["t"].Columns["tup"].Type.String(), "Tuple(Int32, String)"; got != want {
		t.Errorf("tup type = %q, want %q", got, want)
	}
	if got, want := schema.Tables["t"].Columns["named"].Type.String(),
		"Tuple(a Nullable(Int32), b LowCardinality(String))"; got != want {
		t.Errorf("named type = %q, want %q", got, want)
	}
}

func TestTupleElementAccessTypes(t *testing.T) {
	cases := []struct {
		expression string
		goType     string
	}{
		{"tup.1", "int32"},
		{"tup.2", "string"},
		{"tupleElement(tup, 1)", "int32"},
		{"tupleElement(tup, 2)", "string"},
		{"named.1", "*int32"},
		// LowCardinality is dropped by the index form and by every
		// tupleElement form; String and LowCardinality(String) share
		// the Go type, so the wrapper check lives in the CHType test
		// below.
		{"named.2", "string"},
		{"tupleElement(named, 'a')", "*int32"},
		{"tupleElement(named, 'b')", "string"},
		{"withArray.1", "[]int32"},
		{"tupleElement(nested.2, 1)", "string"},
		{"tupleElement(tupleElement(nested, 2), 2)", "int64"},
		{"toString(tup.1)", "string"},
	}
	for _, testCase := range cases {
		t.Run(testCase.expression, func(t *testing.T) {
			query, err := tupleResultType(t, testCase.expression)
			if err != nil {
				t.Fatalf("parseQueriesWithSchema(t, %s) error = %v", testCase.expression, err)
			}
			if got := query.Results[0].GoType; got != testCase.goType {
				t.Errorf("%s Go type = %q, want %q", testCase.expression, got, testCase.goType)
			}
		})
	}
}

func TestTupleElementAccessWrappers(t *testing.T) {
	schema, err := schemaFromDDLErr(t, tupleSchemaDDL)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	cases := []struct {
		expression string
		chType     string
	}{
		// The dot form with an element name keeps LowCardinality.
		{"named.b", "LowCardinality(String)"},
		// The index form and every tupleElement form drop it.
		{"named.2", "String"},
		{"tupleElement(named, 'b')", "String"},
		{"tupleElement(named, 2)", "String"},
		// Nullable on an element is kept by every form.
		{"named.a", "Nullable(Int32)"},
		{"named.1", "Nullable(Int32)"},
		{"tupleElement(named, 'a')", "Nullable(Int32)"},
	}
	for _, testCase := range cases {
		t.Run(testCase.expression, func(t *testing.T) {
			if got := inferCHTypeString(t, schema, testCase.expression); got != testCase.chType {
				t.Errorf("%s ClickHouse type = %q, want %q", testCase.expression, got, testCase.chType)
			}
		})
	}
}

func TestTupleElementAccessRefusesOutOfRange(t *testing.T) {
	// ClickHouse itself rejects both of these with NOT_FOUND_COLUMN_IN_BLOCK,
	// so chgen must not invent a type.
	for _, expression := range []string{"tup.0", "tup.3", "tupleElement(tup, 3)", "tupleElement(tup, 'x')"} {
		t.Run(expression, func(t *testing.T) {
			_, err := tupleResultType(t, expression)
			if err == nil {
				t.Fatalf("parseQueriesWithSchema(t, %s) succeeded; want a refusal", expression)
			}
			if !strings.Contains(err.Error(), "Tuple") {
				t.Errorf("error = %q, want it to name the Tuple", err)
			}
		})
	}
}

func TestTupleElementAccessRefusesNonTuple(t *testing.T) {
	_, err := tupleResultType(t, "id.1")
	if err == nil {
		t.Fatal("parseQueriesWithSchema(t, id.1) succeeded on a non-tuple")
	}
}

// A Tuple as a whole result column has no faithful Go representation, so it
// must refuse explicitly, in the way that the Map-with-an-IP-key refusal
// does. It must never guess a Go type.
func TestTupleResultColumnRefusesWithAnExplicitError(t *testing.T) {
	_, err := tupleResultType(t, "tup")
	if err == nil {
		t.Fatal("parseQueriesWithSchema(t, tup) succeeded; a Tuple result has no Go mapping")
	}
	if !strings.Contains(err.Error(), "no Go mapping") {
		t.Errorf("error = %q, want it to say that the Tuple has no Go mapping", err)
	}
	if !strings.Contains(err.Error(), "Tuple(Int32, String)") {
		t.Errorf("error = %q, want it to name the full Tuple type", err)
	}
}

// The path form t.x must still read the qualified column when the qualifier
// is a table, even when a column of that name exists too. The tuple branch
// fires only when the qualifier resolves to a Tuple.
func TestPathKeepsTheQualifiedColumnWhenTheQualifierIsNotATuple(t *testing.T) {
	schema, err := schemaFromDDLErr(t, `CREATE TABLE t (t Int32, x String) ENGINE = MergeTree ORDER BY tuple();`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	if got, want := inferCHTypeString(t, schema, "t.x"), "String"; got != want {
		t.Errorf("t.x = %q, want %q", got, want)
	}
}

// A Tuple whose declaration gives no element names has no name to match.
func TestTupleElementByNameRefusesOnAnUnnamedTuple(t *testing.T) {
	_, err := tupleResultType(t, "tupleElement(tup, 'a')")
	if err == nil {
		t.Fatal("parseQueriesWithSchema(t, tupleElement(tup, 'a')) succeeded on an unnamed Tuple")
	}
	if !strings.Contains(err.Error(), "unnamed") {
		t.Errorf("error = %q, want it to say that the elements are unnamed", err)
	}
}

// The three-argument form replaces an out-of-range element with the default,
// and the result is the type of the default (measured on ClickHouse
// 25.8.29.51: tupleElement(tup, 3, 'def') is String).
func TestTupleElementDefaultArgumentGivesItsOwnType(t *testing.T) {
	schema, err := schemaFromDDLErr(t, tupleSchemaDDL)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	if got, want := inferCHTypeString(t, schema, "tupleElement(tup, 3, 'def')"), "String"; got != want {
		t.Errorf("tupleElement(tup, 3, 'def') = %q, want %q", got, want)
	}
}

// ClickHouse refuses a non-constant element selector, so chgen must too.
func TestTupleElementRefusesANonConstantSelector(t *testing.T) {
	_, err := tupleResultType(t, "tupleElement(tup, id)")
	if err == nil {
		t.Fatal("parseQueriesWithSchema(t, tupleElement(tup, id)) succeeded with a non-constant selector")
	}
	if !strings.Contains(err.Error(), "constant") {
		t.Errorf("error = %q, want it to demand a constant selector", err)
	}
}
