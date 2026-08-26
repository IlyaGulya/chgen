package engine

import (
	"strings"
	"testing"
)

// A combinator must not widen the argument domain of its base aggregate.
// The bare aggregate refuses an argument type that is outside its measured
// accept-set, and every combinator form refuses the same argument.
//
// Measured on ClickHouse 25.8.29.51 (HTTP, real table columns, never
// literals, because the server folds constants). The bare form and every
// combinator form give the same Code: 43 (ILLEGAL_TYPE_OF_ARGUMENT):
//
//	sum(s)              sumState(s)  sumIf(s, b)  sumArray(arr_s)
//	                    sumOrNull(s) sumOrDefault(s) sumSimpleState(s)
//	                    sumResample(0, 10, 1)(s, i32)
//	sum(d)              sumState(d)   and the same set
//	sum(u)              sumState(u)   and the same set
//	avg(e8)             avgState(e8)  and the same set
//	quantile(0.5)(s)    quantileState(0.5)(s)   and the same set
//	quantile(0.5)(e8)   quantileState(0.5)(e8)  and the same set
//	quantile(0.5)(d32)  quantileState(0.5)(d32) and the same set
//	groupBitOr(f64)     groupBitOrState(f64)    and the same set
//
// The sweep found NO case where the server accepts a combinator form of an
// argument that it refuses in the bare form. Thus the routing is safe: it
// cannot refuse a call that the server would run.

// combinatorDomainSchema holds the columns that the domain cases need. The
// shared combinatorSchema has no Enum, no Date, no Date32 and no UUID
// column, and it is pinned by the result-type tests, thus this file brings
// its own table.
const combinatorDomainSchema = `
CREATE TABLE t (
    i32    Int32,
    f64    Float64,
    dec    Decimal(18, 4),
    b      Bool,
    s      String,
    d      Date,
    d32    Date32,
    dt     DateTime,
    e8     Enum8('a' = 1, 'b' = 2),
    u      UUID,
    arr_s  Array(String),
    arr_i32 Array(Int32),
    arr_e8 Array(Enum8('a' = 1, 'b' = 2))
);
CREATE TABLE agg (
    qsd AggregateFunction(quantile(0.5), Date),
    ssi AggregateFunction(sum, Int32)
);
`

// TestCombinatorRefusesArgumentOutsideBaseDomain locks the refusals. Before
// the routing, each of these calls gave a type although the server answers
// Code: 43.
func TestCombinatorRefusesArgumentOutsideBaseDomain(t *testing.T) {
	schema, err := schemaFromDDLErr(t, combinatorDomainSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	cases := []struct{ expr, wantIn string }{
		// -State over an argument that the base refuses.
		{"sumState(s)", "String"},
		{"sumState(d)", "Date"},
		{"sumState(u)", "UUID"},
		{"avgState(e8)", "Enum8"},
		{"quantileState(0.5)(s)", "String"},
		{"quantileState(0.5)(e8)", "Enum8"},
		{"quantileState(0.5)(d32)", "Date32"},
		{"medianState(0.5)(s)", "String"},
		{"groupBitOrState(f64)", "Float64"},
		{"groupBitOrState(dec)", "Decimal"},
		// The other combinators are blind in the same way.
		{"sumIf(s, b)", "String"},
		{"groupBitOrIf(f64, b)", "Float64"},
		{"sumOrNull(s)", "String"},
		{"sumOrDefault(s)", "String"},
		{"sumArray(arr_s)", "String"},
		{"quantileArray(0.5)(arr_e8)", "Enum8"},
		{"sumResample(0, 10, 1)(s, i32)", "String"},
		{"sumSimpleState(s)", "String"},
		{"groupBitOrSimpleState(f64)", "Float64"},
		{"avgOrNull(e8)", "Enum8"},
		{"quantileOrNull(0.5)(e8)", "Enum8"},
	}
	for _, testCase := range cases {
		got, err := combinatorCHType(t, schema, "t", testCase.expr)
		if err == nil {
			t.Errorf("%s: CH type = %q, want a refusal", testCase.expr, got)
			continue
		}
		if !strings.Contains(err.Error(), "does not accept an argument of type") {
			t.Errorf("%s: refusal %v does not name the argument domain", testCase.expr, err)
			continue
		}
		if !strings.Contains(err.Error(), testCase.wantIn) {
			t.Errorf("%s: refusal %v does not name the type %s", testCase.expr, err, testCase.wantIn)
		}
	}
}

// TestCombinatorKeepsArgumentInsideBaseDomain is the negative control. The
// server accepts every call below, thus chgen must give each one a type.
// A refusal here is an over-refusal, which breaks a query that works.
func TestCombinatorKeepsArgumentInsideBaseDomain(t *testing.T) {
	schema, err := schemaFromDDLErr(t, combinatorDomainSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	cases := []struct{ expr, want string }{
		// An argument inside the domain of the base aggregate.
		{"sumState(i32)", "AggregateFunction(sum, Int32)"},
		{"sumState(e8)", "AggregateFunction(sum, Enum8('a' = 1, 'b' = 2))"},
		{"sumIf(i32, b)", "Int64"},
		{"sumOrNull(i32)", "Nullable(Int64)"},
		{"sumOrDefault(i32)", "Int64"},
		{"sumArray(arr_i32)", "Int64"},
		{"sumResample(0, 10, 1)(i32, i32)", "Array(Int64)"},
		{"sumSimpleState(i32)", "SimpleAggregateFunction(sum, Int64)"},
		{"quantileState(0.5)(i32)", "AggregateFunction(quantile(0.5), Int32)"},
		{"quantileState(0.5)(d)", "AggregateFunction(quantile(0.5), Date)"},
		{"quantileState(0.5)(dt)", "AggregateFunction(quantile(0.5), DateTime)"},
		{"avgOrNull(f64)", "Nullable(Float64)"},
		{"avgState(dec)", "AggregateFunction(avg, Decimal(18, 4))"},
		{"groupBitOrState(b)", "AggregateFunction(groupBitOr, Bool)"},
		// A base aggregate with no domain accepts every type, thus the
		// routing must not add a constraint of its own.
		{"maxState(s)", "AggregateFunction(max, String)"},
		{"anyState(u)", "AggregateFunction(any, UUID)"},
		{"uniqState(s)", "AggregateFunction(uniq, String)"},
		{"countState(s)", "AggregateFunction(count, String)"},
		{"groupArrayState(s)", "AggregateFunction(groupArray, String)"},
		{"maxIf(s, b)", "String"},
		{"maxOrNull(s)", "Nullable(String)"},
	}
	for _, testCase := range cases {
		got, err := combinatorCHType(t, schema, "t", testCase.expr)
		if err != nil {
			t.Errorf("%s: error = %v, want %q", testCase.expr, err, testCase.want)
			continue
		}
		if got != testCase.want {
			t.Errorf("%s: CH type = %q, want %q", testCase.expr, got, testCase.want)
		}
	}
}

// TestMergeCombinatorSkipsTheArgumentDomain is the other over-refusal
// guard. The argument of a -Merge call is an AggregateFunction state and
// not a data value, thus no data domain accepts it. The domain check must
// skip -Merge, or every -Merge call over a constrained base would refuse.
//
// Measured: quantileMerge(0.5)(qsd) is Date and sumMerge(ssi) is Int64,
// with qsd an AggregateFunction(quantile(0.5), Date) column and ssi an
// AggregateFunction(sum, Int32) column.
func TestMergeCombinatorSkipsTheArgumentDomain(t *testing.T) {
	schema, err := schemaFromDDLErr(t, combinatorDomainSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	cases := []struct{ expr, want string }{
		{"quantileMerge(0.5)(qsd)", "Date"},
		{"sumMerge(ssi)", "Int64"},
	}
	for _, testCase := range cases {
		got, err := combinatorCHType(t, schema, "agg", testCase.expr)
		if err != nil {
			t.Errorf("%s: error = %v, want %q", testCase.expr, err, testCase.want)
			continue
		}
		if got != testCase.want {
			t.Errorf("%s: CH type = %q, want %q", testCase.expr, got, testCase.want)
		}
	}
}
