package typeboundary

// This file holds the fixture schema and the query builder that every
// catalog cell uses.
//
// Every cell reads REAL TABLE COLUMNS, never a literal. A literal is folded
// by ClickHouse's constant analysis before the type rule under test ever
// runs, so a literal probe measures the constant folder, not the boundary
// (see docs/wrapper-grid.md). The probe
// therefore creates one fixture table, `chgen_probe_t`, with one seeded row,
// and every cell selects a plain column reference out of it, exactly the way
// the type oracle's own fixture does.

// SchemaDDL creates the probe fixture table. The table name is namespaced
// (chgen_probe_t, not `t`) so a probe run can share a database with other
// chgen tooling without a collision.
const SchemaDDL = `CREATE TABLE IF NOT EXISTS chgen_probe_t (
	i8 Int8, i16 Int16, i32 Int32, i64 Int64, u8 UInt8, u32 UInt32, u64 UInt64,
	f32 Float32, f64 Float64,
	dec Decimal(18, 4),
	str String, fs FixedString(4),
	arr_i32 Array(Int32), arr_n_i32 Array(Nullable(Int32)), arr_str Array(String),
	nul_i32 Nullable(Int32),
	lc_str LowCardinality(String),
	safn_i32 SimpleAggregateFunction(anyLast, Nullable(Int32)),
	safn_u64 SimpleAggregateFunction(anyLast, Nullable(UInt64)),
	saf_narr SimpleAggregateFunction(anyLast, Array(Nullable(Int32))),
	safarr_narr SimpleAggregateFunction(anyLast, Array(Array(Nullable(Int32)))),
	dt DateTime, dt64 DateTime64(3), dte Date,
	en Enum8('a' = 1, 'b' = 2),
	uid UUID,
	ip4 IPv4
) ENGINE = AggregatingMergeTree ORDER BY tuple()`

// SeedRowDML inserts the one fixture row that every cell reads. Most values
// are ordinary and do not use a value edge. The nullable arrays hold a real
// NULL because the 24.8 cityHash64 boundary occurs only when execution reads
// that value. All other values keep the probe about the type rule.
const SeedRowDML = `INSERT INTO chgen_probe_t SELECT
	1, 2, 3, 4, 5, 6, 7,
	1.5, 2.5,
	10.25,
	'hello', 'abcd',
	[1,2,3], [1,NULL,2], ['a','b'],
	42,
	'x',
	3,
	4,
	[1,NULL,2],
	[[1,NULL,2]],
	'2024-01-01 00:00:00', '2024-01-01 00:00:00.123', '2024-01-01',
	'a',
	'12345678-1234-1234-1234-123456789abc',
	'1.2.3.4'
`

// DropDDL removes the probe fixture table. A probe run that creates its own
// database (see internal/tooling/cmd/probe) does not strictly need this, but a probe run
// pointed at a shared database must clean up after itself.
const DropDDL = `DROP TABLE IF EXISTS chgen_probe_t`

// exprToQuery wraps one expression into the fixed query shape every cell
// uses: the analysed type, PLUS an execution witness.
//
// toTypeName alone answers from ANALYSIS and never runs the expression, so a
// type the server would refuse to compute at run time can be reported as if
// it were safe (see the ignore()/toStartOfInterval comment in
// typeoracle_fuzz_test.go, which is the origin of this exact technique
// and the concrete case that motivated it: toTypeName(toStartOfInterval(d,
// INTERVAL 1 HOUR)) answers DateTime while the bare expression is Code 43).
// ignore(e) forces the expression to run over the seeded row.
//
// Each cell is sent alone, one expression per query. A batch would let a
// second expression's own refusal (for example a NOT_AN_AGGREGATE mismatch
// between an aggregate and a bare column sharing one SELECT) land on the
// wrong cell; sending one expression per round trip removes that class of
// harness fault entirely, at the cost of more round trips, which a fixed,
// small catalog can afford.
func exprToQuery(expr string) string {
	return "SELECT toTypeName(" + expr + "), ignore(" + expr + ") FROM chgen_probe_t"
}
