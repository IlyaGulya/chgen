package engine

import "testing"

// This file pins the rule for greatest and least over a LowCardinality
// argument.
//
// Before the rule, chgen gave greatest(lcn) the type Nullable(String).
// The server gives LowCardinality(Nullable(String)). That was a SILENTLY
// WRONG TYPE, not a refusal. The current registry grammar fuzz oracle found it.
//
// The cause: greatest and least have the class wrapperAggregate, and a
// true aggregate always drops the LowCardinality wrapper. greatest and
// least are scalar, thus they need an exception.
//
// Every expected value below was measured on ClickHouse 25.8.29.51
// through the HTTP interface, against real columns of a real table,
// never over literals, because the server folds constants.
const lowCardinalityGreatestLeastSchema = `
CREATE TABLE t (
    lc   LowCardinality(String),
    lcn  LowCardinality(Nullable(String)),
    lci  LowCardinality(Int64),
    lcni LowCardinality(Nullable(Int64)),
    s    String,
    sn   Nullable(String),
    i    Int64
);
`

// TestGreatestLeastKeepsLowCardinalityAtArityOne locks the positions
// where the server KEEPS the wrapper. ONE argument keeps it, for every
// inner type.
//
// Measured on ClickHouse 25.8.29.51, for example:
//
//	SELECT toTypeName(greatest(lcn)) FROM t -> LowCardinality(Nullable(String))
//	SELECT toTypeName(least(lc)) FROM t     -> LowCardinality(String)
func TestGreatestLeastKeepsLowCardinalityAtArityOne(t *testing.T) {
	schema, err := schemaFromDDLErr(t, lowCardinalityGreatestLeastSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	cases := []struct{ expr, want string }{
		{"greatest(lc)", "LowCardinality(String)"},
		{"greatest(lcn)", "LowCardinality(Nullable(String))"},
		{"greatest(lci)", "LowCardinality(Int64)"},
		{"greatest(lcni)", "LowCardinality(Nullable(Int64))"},
		{"least(lc)", "LowCardinality(String)"},
		{"least(lcn)", "LowCardinality(Nullable(String))"},
		{"least(lci)", "LowCardinality(Int64)"},
		{"least(lcni)", "LowCardinality(Nullable(Int64))"},
	}
	for _, testCase := range cases {
		got, err := inferSelectItemCHType(t, schema, "SELECT "+testCase.expr+" AS a FROM t")
		if err != nil {
			t.Errorf("%s: error = %v, want %q", testCase.expr, err, testCase.want)
			continue
		}
		if got != testCase.want {
			t.Errorf("%s: CH type = %q, want %q", testCase.expr, got, testCase.want)
		}
	}
}

// TestGreatestLeastDropsLowCardinalityAtArityTwo locks the positions
// where the server DROPS the wrapper. Two or more arguments drop it, for
// every inner type and for every mix of arguments. These cases are the
// reason the exception must stay at arity 1.
//
// Measured on ClickHouse 25.8.29.51, for example:
//
//	SELECT toTypeName(greatest(lc, lc)) FROM t   -> String
//	SELECT toTypeName(greatest(lcn, sn)) FROM t  -> Nullable(String)
func TestGreatestLeastDropsLowCardinalityAtArityTwo(t *testing.T) {
	schema, err := schemaFromDDLErr(t, lowCardinalityGreatestLeastSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	cases := []struct{ expr, want string }{
		{"greatest(lc, lc)", "String"},
		{"greatest(lcn, lcn)", "Nullable(String)"},
		{"greatest(lci, lci)", "Int64"},
		{"greatest(lcni, lcni)", "Nullable(Int64)"},
		{"greatest(lc, s)", "String"},
		{"greatest(lcn, s)", "Nullable(String)"},
		{"greatest(lcn, sn)", "Nullable(String)"},
		{"greatest(lci, i)", "Int64"},
		{"greatest(lc, lcn)", "Nullable(String)"},
		{"greatest(lci, lcni)", "Nullable(Int64)"},
		{"greatest(lc, lc, lc)", "String"},
		{"greatest(lci, lci, lci)", "Int64"},
		{"least(lc, lc)", "String"},
		{"least(lcn, lcn)", "Nullable(String)"},
		{"least(lci, lci)", "Int64"},
		{"least(lcni, lcni)", "Nullable(Int64)"},
		{"least(lc, s)", "String"},
		{"least(lcn, sn)", "Nullable(String)"},
		{"least(lc, lcn)", "Nullable(String)"},
		{"least(lc, lc, lc)", "String"},
	}
	for _, testCase := range cases {
		got, err := inferSelectItemCHType(t, schema, "SELECT "+testCase.expr+" AS a FROM t")
		if err != nil {
			t.Errorf("%s: error = %v, want %q", testCase.expr, err, testCase.want)
			continue
		}
		if got != testCase.want {
			t.Errorf("%s: CH type = %q, want %q", testCase.expr, got, testCase.want)
		}
	}
}

// TestAggregatesDropLowCardinality shows that a true aggregate drops the
// wrapper at every arity. The exception for greatest and least must not
// reach these functions.
//
// Measured on ClickHouse 25.8.29.51:
//
//	SELECT toTypeName(max(lc)) FROM t        -> String
//	SELECT toTypeName(min(lcn)) FROM t       -> Nullable(String)
//	SELECT toTypeName(any(lc)) FROM t        -> String
//	SELECT toTypeName(argMax(lc, i)) FROM t  -> String
func TestAggregatesDropLowCardinality(t *testing.T) {
	schema, err := schemaFromDDLErr(t, lowCardinalityGreatestLeastSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	cases := []struct{ expr, want string }{
		{"max(lc)", "String"},
		{"min(lcn)", "Nullable(String)"},
		{"any(lc)", "String"},
		{"anyLast(lcn)", "Nullable(String)"},
		{"argMax(lc, i)", "String"},
	}
	for _, testCase := range cases {
		got, err := inferSelectItemCHType(t, schema, "SELECT "+testCase.expr+" AS a FROM t")
		if err != nil {
			t.Errorf("%s: error = %v, want %q", testCase.expr, err, testCase.want)
			continue
		}
		if got != testCase.want {
			t.Errorf("%s: CH type = %q, want %q", testCase.expr, got, testCase.want)
		}
	}
}
