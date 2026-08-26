package engine

import (
	"fmt"
	"strings"
)

// This file holds the fixture of the wrapper grid and the closed wrapper
// alphabet that the grid enumerates.
//
// It has NO build tag, for the same reason that oraclefixture_test.go has
// none: the fixture must be readable by the default `go test ./...` run,
// so that the shape gates of the grid can run without a server, while the
// measuring test itself stays behind the fuzzoracle tag.
//
// The grid does NOT reuse the oracle fixture of oraclefixture_test.go.
// That fixture is inside a byte-identity fingerprint, and it carries one
// column for each type that the RANDOM walk needed. The grid needs the
// opposite shape: one column for every (wrapper, base type) pair of a
// CLOSED alphabet, so that a cell can be addressed by name instead of
// found by chance.

// gridWrapper is one member of the closed wrapper alphabet.
//
// The alphabet is closed on purpose. A grid whose alphabet can grow by
// accident is not an enumeration any more, and the count of cells would
// stop being a reviewable number.
type gridWrapper struct {
	// key is the short, stable name of the wrapper. It goes into the
	// golden file and into every column name, thus it must never
	// change once a golden is committed.
	key string
	// format is the printf format that wraps a base type name.
	format string
}

// gridWrappers is the closed wrapper alphabet of the grid. The order is
// fixed and is part of the reproducibility contract: the golden file is
// written in this order.
var gridWrappers = []gridWrapper{
	{key: "bare", format: "%s"},
	{key: "lc", format: "LowCardinality(%s)"},
	{key: "n", format: "Nullable(%s)"},
	{key: "lcn", format: "LowCardinality(Nullable(%s))"},
	{key: "saf", format: "SimpleAggregateFunction(anyLast, %s)"},
	{key: "safn", format: "SimpleAggregateFunction(anyLast, Nullable(%s))"},
	// A COMPOSITE, NON-Nullable inner type under the marker. The regression
	// added it, and it is the one letter that the alphabet needed.
	//
	// WHY IT EARNS ITS PLACE. The measured marker rule is that the
	// marker survives a value read only over a BARE SCALAR inner type.
	// The competing rule, which the code held until the regression, is that
	// the marker survives when the inner type is not Nullable. The two
	// agree on `saf` and they agree on `safn`, thus NO cell of the old
	// alphabet could separate them. They disagree exactly over a
	// composite inner type that carries no Nullable of its own, which
	// is what this letter spells.
	//
	// WHY LowCardinality AND NOT Array, Tuple OR Map. The alphabet
	// already reaches Array, Tuple and Map inners through the `saf`
	// letter over the container bases c_saf_arr, c_saf_tup and
	// c_saf_map, and those cells did report the defect: the four
	// arrayDistinct, arraySort, arraySlice and arrayResize MISMATCH
	// cells of the committed golden are this class. What the alphabet
	// could NOT reach was the value-preserving aggregate family and the
	// case-folding family, because an entry takes at most two
	// representative base types of its domain and a container base is
	// never among the first two that max, any, argMax, lower or upper
	// accept. A LowCardinality inner is composite for the marker rule
	// AND legal for every scalar domain, thus it reaches those families
	// over their own representative bases. No other composite inner
	// does.
	//
	// It also separates the two transports, which is a second rule that
	// the old alphabet could not see. Measured on ClickHouse 25.8.29.51
	// with real columns of a real AggregatingMergeTree table:
	//
	//	max(saflc)    String                  the aggregate drops the LC
	//	lower(saflc)  LowCardinality(String)  case folding keeps the LC
	//
	// A letter that only dropped the marker would record one answer for
	// both and hide that split.
	//
	// The wrapper is legal only over a base that LowCardinality itself
	// accepts; see gridWrapperAccepts.
	{key: "saflc", format: "SimpleAggregateFunction(anyLast, LowCardinality(%s))"},
	// A CONTAINER inner type under the marker. The regression added it.
	//
	// WHY IT EARNS ITS PLACE. The `saf` letter can already SPELL a
	// container inner, because SimpleAggregateFunction accepts every
	// base of the grid and the grid holds the container bases arr, tup
	// and map.
	//
	// HISTORY, and why this paragraph is kept. When the regression added the
	// letter, an entry took its representative bases by POSITION in
	// gridBases order, where the containers are LAST. Thus for every
	// entry whose domain also accepted a scalar, both slots of the `saf`
	// letter went to scalars and the container inner was never measured.
	// The letter was therefore read as the only way to reach a container
	// inner at all. The regression removed that cap: gridRepresentativeCells
	// now chooses per KIND, so the `saf` letter reaches a container inner
	// on its own merits. The letter still earns its place for the reason
	// below, which the cap never governed.
	//
	// This letter gives the container inner its OWN slot. The
	// element type varies instead of the container kind, thus an entry
	// that accepts both a scalar and an array now spends a full cap
	// inside this class rather than losing it to Int32 and UInt64.
	//
	// The rule it separates is the marker rule over a container inner.
	// Every other letter of the alphabet agrees that the marker survives
	// a bare scalar and drops over Nullable. Measured on ClickHouse
	// 25.8.29.51 against a real seeded AggregatingMergeTree column of
	// type SimpleAggregateFunction(anyLast, Array(Int32)):
	//
	//	anySimpleState(x)  SimpleAggregateFunction(any, Array(Int32))
	//	greatest(x)        Array(Int32)
	//	least(x)           Array(Int32)
	//	max(x)             Array(Int32)
	//	arraySort(x)       Array(Int32)
	//	length(x)          UInt64
	//
	// The marker DROPS although the inner type carries no Nullable and
	// no LowCardinality of its own. A rule that keeps the marker unless
	// the inner is Nullable therefore answers wrongly here, and no cell
	// of the old EFFECTIVE census could show it.
	//
	// WHY Array AND NOT Tuple OR Map. An Array inner is legal over EVERY
	// base of the grid, thus this one letter reaches the whole base list
	// and its two slots vary the ELEMENT type. A Tuple or a Map inner
	// needs a fixed second component, which varies two things at once,
	// and its base list is one frozen shape that the existing `saf` over
	// tup and map already spells.
	//
	// TRAP, measured. The element must be the BARE base type. The server
	// refuses Array(LowCardinality(T)) under the marker with Code 36 for
	// EVERY T, not only for String:
	//
	//	SimpleAggregateFunction(anyLast, Array(LowCardinality(String)))
	//	                                                     Code 36
	//	SimpleAggregateFunction(anyLast, Array(LowCardinality(Int32)))
	//	                                                     Code 36
	//
	// Code 36 is the storage-type mismatch: anyLast over that column
	// returns Array(String), which is not the declared storage type.
	// This letter never composes an inner wrapper, thus it never meets
	// that refusal.
	{key: "safarr", format: "SimpleAggregateFunction(anyLast, Array(%s))"},
}

// gridBase is one representative base type of an accepted domain.
//
// The grid carries one or two representatives per domain, never the whole
// type list. Two representatives are enough to separate a rule that reads
// the type from a rule that returns a constant, and the cell count stays
// a number that a reviewer can hold.
type gridBase struct {
	// key is the short, stable name. It becomes part of a column name
	// and of a golden cell address.
	key string
	// chType is the ClickHouse type name.
	chType string
	// seed is the SQL expression that seeds the column. It must be a
	// value that is legal for the type AND ordinary for it: a probe
	// value that is out of the natural range of the function under
	// test turns a type answer into a value error. See the exclusion
	// of Code 6 in wrappergrid_test.go.
	seed string
	// lowCardinality reports whether the server accepts this base type
	// under LowCardinality. It is MEASURED, not assumed: the rejected
	// set is {Decimal, DateTime64, Enum8, Enum16, Array, Tuple, Map}
	// and the server answers Code 43 for it, which is type evidence.
	//
	// Measured on ClickHouse 25.8.29.51 with a real MergeTree column,
	// with allow_suspicious_low_cardinality_types=1:
	//
	//	LowCardinality(Int32)          accepted
	//	LowCardinality(UUID)           accepted
	//	LowCardinality(IPv4)           accepted
	//	LowCardinality(Bool)           accepted
	//	LowCardinality(FixedString(8)) accepted
	//	LowCardinality(Decimal(18,4))  Code 43
	//	LowCardinality(DateTime64(3))  Code 43
	//	LowCardinality(Enum8(...))     Code 43
	//	LowCardinality(Array(Int32))   Code 43
	//	LowCardinality(Tuple(...))     Code 43
	//	LowCardinality(Map(...))       Code 43
	//
	// The server message names "numbers, strings, Date or DateTime",
	// and that message is WRONG: UUID, IPv4, IPv6 and Bool are all
	// accepted. The measurement, not the message, is the rule here.
	lowCardinality bool
	// nullable reports whether the server accepts the base type under
	// Nullable. Array, Tuple and Map are refused with Code 43.
	nullable bool
	// noSimpleAggregate reports that the server REFUSES the base type
	// under SimpleAggregateFunction(anyLast, ...).
	//
	// The flag states the EXCEPTION, not the rule. Acceptance is the
	// rule for every base but one, thus a positive flag would have to
	// be written true on sixteen lines that no measurement disputes,
	// and the one line that matters would be the hardest to see. The
	// zero value therefore means "accepted", which is the measured
	// majority.
	//
	// Measured on ClickHouse 25.8.29.51 by CREATE TABLE of a real
	// column, over EVERY base of the grid. Exactly one base is
	// refused:
	//
	//	SimpleAggregateFunction(anyLast, Array(Nullable(Int32)))
	//	                                            accepted
	//	SimpleAggregateFunction(anyLast, Array(LowCardinality(String)))
	//	                                            Code 36
	//
	// Code 36 is BAD_ARGUMENTS and the message gives the reason: the
	// aggregate anyLast RETURNS Array(String), while the storage type
	// is Array(LowCardinality(String)). The aggregate drops the
	// element LowCardinality, thus the storage type can never match
	// its own return type. The refusal is therefore about the ELEMENT
	// wrapper, and it lands at DDL time before any function runs.
	//
	// A guard that assumed acceptance here would put an illegal column
	// in the fixture, and the whole CREATE TABLE would fail. The grid
	// would then measure nothing at all.
	noSimpleAggregate bool
}

// gridBases holds the representative base types. The order is fixed and
// is part of the reproducibility contract.
//
// Every flag below was measured; none was copied from documentation.
var gridBases = []gridBase{
	// The integer family. Two representatives: a signed narrow type and
	// an unsigned wide one, so that a widening rule shows.
	{key: "i32", chType: "Int32", seed: "3", lowCardinality: true, nullable: true},
	{key: "u64", chType: "UInt64", seed: "5", lowCardinality: true, nullable: true},
	// The float family.
	{key: "f64", chType: "Float64", seed: "2.5", lowCardinality: true, nullable: true},
	// The Decimal family. It is refused under LowCardinality.
	{key: "dec", chType: "Decimal(18, 4)", seed: "1.2345", lowCardinality: false, nullable: true},
	// The string family. Two representatives, because FixedString and
	// String take different paths in several rules.
	//
	// The seed is 'abc' for String. A numeric-looking seed would make a
	// parse function succeed where it must fail, and the grid would then
	// record a value answer as a type answer.
	{key: "s", chType: "String", seed: "'abc'", lowCardinality: true, nullable: true},
	{key: "fs", chType: "FixedString(8)", seed: "'abcdefgh'", lowCardinality: true, nullable: true},
	// The temporal family. Date, DateTime and DateTime64 differ in the
	// LowCardinality answer, thus all three are present.
	{key: "d", chType: "Date", seed: "'2024-01-02'", lowCardinality: true, nullable: true},
	{key: "dt", chType: "DateTime", seed: "'2024-01-02 03:04:05'", lowCardinality: true, nullable: true},
	{key: "dt64", chType: "DateTime64(3)", seed: "'2024-01-02 03:04:05.123'", lowCardinality: false, nullable: true},
	// Bool. It is a real, distinct server type that a value-preserving
	// rule must keep, while the predicate family must turn it into
	// UInt8. It must be in the grid: a rule that gets either direction
	// wrong shows only here.
	{key: "b", chType: "Bool", seed: "true", lowCardinality: true, nullable: true},
	// The identifier family. The server accepts these under
	// LowCardinality although its own message denies it.
	{key: "uid", chType: "UUID", seed: "'61f0c404-5cb3-11e7-907b-a6006ad3dba0'", lowCardinality: true, nullable: true},
	{key: "ip4", chType: "IPv4", seed: "'1.2.3.4'", lowCardinality: true, nullable: true},
	// The Enum family. Refused under LowCardinality, accepted under
	// Nullable.
	{key: "e8", chType: "Enum8('a' = 1, 'zz' = 2)", seed: "'a'", lowCardinality: false, nullable: true},
	// The container family. Refused under both LowCardinality and
	// Nullable.
	{key: "arr", chType: "Array(Int32)", seed: "[1, 2]", lowCardinality: false, nullable: false},
	{key: "tup", chType: "Tuple(Int32, String)", seed: "(1, 'a')", lowCardinality: false, nullable: false},
	{key: "map", chType: "Map(String, Int64)", seed: "map('k', 1)", lowCardinality: false, nullable: false},
	// An Array whose ELEMENT carries a wrapper. Every other base of
	// the grid wraps the WHOLE column, thus no other cell can ask what
	// a function does to the element wrapper.
	//
	// WHY BOTH AND NOT ONE. The server DROPS an element LowCardinality
	// and KEEPS an element Nullable. Measured on ClickHouse 25.8.29.51
	// with real columns of a real table:
	//
	//	arraySort(lcarr)  Array(String)
	//	arraySort(narr)   Array(Nullable(Int32))
	//
	// One base alone would read as "the element wrapper always goes"
	// or as "the element wrapper always stays". Both readings are
	// wrong, and only the PAIR shows that the answer depends on which
	// wrapper the element carries.
	//
	// The narr seed holds a NULL on purpose. arrayDistinct answers
	// Array(Int32) over narr, which looks like a lost Nullable until
	// the VALUE is read: [1, NULL, 2] gives [1, 2], thus the function
	// really discards the nulls and the type is right. A seed without
	// a NULL could not tell a correct answer from a lost wrapper.
	{key: "narr", chType: "Array(Nullable(Int32))", seed: "[1, NULL, 2]", lowCardinality: false, nullable: false},
	{key: "lcarr", chType: "Array(LowCardinality(String))", seed: "['a', 'b']", lowCardinality: false, nullable: false, noSimpleAggregate: true},
}

// gridColumnName gives the fixture column name of one (wrapper, base)
// cell. The name is derived, never written by hand, so a column and the
// cell that reads it cannot drift apart.
func gridColumnName(wrapper, base string) string {
	return "c_" + wrapper + "_" + base
}

// gridCell names one column of the fixture together with the type that
// the server gave it.
type gridCell struct {
	wrapper string
	base    string
	column  string
	chType  string
}

// gridColumns enumerates every legal (wrapper, base) pair of the closed
// alphabet.
//
// A pair is legal when the server accepts the resulting column type. The
// refusals are MEASURED flags on gridBase, thus this function never asks
// the server and the default test run can call it.
//
// SimpleAggregateFunction is the one wrapper whose legality does not
// follow Nullable: the server accepts SimpleAggregateFunction over a
// Nullable inner type although AggregateFunction does not. A guard that
// tested for a Nullable prefix would therefore drop legal cells, and the
// grid would shrink in silence. The rule below is written from the
// measurement instead.
func gridColumns() []gridCell {
	var cells []gridCell
	for _, wrapper := range gridWrappers {
		for _, base := range gridBases {
			if !gridWrapperAccepts(wrapper.key, base) {
				continue
			}
			cells = append(cells, gridCell{
				wrapper: wrapper.key,
				base:    base.key,
				column:  gridColumnName(wrapper.key, base.key),
				chType:  fmt.Sprintf(wrapper.format, base.chType),
			})
		}
	}
	return cells
}

// gridWrapperAccepts reports whether the server accepts one wrapper over
// one base type. Every answer here is a measurement; see the comments on
// the gridBase flags.
func gridWrapperAccepts(wrapper string, base gridBase) bool {
	switch wrapper {
	case "bare":
		return true
	case "lc":
		return base.lowCardinality
	case "n":
		return base.nullable
	case "lcn":
		return base.lowCardinality && base.nullable
	case "saf":
		// SimpleAggregateFunction(anyLast, T) is accepted for almost
		// every base type of the grid, containers included.
		//
		// This case gave the constant true while the alphabet held no
		// base with a wrapped ELEMENT. The constant was correct for
		// the alphabet of that day, and it is not correct any more:
		// the server refuses
		// SimpleAggregateFunction(anyLast, Array(LowCardinality(String)))
		// with Code 36. See the noSimpleAggregate flag for the
		// measurement and the reason.
		return !base.noSimpleAggregate
	case "saflc":
		// The inner LowCardinality must itself be legal, and it is
		// legal for exactly the same base set as a top-level
		// LowCardinality. Measured on ClickHouse 25.8.29.51 with
		// allow_suspicious_low_cardinality_types=1, by CREATE TABLE of
		// a real AggregatingMergeTree column:
		//
		//	SimpleAggregateFunction(anyLast, LowCardinality(Int32))
		//	                                            accepted
		//	SimpleAggregateFunction(anyLast, LowCardinality(String))
		//	                                            accepted
		//	SimpleAggregateFunction(anyLast, LowCardinality(UUID))
		//	                                            accepted
		//	... Decimal(18, 4)                          Code 43
		//	... DateTime64(3)                           Code 43
		//	... Enum8('a' = 1, 'zz' = 2)                Code 43
		//	... Array(Int32)                            Code 43
		//	... Tuple(Int32, String)                    Code 43
		//	... Map(String, Int64)                      Code 43
		//
		// The accepted set is identical to the measured lowCardinality
		// flag of gridBase, thus the flag is reused rather than copied.
		return base.lowCardinality
	case "safarr":
		// An Array inner is accepted under the marker for EVERY base
		// type of the grid, containers included. This legality follows
		// NO existing gridBase flag: the lowCardinality flag is false
		// for dec, dt64, e8, arr, tup and map, and the nullable flag is
		// false for arr, tup and map, but the marker accepts an Array
		// inner over all of them. Reusing either flag would drop legal
		// cells in silence.
		//
		// Measured on ClickHouse 25.8.29.51 by CREATE TABLE of a real
		// AggregatingMergeTree column, one statement per row:
		//
		//	SimpleAggregateFunction(anyLast, Array(Int32))    accepted
		//	... Array(UInt64)                                 accepted
		//	... Array(Float64)                                accepted
		//	... Array(Decimal(18, 4))                         accepted
		//	... Array(String)                                 accepted
		//	... Array(FixedString(8))                         accepted
		//	... Array(Date)                                   accepted
		//	... Array(DateTime)                               accepted
		//	... Array(DateTime64(3))                          accepted
		//	... Array(Bool)                                   accepted
		//	... Array(UUID)                                   accepted
		//	... Array(IPv4)                                   accepted
		//	... Array(Enum8('a' = 1, 'zz' = 2))               accepted
		//	... Array(Array(Int32))                           accepted
		//	... Array(Tuple(Int32, String))                   accepted
		//	... Array(Map(String, Int64))                     accepted
		//
		// The list above was measured while the alphabet held no base
		// with a wrapped ELEMENT. Such a base arrived in the same change,
		// and it changes the answer. Measured on ClickHouse 25.8.29.51:
		//
		//	... Array(Array(Int32))                    accepted
		//	... Array(Array(Nullable(Int32)))          accepted
		//	... Array(Array(LowCardinality(String)))   Code 36
		//
		// The reason is the SAME one that the noSimpleAggregate flag
		// records: the aggregate anyLast drops an element
		// LowCardinality at ANY depth, thus its return type can never
		// match the storage type. A Nullable element survives at any
		// depth, thus it stays legal.
		//
		// The flag therefore governs this case as well. The letter adds
		// one more Array level, and that level does not change which
		// element wrapper the aggregate keeps.
		return !base.noSimpleAggregate
	case "safn":
		// The inner Nullable must itself be legal. Measured: the
		// server answers Code 43 for
		// SimpleAggregateFunction(anyLast, Nullable(Array(Int32))).
		return base.nullable
	}
	return false
}

// gridSchemaDDL builds the CREATE TABLE of the grid fixture from the
// enumerated columns. The DDL is DERIVED, never written by hand: the same
// list that addresses the cells makes the table, thus a column cannot
// exist without a cell and a cell cannot name a missing column.
//
// The table name is `g`, not `t`. The oracle fixture uses `t`, and a
// distinct name keeps the two fixtures from being confused in a database
// that holds both.
func gridSchemaDDL() string {
	var lines []string
	for _, cell := range gridColumns() {
		lines = append(lines, "    "+cell.column+" "+cell.chType)
	}
	return "CREATE TABLE g (\n" + strings.Join(lines, ",\n") +
		"\n) ENGINE = MergeTree ORDER BY tuple()"
}

// gridSeedRow builds the one seed row.
//
// It uses INSERT ... SELECT and an explicit CAST for each column. A
// VALUES literal cannot write a SimpleAggregateFunction column, and a
// bare literal would let the server pick its own type for the value
// instead of the declared column type.
//
// One row is enough and more than one would be wrong: the grid measures
// TYPES, and a type does not depend on the row count. A single row keeps
// every execution witness cheap.
func gridSeedRow() string {
	seeds := map[string]string{}
	for _, base := range gridBases {
		seeds[base.key] = base.seed
	}
	var values []string
	for _, cell := range gridColumns() {
		seed := seeds[cell.base]
		// A wrapper that adds a CONTAINER around the base needs the
		// seed in that container too. The base seed is a SCALAR value
		// of the base type, and the server cannot cast a scalar to an
		// array. Measured on ClickHouse 25.8.29.51:
		//
		//	CAST(3, 'Array(Int32)')       Code 53
		//	CAST('abc', 'Array(Int32)')   Code 130
		//	CAST([1, 2], 'Array(Int32)')  Array(Int32)
		//
		// A one-element array is enough. The grid measures TYPES, and
		// the element count does not change a type.
		if cell.wrapper == "safarr" {
			seed = "[" + seed + "]"
		}
		values = append(values, "CAST("+seed+", '"+
			strings.ReplaceAll(cell.chType, "'", "\\'")+"')")
	}
	return "INSERT INTO g SELECT " + strings.Join(values, ", ")
}
