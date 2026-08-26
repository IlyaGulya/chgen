package engine

// This file holds the regression tests for the regression.
//
// the regression adjudicated the 11 v3 blind43 signatures that the union cell of
// testdata/oracle-baseline.json had accepted. A blind43 signature is a
// case where chgen answers a TYPE for an expression that the server refuses
// with Code 43, ILLEGAL_TYPE_OF_ARGUMENT. This is the most costly wrong
// answer the tool can give: silent, not loud.
//
// Seven signatures are fixed. chgen now refuses the same expressions that
// the server refuses. The baseline retired the signatures after a live check.
// This file keeps a refusal test for each reproducible expression. It also
// keeps an accepted control, so a broken test fixture cannot give a false pass.
//
// See docs/v2-... (not applicable here; this is the public chgen repo) and
// the regression report for the full measurement. Five signatures stay
// because no direct Code 43 expression is available for the current fixture:
// v3-fn-least, v3-fn-median, v3-fn-uniqCombined64,
// v3-window-last_value, and v3-window-leadInFrame.

import "testing"

// unionRetirementSchema is oracleSchemaDDL, renamed to "probe" so that
// inferTestExprType (argument_domain_test.go), which hard-codes the
// table name "probe", reads the real table instead of failing with "table
// probe is not present" and reporting every case as a false refusal.
const unionRetirementSchema = `
CREATE TABLE probe (
    i8   Int8,
    i16  Int16,
    i32  Int32,
    i64  Int64,
    u8   UInt8,
    u16  UInt16,
    u32  UInt32,
    u64  UInt64,
    f32  Float32,
    f64  Float64,
    dec  Decimal(18, 4),
    b    Bool,
    s    String,
    fs   FixedString(8),
    d    Date,
    dt   DateTime,
    dt64 DateTime64(3),
    ni32 Nullable(Int32),
    nf64 Nullable(Float64),
    ns   Nullable(String),
    arr_i Array(Int32),
    arr_s Array(String),
    m    Map(String, Int64),
    lc   LowCardinality(String),
    lcn  LowCardinality(Nullable(String)),
    e8   Enum8('a' = 1, 'zz' = 2),
    e16  Enum16('x' = 1, 'y' = 2),
    uid  UUID,
    ip4  IPv4,
    ip6  IPv6,
    i128 Int128,
    u128 UInt128,
    i256 Int256,
    u256 UInt256,
    d32  Decimal32(4),
    d64s Decimal64(4),
    d128 Decimal128(4),
    tup  Tuple(Int32, String),
    dtz  DateTime('UTC'),
    dtz64 DateTime64(6, 'UTC'),
    dt32 Date32,
    dec256 Decimal256(4),
    sagg SimpleAggregateFunction(sum, Int64),
    agg  AggregateFunction(uniq, UInt64),
    ni64 Nullable(Int64),
    arr_i64 Array(Int64),
    arr_n Array(Nullable(Int32)),
    m_is Map(Int32, String),
    nd   Nullable(Date),
    ndt  Nullable(DateTime),
    fs16 FixedString(16),
    nu64 Nullable(UInt64),
    ndec Nullable(Decimal(18, 4)),
    aggif AggregateFunction(sumIf, Int32, UInt8)
) ENGINE = MergeTree ORDER BY tuple()
`

func unionRetirementTestSchema(t *testing.T) *Schema {
	t.Helper()
	schema, err := schemaFromDDLErr(t, unionRetirementSchema)
	if err != nil {
		t.Fatalf("parse the current fixture schema as probe: %v", err)
	}
	return schema
}

// TestUnionRetirementControlAccepted is the harness control. If this case
// ever reports an error, the table name or the schema went stale, and every
// refusal test below would then pass for the WRONG reason: a missing table,
// not a domain check.
func TestUnionRetirementControlAccepted(t *testing.T) {
	schema := unionRetirementTestSchema(t)
	got, err := inferTestExprType(t, schema, "i32 + i32")
	if err != nil {
		t.Fatalf("control expression must be accepted, harness is broken: %v", err)
	}
	if got != "Int64" {
		t.Fatalf("control expression: got %s, want Int64", got)
	}
}

// TestUnionRetirementArgMinFixed pins the regression finding for
// v3-fn-argMin: chgen=Int64.
//
// Measured live on ClickHouse 25.8.29.51 against the current fixture (agg is
// AggregateFunction(uniq, UInt64)):
//
//	SELECT toTypeName(argMin(i64, agg)) FROM probe
//	Code: 43. Illegal type AggregateFunction(uniq, UInt64) of second
//	argument of aggregate function argMin because the values of that
//	data type are not comparable. (ILLEGAL_TYPE_OF_ARGUMENT)
//
//	SELECT argMin(i64, agg) FROM probe
//	same Code: 43, confirmed by execution and not only by analysis.
//
// chgen now refuses this call too: comparableValueArgumentDomain, pinned to
// argMin's second argument (registry.go, domainArgs: []int{1}), rejects an
// AggregateFunction state as the comparison key. Before that domain existed
// chgen answered Int64, the first argument's type, and gave a silently wrong
// type for a call the server refuses. This is the exact shape the retired
// v3-fn-argMin signature named.
func TestUnionRetirementArgMinFixed(t *testing.T) {
	schema := unionRetirementTestSchema(t)
	_, err := inferTestExprType(t, schema, "argMin(i64, agg)")
	if err == nil {
		t.Fatalf("chgen must refuse argMin(i64, agg): the server refuses this pair with " +
			"Code 43 because an AggregateFunction state is not comparable, and an accepted " +
			"answer here is the exact silently wrong type that v3-fn-argMin named")
	}
}

// TestUnionRetirementToDecimalFixed pins the regression findings for
// v3-fn-toDecimal128, v3-fn-toDecimal256 and v3-fn-toDecimal64.
//
// Measured live on ClickHouse 25.8.29.51 against the current fixture: toTypeName
// LIES for all three calls below (it answers a Decimal type), but the real
// execution refuses with Code 43 in every case:
//
//	SELECT toTypeName(toDecimal64(agg, 2)) FROM probe   -> Decimal(18, 2) (WRONG, analysis only)
//	SELECT toDecimal64(agg, 2) FROM probe
//	Code: 43. Illegal type AggregateFunction(uniq, UInt64) of argument of
//	function toDecimal64 (ILLEGAL_TYPE_OF_ARGUMENT), confirmed by execution.
//
//	SELECT toDecimal128(agg, 3) FROM probe   same Code: 43
//	SELECT toDecimal256(agg, 1) FROM probe   same Code: 43
//
// chgen now refuses all three: decimalConstructorArgumentDomain accepts
// only an integer, a float, a Decimal, a String or a FixedString, and an
// AggregateFunction state matches none of those, so inference refuses
// instead of answering the Decimal type that toTypeName alone would suggest.
func TestUnionRetirementToDecimalFixed(t *testing.T) {
	schema := unionRetirementTestSchema(t)
	cases := []string{
		"toDecimal128(agg, 3)",
		"toDecimal256(agg, 1)",
		"toDecimal64(agg, 2)",
	}
	for _, expr := range cases {
		t.Run(expr, func(t *testing.T) {
			_, err := inferTestExprType(t, schema, expr)
			if err == nil {
				t.Fatalf("chgen must refuse %s: the server refuses this call at EXECUTION with "+
					"Code 43 (toTypeName alone answers a Decimal type and lies about it), and an "+
					"accepted answer here is the exact silently wrong type that this signature named", expr)
			}
		})
	}
}

// TestUnionRetirementLaterFixes checks the three signatures that later work
// retired. The tests use the exact bad argument shapes from the issue records.
func TestUnionRetirementLaterFixes(t *testing.T) {
	schema := unionRetirementTestSchema(t)
	cases := []string{
		"equals(arraySlice(arr_i, 1), arrayDistinct(arr_s))",
		"greaterOrEquals(arraySlice(arr_i, 1), arrayDistinct(arr_s))",
		"quantileStateIf(0.5)(u64, s)",
	}
	for _, expr := range cases {
		t.Run(expr, func(t *testing.T) {
			if _, err := inferTestExprType(t, schema, expr); err == nil {
				t.Fatalf("chgen must refuse %s because ClickHouse 25.8.29.51 refuses this call with Code 43", expr)
			}
		})
	}
}
