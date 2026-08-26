package engine

// This file holds the fixture schema of the type oracle. It has NO build
// tag on purpose, although the oracle itself is behind the fuzzoracle
// tag.
//
// An untagged _test.go file is compiled in the default `go test ./...`
// run AND in the tagged run. Thus the completeness gate, which must run
// in the default build, can read the same fixture that the fuzzer uses.
// A copy of the schema beside the gate would drift from this one, and
// that duplication is the defect this move removes.

const currentOracleFixtureID = "current-fixture"

// oracleSchemaDDL is the schema for the current type-oracle fixture.
// The report records a hash of this DDL and the observed fixture shape.
//
// The nine added columns give the generator a legal operand where the v2
// fixture had none, and they widen the ILLEGAL half of a domain as well.
// Each column was created and read back on 25.8.29.51 before it was written
// here. The server reported these column types:
//
//	ni64     Nullable(Int64)
//	arr_i64  Array(Int64)
//	arr_n    Array(Nullable(Int32))
//	m_is     Map(Int32, String)
//	nd       Nullable(Date)
//	ndt      Nullable(DateTime)
//	fs16     FixedString(16)
//	nu64     Nullable(UInt64)
//	ndec     Nullable(Decimal(18, 4))
//
// LowCardinality(Int32) was measured and REFUSED. The server answers code
// 455, SUSPICIOUS_TYPE_FOR_LOW_CARDINALITY, unless the setting
// allow_suspicious_low_cardinality_types is on. The column is therefore
// absent, instead of carried with a special setting that production code
// does not use.
//
// aggif holds a state that an -If form built, AggregateFunction(sumIf,
// Int32, UInt8). agg above holds a state that the BARE uniq form built,
// thus agg only ever reaches uniqMerge. The -IfMerge and -IfMergeState
// suffixes need a state built by an -If form, so a second column is
// needed and one -If base is enough: sumIfMerge(aggif) was measured on
// 25.8.29.51 to give Int64, and every other base answers Code 43,
// "different aggregate function: sumIf instead", in the same way that
// eleven of the thirteen bases fail plain -Merge against agg. Thus sum
// is the only base that reaches -IfMerge and -IfMergeState here, one
// level deeper than the uniq-only limit that agg already carries.
//
// The eleven columns below close the regression. Before this change no
// fixture column held Variant, Dynamic, JSON, Nested, a NAMED Tuple, a
// Map whose value is a container, or a Decimal at an extreme scale.
// A type that no fixture column holds cannot be reached by any
// generated expression, thus the oracle was blind to each of these
// families by construction. A green
// report over such a family was evidence of absence, not of
// correctness.
//
// generatedTypeSpecimenColumns appends the accepted entries from the pinned
// real-column catalog. The catalog probes every server type family with a
// create, seed, analysis, and execution witness. Its generated columns use
// the observed canonical type, so aliases such as DateTime32 do not give the
// chgen catalog a type name that the live table does not have. The seed below
// names only the hand-written columns. ClickHouse fills each generated
// specimen with its type default, which is still a real column value and is
// not a folded literal.
//
// Each column was created, seeded and read back on 25.8.29.51 through
// the plain HTTP interface before it was written here, with NO server
// setting beyond the defaults. The measurement result:
//
//	variant  Variant(Int32, String)          -- no setting needed
//	dyn      Dynamic                         -- no setting needed
//	js       JSON                            -- no setting needed
//	nst_a    Nested(a Int32, b String)       -- ordinary DDL, no setting
//	ntup     Tuple(x Int32, y String)        -- a NAMED Tuple
//	m_arr    Map(String, Array(Int32))       -- container value
//	m_tup    Map(String, Tuple(Int32, String)) -- container value
//	dec76_0  Decimal(76, 0)                  -- the widest precision
//	dec76_76 Decimal(76, 76)                 -- the widest scale
//
// ClickHouse 25.8 needed none of allow_experimental_variant_type,
// allow_experimental_dynamic_type or allow_experimental_json_type: a
// plain CREATE TABLE with a Memory-engine probe table accepted all
// three types outright. The oracle harness sets no such flag, and none
// is needed on this server, so the columns are safe to add without
// touching the harness. This was measured, not assumed: an earlier
// ClickHouse series gated these types behind the settings above, and a
// silent carry-over of that assumption would have been wrong here.
//
// Nested uses its runtime Array(Tuple(...)) shape in chgen's type model.
// The live fixture guard compares this parsed shape with the executed server
// answer for nst_a. This check prevents a bare Nested shape from returning.
//
// The server SPLITS a Nested column into physical sub-columns, measured
// on 25.8.29.51: system.columns reports nst_a.a Array(Int32) and
// nst_a.b Array(String), not one nst_a Nested(...) row. chgen's schema
// parser does NOT perform this split: parseCreateTable reads one
// ColumnDef per DDL entry, thus nst_a stays ONE column of type
// Array(Tuple(a Int32, b String)) in chgen's own catalog
// (TestWidenedFixtureColumnsAreReachable checks the name "nst_a", not
// "nst_a.a" or "nst_a.b", for exactly this reason).
// A caller who runs `DESCRIBE t` against the live table and expects the
// two sub-column names chgen never produces would be surprised by this
// gap; it is recorded here, not fixed, because gotype.go and schema.go
// belong to a separate change.
//
// ntup is a SECOND Tuple column, distinct from the existing positional
// tup column: the fixture needs one column of each shape so that a
// generated expression can draw either. Measured on 25.8.29.51:
// toTypeName(ntup) answers "Tuple(\n    x Int32,\n    y String)", with
// the field list on its own indented lines, while chgen renders a
// Tuple type on one line with no such break. This is a real,
// measured difference in the STRING that names the type, and any
// MISMATCH signature this raises against ntup is a harness rendering
// gap, not a wrong chgen type; see the finding recorded in the top
// commit that adds these columns.
const oracleSchemaPrefix = `
CREATE TABLE t (
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
    aggif AggregateFunction(sumIf, Int32, UInt8),
    variant  Variant(Int32, String),
    dyn      Dynamic,
    js       JSON,
    nst_a    Nested(a Int32, b String),
    ntup     Tuple(x Int32, y String),
    m_arr    Map(String, Array(Int32)),
    m_tup    Map(String, Tuple(Int32, String)),
    dec76_0  Decimal(76, 0),
    dec76_76 Decimal(76, 76),
`

const oracleSchemaSuffix = `
) ENGINE = MergeTree ORDER BY tuple()
`

const oracleSchemaDDL = oracleSchemaPrefix + generatedTypeSpecimenColumns + oracleSchemaSuffix

// oracleSeedRow seeds the current fixture. It uses INSERT ... SELECT because
// the agg column holds an AggregateFunction state. A VALUES literal cannot
// write that state.
//
// aggif is seeded with an explicit toInt32/toUInt8 cast on the two
// sumIfState arguments. Without the cast, sumIfState(3, 1) infers UInt8
// for both literals and the server then answers Code 70,
// CANNOT_CONVERT_TYPE, "Conversion from AggregateFunction(sumIf, UInt8,
// UInt8) to AggregateFunction(sumIf, Int32, UInt8) is not supported",
// measured on 25.8.29.51. The row holds the condition true (1), so the
// state carries a non-empty sum and the column is read back, not merely
// typed: toTypeName(aggif) gives AggregateFunction(sumIf, Int32, UInt8)
// and sumIfMerge(aggif) gives 3.
//
// The eleven values that seed the columns the regression added were checked
// with a real read-back on 25.8.29.51, not only a successful INSERT:
//
//	SELECT * FROM t FORMAT Vertical
//
// gave back variant=3, dyn=3, js={"a":1}, nst_a.a=[1,2], nst_a.b=['a','b'],
// ntup=(1,'a'), m_arr={'k':[1,2]}, m_tup={'k':(1,'a')}, dec76_0=1 and
// dec76_76 rounded to the column's own scale. variant and dyn need an
// explicit CAST(3, 'Int32') on the source literal: a bare integer
// literal is UInt8, and inserting a UInt8 into a Variant or a Dynamic
// column that only ever saw Int32 through this row would otherwise seed
// the wrong held branch (measured Code 70, CANNOT_CONVERT_TYPE, for the
// Variant column: "Conversion to Variant allowed only for types from
// this Variant").
const oracleSeedRow = `INSERT INTO t (i8,i16,i32,i64,u8,u16,u32,u64,f32,f64,dec,b,s,fs,d,dt,dt64,ni32,nf64,ns,arr_i,arr_s,m,lc,lcn,e8,e16,uid,ip4,ip6,i128,u128,i256,u256,d32,d64s,d128,tup,dtz,dtz64,dt32,dec256,sagg,agg,ni64,arr_i64,arr_n,m_is,nd,ndt,fs16,nu64,ndec,aggif,variant,dyn,js,nst_a.a,nst_a.b,ntup,m_arr,m_tup,dec76_0,dec76_76) SELECT 3, 3, 3, 3, 5, 5, 5, 5, 1.5, 2.5, 1.2345, true, 'abc', 'abcdefgh', '2024-01-02', '2024-01-02 03:04:05', '2024-01-02 03:04:05.123', 7, 3.25, 'x', [1,2], ['a'], map('k', 1), 'lc', 'v', 'a', 'x', '61f0c404-5cb3-11e7-907b-a6006ad3dba0', '1.2.3.4', '::1', 3, 3, 3, 3, 1.5, 1.5, 1.5, (1, 'a'), '2024-01-02 03:04:05', '2024-01-02 03:04:05.123456', toDate32('2024-01-02'), toDecimal256(1.5, 4), 3, uniqState(toUInt64(1)), 3, [1,2], [1, NULL], map(1, 'a'), '2024-01-02', '2024-01-02 03:04:05', 'abcdefghabcdefgh', 5, 1.2345, CAST(sumIfState(toInt32(3), toUInt8(1)), 'AggregateFunction(sumIf, Int32, UInt8)'), CAST(3 AS Int32), CAST(3 AS Int32), '{"a":1}', [1,2], ['a','b'], (1, 'a'), map('k', [1,2]), map('k', (1, 'a')), toDecimal256(1.5, 0), toDecimal256(0.12345, 76)`
