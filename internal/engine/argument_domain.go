package engine

import (
	"fmt"
	"strings"

	clickhouse "github.com/AfterShip/clickhouse-sql-parser/parser"
)

// domainBaseType removes wrappers that do not change an argument domain.
func domainBaseType(columnType CHType) CHType {
	current := columnType
	for {
		switch current.normalizedName() {
		case "nullable", "lowcardinality":
			if len(current.Params) != 1 {
				return current
			}
			current = current.Params[0]
		default:
			return current
		}
	}
}

// A type rule must refuse an argument that the server refuses. A rule
// that gives a type to an impossible call is a silent wrong answer: the
// generated Go compiles, and the query then fails at run time.
//
// The accept-sets below are measured, not assumed. Every entry comes
// from ClickHouse 25.8.29.51 with a real table column, never a literal,
// because the server folds constants and a rule measured over a literal
// reports the wrong domain. The measurement is one probe for each
// argument type:
//
//	SELECT toTypeName(<function>(<column>)) FROM probe
//
// A column that gives a type is in the accept-set. A column that gives
// Code: 43 (ILLEGAL_TYPE_OF_ARGUMENT) is outside it.

// argumentDomain names the measured set of base types that one function
// accepts as its data argument. The check runs on the base type, that is
// after the Nullable and the LowCardinality wrappers are removed,
// because the server decides on the inner type: sum(ns) and sum(s) both
// fail for the String, not for the Nullable.
type argumentDomain struct {
	// name is the domain's name for the refusal message.
	name string
	// accepts reports whether the base type is in the measured set.
	accepts func(CHType) bool
	// expected describes the accepted types for the refusal message.
	expected string
}

// integerBaseType covers the fixed-width integers, the wide integers and
// Bool. ClickHouse treats Bool as UInt8 for every arithmetic aggregate
// (measured: sum(b) is UInt64, groupBitXor(b) is UInt8).
func integerBaseType(value CHType) bool {
	if len(value.Params) != 0 {
		return false
	}
	switch value.normalizedName() {
	case "int8", "int16", "int32", "int64", "int", "int128", "int256",
		"uint8", "uint16", "uint32", "uint64", "uint128", "uint256",
		"bool", "boolean":
		return true
	default:
		return false
	}
}

// floatBaseType covers the two floating point types.
func floatBaseType(value CHType) bool {
	if len(value.Params) != 0 {
		return false
	}
	switch value.normalizedName() {
	case "float32", "float64":
		return true
	default:
		return false
	}
}

// numberBaseType covers all numeric expressions.
func numberBaseType(value CHType) bool {
	return integerBaseType(value) || floatBaseType(value) || arithmeticDecimalType(value) ||
		enumBaseType(value) || value.normalizedName() == "bfloat16"
}

// offsetBaseType covers fixed-width numbers. Wide integers, Decimals, and
// Enums are not in this server domain.
func offsetBaseType(value CHType) bool {
	if len(value.Params) != 0 {
		return false
	}
	switch value.normalizedName() {
	case "int8", "int16", "int32", "int64", "int",
		"uint8", "uint16", "uint32", "uint64", "bool", "boolean",
		"float32", "float64":
		return true
	default:
		return false
	}
}

// indexBaseType covers fixed-width integers.
func indexBaseType(value CHType) bool {
	if len(value.Params) != 0 {
		return false
	}
	switch value.normalizedName() {
	case "int8", "int16", "int32", "int64", "int",
		"uint8", "uint16", "uint32", "uint64", "bool", "boolean":
		return true
	default:
		return false
	}
}

// enumBaseType covers Enum8 and Enum16. The server reads an Enum as its
// underlying integer for sum, but refuses it for avg and quantile
// (measured: sum(e8) is Int64, avg(e8) and quantile(0.5)(e8) are
// Code: 43).
func enumBaseType(value CHType) bool {
	switch value.normalizedName() {
	case "enum", "enum8", "enum16":
		return true
	default:
		return false
	}
}

// temporalBaseType covers the date and the time types. quantile keeps
// them, sum refuses them (measured: quantile(0.5)(d) is Date,
// sum(d) is Code: 43).
func temporalBaseType(value CHType) bool {
	switch value.normalizedName() {
	case "date", "datetime", "datetime64":
		return true
	default:
		return false
	}
}

// stringLikeBaseType covers the argument types that the string functions
// accept (measured: lower, upper, trim, trimLeft and trimRight accept
// String and FixedString only; every other column gives Code: 43).
func stringLikeBaseType(value CHType) bool {
	switch value.normalizedName() {
	case "string", "fixedstring":
		return true
	default:
		return false
	}
}

// countableBaseType covers the argument types that empty and notEmpty
// accept (measured: String, FixedString, Array, Map, UUID, IPv4 and
// IPv6 give a type; every numeric, Enum, temporal and Tuple column
// gives Code: 43).
//
// length does NOT share this set. See byteLengthBaseType below.
func countableBaseType(value CHType) bool {
	switch value.normalizedName() {
	case "string", "fixedstring", "array", "map", "uuid", "ipv4", "ipv6":
		return true
	default:
		return false
	}
}

// byteLengthBaseType covers the argument types that length and
// octet_length accept: a raw byte count over a String, a FixedString or
// a container. IPv4, IPv6 and UUID are OUT of this set, unlike
// countableBaseType above.
//
// Measured on ClickHouse 25.8.29.51 by EXECUTION and not only by
// toTypeName, because toTypeName is analysis and is blind to a refusal
// that happens while the function runs: "SELECT toTypeName(length(ip4))"
// answers UInt64, while "SELECT length(ip4) FROM probe" is Code: 43,
// "Cannot apply function length to IPv4 argument".
//
//	length(s)      OK, 3       length(fs)     OK, 8
//	length(arr_i)  OK, 2       length(m)      OK, 1
//	length(ip4)    Code: 43    length(ip6)    Code: 43
//	length(uid)    Code: 43    length(i128)   Code: 43
//	length(u256)   Code: 43
//
// octet_length is the same function under another name: the error
// message for octet_length(ip4) names "function length", and the
// measured accept-set and refusal-set are identical for both spellings.
func byteLengthBaseType(value CHType) bool {
	switch value.normalizedName() {
	case "string", "fixedstring", "array", "map":
		return true
	default:
		return false
	}
}

// lengthUTF8 and octet_length are measured for completeness, but chgen
// registers neither name today, thus neither types any call and neither
// needs a domain here: an unregistered name cannot answer a silently
// wrong type. Measured on ClickHouse 25.8.29.51 by EXECUTION, for the
// record:
//
//	lengthUTF8(s)      OK, 3        lengthUTF8(fs)   OK, 8
//	lengthUTF8(arr_i)  Code: 43     lengthUTF8(m)    Code: 43
//	lengthUTF8(ip4)    Code: 43     lengthUTF8(ip6)  Code: 43
//	lengthUTF8(uid)    Code: 43     lengthUTF8(i128) Code: 43
//	lengthUTF8(u256)   Code: 43
//
// octet_length shares the exact accept-set and refusal-set of length: the
// server's error message for octet_length(ip4) names "function length".
// If octet_length is registered later, it must use
// byteLengthArgumentDomain and not countableArgumentDomain.

// arrayBaseType covers the one type that the array-reading functions
// accept. Measured for arrayDistinct, arraySort, arraySlice, arrayResize
// and arrayStringConcat on ClickHouse 25.8.29.51: Array(Int32),
// Array(String), Array(Nullable(Int32)) and Array(Array(Int32)) all give
// a type, and every scalar, Map and Tuple column gives Code: 43. The
// server names the function and the offending type, for example
// "Argument for function arrayDistinct must be array but it has type
// Int32".
//
// A SimpleAggregateFunction(anyLast, Array(Int32)) column is accepted
// too. The check runs on the base type after the marker comes off, thus
// this set does not have to name the marker.
func arrayBaseType(value CHType) bool {
	return value.normalizedName() == "array"
}

// containerBaseType covers the three types that hold other values:
// Array, Map and Tuple. A scalar function refuses all three and accepts
// every scalar.
//
// Measured on ClickHouse 25.8.29.51 with real columns of one table, with
// a VALUE select and never toTypeName alone. toTypeName LIES here:
// "SELECT toTypeName(toDate(arr))" answers Date, while
// "SELECT toDate(arr)" is Code: 43. The sweep below is by EXECUTION:
//
//	                arr  mp   tp   s    i32  d    dt   u    ip   dec  fs   e8
//	toInt8          43   43   43   ok   ok   ok   ok   48   48   ok   6    ok
//	toUInt64        43   43   43   ok   ok   ok   ok   48   ok   ok   6    ok
//	toFloat64       43   43   43   ok   ok   ok   ok   48   48   ok   6    ok
//	toDate          43   43   43   38   ok   ok   ok   48   48   44   38   ok
//	toDate32        43   43   43   38   ok   ok   ok   48   48   44   38   ok
//	toDateTime      43   43   43   41   ok   ok   ok   48   48   44   41   ok
//	hex             43   43   43   ok   ok   ok   ok   ok   ok   ok   ok   43
//	nullIf(c, c)    43   43   43   ok   ok   ok   ok   ok   ok   ok   ok   ok
//
// Only Code: 43 is type evidence. Codes 6, 38 and 41 are VALUE errors:
// the server accepted the String and then could not parse 'a', thus the
// String stays in the domain. Code 48 is a narrower per-function gap
// for UUID, and this shared domain does not state it, because a
// refusal wider than the server's would break a query that runs. Code
// 44 in the dec column of the toDate, toDate32 and toDateTime rows is
// a different kind of gap: it is shared by all three functions and
// every Decimal width, and dateFromValueArgumentDomain, below, names
// it.
//
// The three container columns are the ONE boundary that every row of the
// sweep shares, thus one rule covers the whole family.
func containerBaseType(value CHType) bool {
	value = withoutGeometryAliases(value)
	switch value.normalizedName() {
	case "array", "map", "tuple":
		return true
	default:
		return false
	}
}

// textArgumentBaseType covers the types that substring and the LIKE
// family read as text. It is stringLikeBaseType PLUS the Enums.
//
// The Enum is the reason this is a separate set. Measured on
// ClickHouse 25.8.29.51 and confirmed with a real SELECT:
// substring(e8, 2) returns a row, while lower(e8) and trim(e8) are
// Code: 43. The two families therefore do not share one domain, and
// giving substring the narrower stringArgumentDomain would refuse a call
// that the server runs.
func textArgumentBaseType(value CHType) bool {
	if stringLikeBaseType(value) {
		return true
	}
	return enumBaseType(value)
}

// dateArgumentBaseType covers every date and time type. It is
// temporalBaseType PLUS Date32.
//
// Date32 is the reason this is a separate set. temporalBaseType leaves
// Date32 out because quantile really refuses it, but toStartOfDay and
// the dateDiff pair accept it. Measured on ClickHouse 25.8.29.51:
// toStartOfDay(d32) is DateTime and dateDiff('day', d32, d32) is Int64,
// while toStartOfDay(i32) and toStartOfDay(s) are Code: 43 with the
// message "Should be Date, Date32, DateTime or DateTime64".
func dateArgumentBaseType(value CHType) bool {
	if temporalBaseType(value) {
		return true
	}
	return value.normalizedName() == "date32"
}

// The measured domains. Each `expected` string lists the types that the
// server accepted in the sweep, so the refusal tells the user what would
// work instead of only what failed.
var (
	// sum accepts the integers, the floats, the Decimals and the Enums.
	// Measured rejects: String, FixedString, Date, Date32, DateTime,
	// DateTime64, Array, Map, Tuple, UUID, IPv4 and IPv6.
	sumArgumentDomain = argumentDomain{
		name: "sum",
		accepts: func(value CHType) bool {
			return integerBaseType(value) || floatBaseType(value) ||
				arithmeticDecimalType(value) || enumBaseType(value)
		},
		expected: "an integer, a float, a Decimal or an Enum",
	}

	// avg accepts the integers, the floats and the Decimals. It refuses
	// the Enums, unlike sum. Measured rejects: String, FixedString,
	// Enum8, Enum16, every temporal type, Array, Map, Tuple, UUID, IPv4
	// and IPv6.
	avgArgumentDomain = argumentDomain{
		name: "avg",
		accepts: func(value CHType) bool {
			return integerBaseType(value) || floatBaseType(value) ||
				arithmeticDecimalType(value)
		},
		expected: "an integer, a float or a Decimal",
	}

	// quantile and median accept the integers, the floats, the Decimals
	// and Date, DateTime and DateTime64. They refuse Date32, unlike the
	// other temporal types. Measured rejects: String, FixedString,
	// Enum8, Enum16, Date32, Array, Map, Tuple, UUID, IPv4 and IPv6.
	quantileArgumentDomain = argumentDomain{
		name: "quantile",
		accepts: func(value CHType) bool {
			return integerBaseType(value) || floatBaseType(value) ||
				arithmeticDecimalType(value) || temporalBaseType(value)
		},
		expected: "an integer, a float, a Decimal, a Date, a DateTime or a DateTime64",
	}

	// The bitwise group aggregates accept the integers only. They refuse
	// the floats and the Decimals, unlike sum. Measured rejects:
	// Float32, Float64, every Decimal, String, FixedString, Enum8,
	// Enum16, every temporal type, Array, Map, Tuple, UUID, IPv4 and
	// IPv6.
	groupBitArgumentDomain = argumentDomain{
		name: "the bitwise group aggregate",
		accepts: func(value CHType) bool {
			return integerBaseType(value)
		},
		expected: "an integer",
	}

	// arrayDistinct, arraySort, arraySlice, arrayResize and
	// arrayStringConcat read their first argument as an array and accept
	// nothing else.
	//
	// Every refusal is Code: 43. Measured refusals: Int8, Int32, Int64,
	// UInt8, UInt64, Int128, Float64, Decimal, Bool, String,
	// FixedString, Date, Date32, DateTime, DateTime64, Enum8, UUID,
	// IPv4, IPv6, Map and Tuple.
	//
	// The domain does NOT reach a call whose first argument is a lambda,
	// for example arraySort(x -> x, arr). inferFunctionType routes such
	// a call to inferHigherOrderArrayType before the registry lookup,
	// thus the higher-order form keeps working.
	arrayArgumentDomain = argumentDomain{
		name: "the array function",
		accepts: func(value CHType) bool {
			return arrayBaseType(value)
		},
		expected: "an Array",
	}

	// has reads its first argument as a container, and it accepts a Map
	// as well as an Array. It therefore cannot share
	// arrayArgumentDomain.
	//
	// Measured on ClickHouse 25.8.29.51:
	//
	//	has(arr, 1)     UInt8
	//	has(arrn, 1)    UInt8
	//	has(mp, 'a')    UInt8
	//	has(tp, 1)      Code: 43   First argument for function has must
	//	                           be an array or map. Actual
	//	                           Tuple(Int32, Int32)
	//	has(i32, 1)     Code: 43   ... Actual Int32
	//	has(s, 'a')     Code: 43   ... Actual String
	//
	// A second argument that shares no supertype with the element is
	// Code: 386, NO_COMMON_TYPE, and not Code: 43. That is a
	// common-type refusal about the PAIR and not a domain of the first
	// argument, thus this domain does not state it.
	// scalarArgumentDomain is the domain of the SCALAR-ARGUMENT family:
	// hex and nullIf. Every one of them reads a single value and refuses
	// a container. See containerBaseType for the measured sweep that
	// this domain states.
	//
	// The family refuses the containers only. It does NOT name the
	// narrower per-function gap for UUID, for example toDate(u) Code:
	// 48, because a domain wider than the server's gives a wrong type
	// while a domain narrower than the server's refuses a query that
	// runs. The Decimal gap that the same sweep found for toDate,
	// toDate32 and toDateTime is named below, in
	// dateFromValueArgumentDomain, because that gap is shared by every
	// Decimal width and thus is wide enough for its own domain.
	//
	// hex is why an AggregateFunction state is NOT refused here.
	// Measured on 25.8.29.51 over a real AggregatingMergeTree column
	// (agg is AggregateFunction(uniq, UInt64)):
	//
	//	hex(agg)         String, "00012CCBC234", RUNS
	//	toString(agg)    String, RUNS (toString has no domain at all)
	//
	// hex and toString read the state's raw bytes; they never interpret
	// it as a number, thus the state stays inside this domain. The toXxx
	// numeric and date/time conversions DO interpret the value, and they
	// refuse the state (Code: 43); see castArgumentDomain below for that
	// narrower, measured fact.
	scalarArgumentDomain = argumentDomain{
		name: "the scalar function",
		accepts: func(value CHType) bool {
			return !containerBaseType(value)
		},
		expected: "a scalar, not an Array, a Map or a Tuple",
	}

	// hexArgumentDomain is scalarArgumentDomain MINUS three measured
	// gaps: a DateTime64 argument, an Enum argument and a Date32
	// argument. hex reads raw bytes for most types, but the server
	// refuses these three at different steps, thus this is three facts
	// and not one.
	//
	// Measured on ClickHouse 25.8.29.51 over real columns, both by
	// DESCRIBE and by a VALUE select (dt64 is DateTime64(3), dtz64 is
	// DateTime64(6, 'UTC'), e8 is Enum8('a'=1,'zz'=2), e16 is
	// Enum16('x'=1,'y'=2)):
	//
	//	hex(dt64)    Code: 44   Illegal column DateTime64 of argument
	//	                        of function hex
	//	hex(dtz64)   Code: 44   same message
	//	hex(e8)      Code: 43   Illegal type Enum8(...) of argument of
	//	                        function hex
	//	hex(e16)     Code: 43   same message, Enum16
	//
	// The DateTime64 refusal is Code: 44, ILLEGAL_COLUMN, a
	// column-level gap; the Enum refusal is Code: 43,
	// ILLEGAL_TYPE_OF_ARGUMENT, a type-level gap. The two codes name two
	// different steps of the server's check, so this domain states both
	// facts and does not merge them into one guess.
	//
	// Confirmed still accepted, unchanged from scalarArgumentDomain and
	// measured on the same probe: Int32, String, Float64, FixedString,
	// Date, DateTime (plain, not DateTime64) and an AggregateFunction
	// state.
	//
	// Date32 is the third measured gap: hex(dt32) gives Code: 43, while
	// hex(d) and hex(dt) both run. See the "date32" case below.
	hexArgumentDomain = argumentDomain{
		name: "hex",
		accepts: func(value CHType) bool {
			if containerBaseType(value) {
				return false
			}
			if enumBaseType(value) {
				return false
			}
			if value.normalizedName() == "date32" {
				// Measured on ClickHouse 25.8.29.51 over a real Date32
				// column and confirmed by execution: hex(dt32) is
				// Code: 43, "Illegal type Date32 of argument of
				// function hex". Date (plain) and DateTime both keep
				// running. Thus, this rule applies to Date32 alone and
				// not to the complete date family.
				return false
			}
			return value.normalizedName() != "datetime64"
		},
		expected: "a scalar that is not a Date32, a DateTime64, an Enum, an Array, a Map or a Tuple",
	}

	// castArgumentDomain is scalarArgumentDomain PLUS one more measured
	// fact: an AggregateFunction state is refused by every toXxx
	// conversion that interprets its argument as a number, a Decimal or
	// a date/time value.
	//
	// Measured on ClickHouse 25.8.29.51 over real columns of an
	// AggregatingMergeTree table (agg is AggregateFunction(uniq,
	// UInt64), aggif is AggregateFunction(sumIf, Int32, UInt8)), and
	// confirmed by a real SELECT, not only DESCRIBE:
	//
	//	toUInt8(aggif)     Code: 43   toInt16(agg)      Code: 43
	//	toInt32(aggif)     Code: 43   toFloat32(agg)    Code: 43
	//	toFloat64(agg)     Code: 43   toDecimal32(agg, 2)  Code: 43
	//	toDateTime64(agg, 3)  Code: 43
	//
	// This is a DIFFERENT gap from the comparability rule in
	// comparableBaseTypes: that rule refuses the PAIR in "agg = agg",
	// this rule refuses the SINGLE argument of a cast. Both read the
	// same fact, holdsAggregateFunctionState, because a value that
	// cannot be compared or cast is one and the same value: an opaque
	// binary state that no scalar operation can interpret.
	//
	// hex and toString are NOT in this family (see scalarArgumentDomain
	// above): they read the raw bytes and never interpret them, so they
	// keep accepting the state.
	//
	// JSON and Variant are two more measured refusals. The analysis
	// witness gives every fixed target type, but the execution witness
	// refuses the real js and variant columns with Code: 43 for every
	// narrow integer, float and DateTime64 conversion. A type from
	// analysis alone is not evidence that the conversion can run.
	// toString is the legal neighbor: it reads either value and returns
	// String. Dynamic is another legal neighbor: toInt64(dyn) runs.
	castArgumentDomain = argumentDomain{
		name: "the numeric or date conversion",
		accepts: func(value CHType) bool {
			if holdsAggregateFunctionState(value) {
				return false
			}
			if value.normalizedName() == "json" || value.normalizedName() == "variant" {
				return false
			}
			return !containerBaseType(value)
		},
		expected: "a scalar that is not JSON, Variant, an AggregateFunction state, an Array, a Map or a Tuple",
	}

	// toDate, toDate32 and toDateTime read a value and additionally
	// refuse a Decimal, unlike the rest of the scalar-argument family.
	//
	// Measured on ClickHouse 25.8.29.51 with DESCRIBE and confirmed by
	// EXECUTION over real columns of an empty and then a one-row table
	// (DESCRIBE alone is a valid witness here, because the failure is
	// ILLEGAL_COLUMN, not a value-parse error):
	//
	//	toDate(dec)        Code: 44   Illegal column ... of first
	//	                              argument of function toDate
	//	toDate32(dec)      Code: 44
	//	toDateTime(dec)    Code: 44
	//
	// All five Decimal widths give the same refusal: Decimal(18,4),
	// Decimal32(4), Decimal64(4), Decimal128(4) and Decimal256(4). That
	// is one fact about the Decimal family, not a per-width list.
	//
	// toDateTime64 is the reason this is a separate domain from
	// castArgumentDomain and NOT a widened containerBaseType check.
	// toDateTime64(dec, 3) RUNS and returns
	// "1970-01-01 00:00:01.500", thus a Decimal is only refused by the
	// three functions above, and toDateTime64 must keep
	// castArgumentDomain.
	//
	// The *OrZero and *OrNull spellings of these three functions
	// already refuse every Decimal width with Code: 43 through
	// castTextArgumentDomain, which accepts String and FixedString
	// only, thus they need no change here.
	//
	// Confirmed still accepted, unchanged from castArgumentDomain: every
	// integer, both floats, String and FixedString.
	//
	// An AggregateFunction state is refused here too, for the same
	// measured reason as castArgumentDomain: toDate, toDate32 and
	// toDateTime interpret their argument as a value, and an
	// AggregateFunction state carries none (measured: toDate(agg) is
	// Code: 43, same message shape as toUInt8(agg)).
	//
	// JSON and Variant are refused for toDate, toDate32 and toDateTime
	// too. For each call, toTypeName gives the target type but a real
	// value SELECT over either column gives Code: 43. Integer and String
	// columns remain the accepted controls for this family, and Dynamic
	// remains accepted.
	dateFromValueArgumentDomain = argumentDomain{
		name: "toDate, toDate32 or toDateTime",
		accepts: func(value CHType) bool {
			if holdsAggregateFunctionState(value) {
				return false
			}
			if value.normalizedName() == "json" || value.normalizedName() == "variant" {
				return false
			}
			if arithmeticDecimalType(value) {
				return false
			}
			return !containerBaseType(value)
		},
		expected: "a scalar that is not JSON, Variant, an AggregateFunction state, a Decimal, an Array, a Map or a Tuple",
	}

	hasArgumentDomain = argumentDomain{
		name: "has",
		accepts: func(value CHType) bool {
			return arrayBaseType(value) || value.normalizedName() == "map"
		},
		expected: "an Array or a Map",
	}

	// comparableValueArgumentDomain names the measured gaps of the
	// min/max family: an AggregateFunction state, Dynamic and Variant.
	// Every other measured type keeps its old accept.
	//
	// Measured on ClickHouse 25.8.29.51 over real columns of an
	// AggregatingMergeTree table (agg is AggregateFunction(uniq,
	// UInt64), aggif is AggregateFunction(sumIf, Int32, UInt8)), and
	// confirmed by a real SELECT:
	//
	//	max(aggif)   Code: 43   "... because the values of that data
	//	                        type are not comparable"
	//	min(aggif)   Code: 43   same message
	//	max(agg)     Code: 43   same message
	//
	// The message names comparability, the exact fact that
	// comparableBaseTypes already states for "agg = agg". min and max
	// pick the smallest or the largest of their argument, which needs
	// the same order that "<" and ">" need, thus this domain reads the
	// same holdsAggregateFunctionState fact and does not duplicate it
	// as a new sweep.
	//
	// argMin and argMax share this domain too, but ONLY on their
	// SECOND argument, the comparison key. Their FIRST argument, the
	// value to output, keeps accepting an AggregateFunction state
	// (measured: argMax(agg, i32) is AggregateFunction(uniq, UInt64),
	// RUNS; argMax(i32, agg) is Code: 43, same "not comparable"
	// message). See the domainArgs field on the argMax, argMin,
	// argMaxIf and argMinIf specs in registry.go for how the domain is
	// pinned to index 1 alone.
	//
	// Dynamic and Variant are refused at the same ordering step. Measured
	// with both witnesses over the real dyn and variant columns on
	// ClickHouse 25.8.29.51:
	//
	//	max(dyn)                 Code: 43
	//	argMax(i32, dyn)         Code: 43
	//	argMaxIf(d, dyn, b)      Code: 43
	//	argMax(dyn, i32)         Dynamic, accepted
	//	argMax(i32, variant)     Code: 43
	//	argMax(variant, i32)     Variant(Int32, String), accepted
	//
	// The accepting cells prove that the refusal belongs to the ordering
	// value and not to every Dynamic or Variant argument.
	comparableValueArgumentDomain = argumentDomain{
		name: "the ordering aggregate",
		accepts: func(value CHType) bool {
			return !holdsAggregateFunctionState(value) &&
				!isDynamicCHType(value) && value.normalizedName() != "variant"
		},
		expected: "a value the server can order, not Dynamic, Variant or an AggregateFunction state",
	}
)

// valuePreservingDomainFunctions names the functions that carry
// comparableValueArgumentDomain (or its domainArgs-pinned use on argMax
// and argMin) AND still give the caller's ARGUMENT back rather than
// computing a new value from it.
//
// inferFunctionType uses "the function has a measured domain" as a proxy
// for "the function computes a new value from its argument, thus a
// SimpleAggregateFunction wrapper must unwrap before the domain check".
// That proxy holds for sum, avg, the bitwise group aggregates and
// quantile, which share max and min's class (wrapperAggregate) yet
// compute (measured: sum(sagg) is Int64, an unwrapped answer). It does
// NOT hold for max, min, argMax, argMin and their -If variants. Adding
// comparableValueArgumentDomain must not change their measured marker
// behaviour (max(sagg) is still
// SimpleAggregateFunction(sum, Int64)), so this set names the exception
// the same way wrapperTransportOverrides names the lower/upper
// exception for the wrapper TRANSPORT question. See the "keepsMarker"
// local in inferFunctionType for the one call site.
var valuePreservingDomainFunctions = map[string]bool{
	"max": true, "min": true, "maxif": true, "minif": true,
	"argmax": true, "argmin": true, "argmaxif": true, "argminif": true,
}

var (

	// substring reads its first argument as text. It accepts the Enums,
	// which the narrower stringArgumentDomain does not.
	//
	// Measured accepts: String, FixedString and Enum8, also under a
	// Nullable, a LowCardinality or a SimpleAggregateFunction wrapper,
	// which the base-type check removes before it asks.
	//
	// Measured refusals, every one Code: 43 with the message
	// "Illegal type <T> of first argument of function substring":
	// Int32, UInt64, Float64, Date, UUID, IPv4, Array, Map and Tuple.
	textArgumentDomain = argumentDomain{
		name: "the text function",
		accepts: func(value CHType) bool {
			return textArgumentBaseType(value)
		},
		expected: "a String, a FixedString or an Enum",
	}

	// toStartOfDay reads its argument as a date or a time.
	//
	// Measured accepts: Date, Date32, DateTime and DateTime64.
	//
	// Measured refusals, every one Code: 43 with the message
	// "Illegal type <T> of argument of function toStartOfDay. Should be
	// Date, Date32, DateTime or DateTime64": Int32, UInt64, Float64,
	// String, Enum8, UUID and Array.
	//
	// The dateDiff pair reads the SAME set, but it takes the unit as its
	// first argument and the two dates after it. Those two entries
	// therefore name their positions with the domainArgs field, so the
	// set meets the two dates and never the String unit or the optional
	// String timezone. See the note on those two entries in registry.go.
	dateArgumentDomain = argumentDomain{
		name: "the date function",
		accepts: func(value CHType) bool {
			return dateArgumentBaseType(value)
		},
		expected: "a Date, a Date32, a DateTime or a DateTime64",
	}

	// toStartOfHour and toStartOfMinute read their argument as a time of
	// day, not only a date. They refuse Date and Date32, unlike
	// toStartOfDay, although the server's OWN refusal message names Date
	// and Date32 as accepted ("Should be Date, Date32, DateTime or
	// DateTime64"). The message is shared boilerplate across the
	// toStartOf* family and is wrong for this narrower pair; trust the
	// measured accept-set, not the string.
	//
	// Measured on ClickHouse 25.8.29.51 with real columns:
	//
	//	toStartOfHour(dt)     DateTime      toStartOfHour(dt64)  DateTime('UTC')
	//	toStartOfMinute(dt)   DateTime      toStartOfMinute(dt64) DateTime('UTC')
	//	toStartOfHour(d)      Code: 43      toStartOfHour(d32)   Code: 43
	//	toStartOfHour(s)      Code: 43      toStartOfHour(e8)    Code: 43
	//
	// toStartOfSecond is a THIRD shape (DateTime64 only, refuses a plain
	// DateTime: measured Code: 43 "Illegal type DateTime of argument for
	// function toStartOfSecond") and toStartOfMonth/Week/Quarter/Year are
	// a FOURTH shape (Date result, and they DO accept Date and Date32).
	// Neither of those two shapes is registered here. This domain and the
	// timezone-carrying rule below cover only Hour and Minute. Registering
	// the other two shapes needs its own measurement pass and is left as a known,
	// documented gap rather than folded into this rule by assumption.
	hourMinuteStartOfArgumentDomain = argumentDomain{
		name: "toStartOfHour or toStartOfMinute",
		accepts: func(value CHType) bool {
			return temporalBaseType(value) && value.normalizedName() != "date"
		},
		expected: "a DateTime or a DateTime64",
	}

	// lower, upper, trim, trimLeft and trimRight accept String and
	// FixedString only.
	stringArgumentDomain = argumentDomain{
		name: "the string function",
		accepts: func(value CHType) bool {
			return stringLikeBaseType(value)
		},
		expected: "a String or a FixedString",
	}

	// reverse reads a TEXT or a CONTAINER, unlike lower, upper and the
	// trim family, which read a text only. reverse therefore needs its
	// own domain and must not widen stringArgumentDomain.
	//
	// Measured on ClickHouse 25.8.29.51 over real columns of a real
	// table, never over literals, and every cell confirmed by execution
	// with ignore(), because toTypeName is analysis and is blind to a
	// run-time refusal:
	//
	//	reverse(s)      String           reverse(arr_i)  Array(Int32)
	//	reverse(fs)     FixedString(N)   reverse(tup)    Tuple, REVERSED
	//	reverse(lc)     LowCardinality   reverse(aa)     Array(Array(T))
	//	reverse(e8)     Code: 43         reverse(mp)     Code: 43
	//	reverse(dt)     Code: 43         reverse(u)      Code: 43
	//
	// A Map is REFUSED although it is a container, thus the domain names
	// Array and Tuple and not "any container".
	reverseArgumentDomain = argumentDomain{
		name: "reverse",
		accepts: func(value CHType) bool {
			if stringLikeBaseType(value) {
				return true
			}
			switch value.normalizedName() {
			case "array", "tuple":
				return true
			}
			return false
		},
		expected: "a String, a FixedString, an Array or a Tuple",
	}

	// and, or and xor accept the narrow integers, the two floats and
	// Bool. They refuse the wide integers, the Decimals, the temporal
	// types, the text types, the Enums, UUID, IPv4, IPv6 and the
	// containers. See isLogicOperandType for the measured table; this
	// domain calls it so that the infix operator path and the function
	// form share one accept-set and cannot drift apart.
	//
	// Measured on ClickHouse 25.8.29.51 with real columns, never over
	// literals: xor(u,'a'), xor(u,toDate(...)) and xor(u,toDecimal32(1,2))
	// are all Code: 43, the same refusal that and and or give for the
	// same argument types.
	logicOperatorArgumentDomain = argumentDomain{
		name: "the logic operator",
		accepts: func(value CHType) bool {
			return isLogicOperandType(value)
		},
		expected: "an integer of at most 64 bits, a float or a Bool",
	}

	// xor has one measured extension to the shared logic domain: it
	// accepts Dynamic. and and or do not accept Dynamic, so they keep the
	// narrower logicOperatorArgumentDomain.
	//
	// Measured on ClickHouse 25.8.29.51 with real fixture columns. Both
	// toTypeName and execution accept xor(dyn, i32), xor(i32, dyn),
	// xor(dyn, f64), xor(dyn, b) and xor(dyn, dyn). The result is
	// Nullable(UInt8). Execution refuses xor(dyn, i128), xor(dyn, u128),
	// xor(dyn, dec) and xor(dyn, s), although toTypeName answers for these
	// calls. The extension is therefore Dynamic only and does not change
	// the numeric boundary.
	xorArgumentDomain = argumentDomain{
		name: "xor",
		accepts: func(value CHType) bool {
			return isLogicOperandType(value) || isDynamicCHType(value)
		},
		expected: "an integer of at most 64 bits, a float, a Bool or Dynamic",
	}

	// empty and notEmpty accept the countable types.
	countableArgumentDomain = argumentDomain{
		name: "the emptiness function",
		accepts: func(value CHType) bool {
			return countableBaseType(value)
		},
		expected: "a String, a FixedString, an Array, a Map, a UUID, an IPv4 or an IPv6",
	}

	// length and octet_length read a raw byte count. They refuse IPv4,
	// IPv6, UUID and the wide integers, unlike empty and notEmpty; see
	// byteLengthBaseType for the measured sweep.
	byteLengthArgumentDomain = argumentDomain{
		name: "length",
		accepts: func(value CHType) bool {
			return byteLengthBaseType(value)
		},
		expected: "a String, a FixedString, an Array or a Map",
	}

	// The *OrZero and the *OrNull cast constructors PARSE a text, thus
	// they accept String and FixedString only.
	//
	// Measured on ClickHouse 25.8.29.51 by EXECUTION, not by
	// toTypeName. The distinction decides this domain: toTypeName
	// reports a type for arguments that the server then refuses while
	// it runs the function, thus a domain taken from the analysis
	// alone would be far too wide. The probe was
	//
	//	SELECT <function>(<column>) FROM probe
	//
	// over a real table, and a column counts as accepted only when the
	// server returned a row.
	//
	// Measured for toInt32OrZero, toInt32OrNull, toFloat32OrNull,
	// toDateOrZero, toDate32OrZero, toDate32OrNull, toUUIDOrZero,
	// toIPv4OrZero, toIPv6OrNull, toDecimal64OrZero, toDecimal64OrNull
	// and toDateTime64OrZero. All twelve gave the same set.
	//
	// toFixedString(x, N) shares this domain. It is not an OrZero or an
	// OrNull name, but the measurement gave the same two accepted types.
	//
	// Accepted: String and FixedString, also under a Nullable or a
	// LowCardinality wrapper, which the base-type check removes before
	// it asks.
	//
	// Refused with Code: 43 (Code: 48 for toFixedString): every
	// integer, every float, every Decimal, Bool, Date, Date32,
	// DateTime, DateTime64, Enum8, Enum16, Array, Map, Tuple, UUID,
	// IPv4 and IPv6.
	//
	// This family is the one place where the whole rejected side is a
	// TYPE refusal. The number-taking families next to it answer Code: 6
	// on a text that does not parse, which is a value error and shapes
	// no domain; see the note on wideIntegerArgumentDomain.
	castTextArgumentDomain = argumentDomain{
		name: "the OrZero or OrNull cast constructor",
		accepts: func(value CHType) bool {
			return stringLikeBaseType(value)
		},
		expected: "a String or a FixedString",
	}

	// addDays, addSeconds and their add*/subtract* siblings read their
	// first argument as a moment in time. Measured on ClickHouse
	// 25.8.29.51 with real columns:
	//
	//	addDays(dt, 1)     DateTime      addDays(dt64, 1)  DateTime64(N)
	//	addDays(d, 1)      Date          addDays(d32, 1)   Date32
	//	addSeconds(d, 1)   DateTime      addSeconds(d32,1) DateTime64(3)
	//	addDays(dec, 1)    Code: 43      addDays(fs, 1)    Code: 43
	//
	// A String argument gives a type at ANALYSIS time (addDays(s, 1) is
	// DateTime64(3)) but fails at EXECUTION on a value the parser cannot
	// read as a date (measured: Code: 41 CANNOT_PARSE_DATETIME). That
	// failure is a property of the DATA, not of the type. toFixedString has
	// the same data-dependent failure class. This domain
	// refuses a String argument rather than crediting an
	// execution-fragile "success": a refusal here costs nothing that
	// currently works, because add*/subtract* had no registered rule at
	// all before this domain existed.
	addSubtractTemporalArgumentDomain = argumentDomain{
		name: "the addDays/addSeconds family",
		accepts: func(value CHType) bool {
			return dateArgumentBaseType(value)
		},
		expected: "a Date, a Date32, a DateTime or a DateTime64",
	}

	// The wide integer constructors toInt128, toInt256, toUInt128 and
	// toUInt256 convert a number AND parse a text.
	//
	// Measured by execution on 25.8.29.51, in the same way as the
	// domain above, and with ONE added precaution that decides the
	// result: the text columns of the probe hold a NUMBER, '12'.
	//
	// The precaution is necessary. A String column holding 'abc' gives
	// Code: 6, CANNOT_PARSE_TEXT, and a first sweep read that as a type
	// refusal and left String out of this domain. It is a VALUE error:
	// the same column holding '12' converts, and toInt128(s) is Int128.
	// A domain is a statement about TYPES, thus only the codes that name
	// the type may shape it: 43 ILLEGAL_TYPE_OF_ARGUMENT, 44
	// ILLEGAL_COLUMN and 48 NOT_IMPLEMENTED. Code 6 and Code 407
	// DECIMAL_OVERFLOW are about the value in the row and must not.
	//
	// Accepted: every integer, every float, every Decimal, Bool, Enum8,
	// Enum16, Date, Date32, DateTime, DateTime64, String and
	// FixedString.
	//
	// Refused: Array, Map and Tuple with Code: 43, and UUID, IPv4 and
	// IPv6 with Code: 48.
	//
	// The UUID and the IP types are OUTSIDE this domain although two of
	// the four names accept some of them: toUInt128 takes UUID, IPv4
	// and IPv6, and toUInt256 takes IPv4, while both signed names take
	// none of the three. The domain states the INTERSECTION, that is
	// the set that every member of the family accepts.
	//
	// The intersection is the safe side of the choice. A domain does
	// two things: it refuses an argument in inference, and it splits
	// the generator pool. Too NARROW a domain moves a legal call into
	// the illegal half, which costs one drawn expression and never
	// gives a wrong type. Too WIDE a domain would let inference answer
	// a type for a call that the server refuses, which is the silent
	// wrong answer that this project must not produce. A per-name
	// domain would state the three sets exactly; it needs a measurement
	// of all four names against every type, and it is not part of this
	// coverage change.
	wideIntegerArgumentDomain = argumentDomain{
		name: "the wide integer constructor",
		accepts: func(value CHType) bool {
			return integerBaseType(value) || floatBaseType(value) ||
				arithmeticDecimalType(value) || enumBaseType(value) ||
				temporalBaseType(value) || value.normalizedName() == "date32" ||
				stringLikeBaseType(value)
		},
		expected: "an integer, a float, a Decimal, an Enum, a Date, a Date32, " +
			"a DateTime, a DateTime64, a String or a FixedString",
	}

	// The plain Decimal constructors toDecimal32, toDecimal64,
	// toDecimal128 and toDecimal256 take a number or a text and a
	// constant scale. They accept a narrower set than the wide integer
	// constructors do: they refuse the temporal types and the Enums.
	//
	// Measured by execution on 25.8.29.51 for toDecimal32(x, 2) and
	// toDecimal64(x, 2), with the same precaution as above: the text
	// columns hold '12', so a value error cannot be read as a type
	// refusal.
	//
	// Accepted: every integer, every float, every Decimal, Bool, String
	// and FixedString.
	//
	// Refused with Code: 44: Date, Date32, DateTime, Enum8, Enum16,
	// UUID, IPv4 and IPv6. Refused with Code: 43: Array, Map and Tuple.
	//
	// DateTime64 is NOT in the domain although toDecimal64(dt64, 2)
	// returns a row: toDecimal32(dt64, 2) answers Code: 407,
	// DECIMAL_OVERFLOW. That is about the VALUE and not about the type,
	// thus no domain over base types can state it: the same column
	// overflows a Decimal32 and fits a Decimal64. The domain states the
	// intersection of the four names, and the intersection leaves
	// DateTime64 out. See the note on wideIntegerArgumentDomain for why
	// the intersection is the safe side.
	decimalConstructorArgumentDomain = argumentDomain{
		name: "the Decimal constructor",
		accepts: func(value CHType) bool {
			return integerBaseType(value) || floatBaseType(value) ||
				arithmeticDecimalType(value) || stringLikeBaseType(value)
		},
		expected: "an integer, a float, a Decimal, a String or a FixedString",
	}
)

// checkArgumentDomain refuses an argument that the server refuses. The
// caller passes the base type, that is the type without its Nullable and
// LowCardinality wrappers.
//
// An argument type that inference could not fill in stays accepted: an
// empty type is not evidence of an impossible call, and refusing it here
// would turn an unknown into a false refusal.
func checkArgumentDomain(displayName string, domain argumentDomain, argument CHType) error {
	if argument.Name == "" {
		return nil
	}
	if domain.accepts(argument) {
		return nil
	}
	return fmt.Errorf(
		"function %s does not accept an argument of type %s; ClickHouse needs %s here; %s",
		displayName, argument.String(), domain.expected, pinTypeHint,
	)
}

// argumentDomainFor reports the measured restricted domain for a function
// name. The explicit domain mode distinguishes a restricted domain from an
// unrestricted domain and from a special route.
//
// The -If variants carry the same domain as their base: the condition
// argument does not change which data types the aggregate accepts
// (measured: sumIf(s, b) gives the same Code: 43 as sum(s)).
func argumentDomainFor(name string) (argumentDomain, bool) {
	spec, ok := functionRegistry[strings.ToLower(name)]
	if !ok || spec.domainMode != argumentDomainRestricted || spec.domain == nil {
		return argumentDomain{}, false
	}
	return *spec.domain, true
}

// domainArgumentIndexes reports which arguments the domain of a function
// applies to, as zero-based indexes. Each restricted semantic source entry
// names the full list.
//
// The list, and not a single index, is what lets a domain cover more than
// one position. See the domainArgs field on functionSpec.
func domainArgumentIndexes(name string) []int {
	spec, ok := functionRegistry[strings.ToLower(name)]
	if !ok {
		return []int{0}
	}
	return spec.domainArgs
}

// checkArgumentDomainAt applies a domain to every argument position that
// the spec names. The caller passes a reader that gives the BASE type of
// one argument, that is the type without its Nullable and LowCardinality
// wrappers, because the server decides on the inner type.
//
// A call that has FEWER arguments than a named index is a REFUSAL. The
// alternative, to skip the missing position in silence, would give a
// type to a call that the server cannot run: dateDiff('day') has no date
// argument at all, and an answer of Int64 there is a silent wrong type.
// An arity that the domain cannot check is therefore an arity that chgen
// does not type. This follows the rule of the project: an explicit
// refusal is always better than a silently wrong type.
//
// The reader may report false when it cannot type the argument. That
// stays ACCEPTED, exactly as an empty type does in checkArgumentDomain:
// an argument that inference could not fill in is not evidence of an
// impossible call, and refusing it would turn an unknown into a false
// refusal.
func checkArgumentDomainAt(
	displayName string,
	domain argumentDomain,
	indexes []int,
	argumentCount int,
	baseAt func(index int) (CHType, bool),
) error {
	for _, index := range indexes {
		if index >= argumentCount {
			return fmt.Errorf(
				"function %s needs an argument at position %d, but the call has %d; "+
					"ClickHouse needs %s there; %s",
				displayName, index+1, argumentCount, domain.expected, pinTypeHint,
			)
		}
		base, ok := baseAt(index)
		if !ok {
			continue
		}
		if err := checkArgumentDomain(displayName, domain, base); err != nil {
			return err
		}
	}
	return nil
}

// predicateBodyBaseType covers the types that a lambda body may have when
// the higher-order array function reads the body as a predicate.
//
// Measured on ClickHouse 25.8.29.51 with a real Array(Int32) column, for
// arrayCount, arrayExists, arrayAll, arrayFilter, arrayFirst and
// arrayLast. Every one of the six gives the identical message when the
// body type is outside the set:
//
//	Expression for function <name> must return UInt8 or Nullable(UInt8),
//	found <type>   (Code: 43, ILLEGAL_TYPE_OF_ARGUMENT)
//
// Accepted body types (each measured, each gives a result type):
//
//	UInt8                     toUInt8(x)
//	Bool                      toBool(x > 1)          Bool is UInt8 storage
//	Nullable(UInt8)           toNullable(toUInt8(x))
//	LowCardinality(UInt8)     toLowCardinality(toUInt8(x))
//	Nullable(Nothing)         NULL                   the untyped NULL
//
// Refused body types (each measured, each gives Code: 43):
//
//	Int8, Int32, Int64, UInt16, UInt64, Float32, Float64, String, Date
//	and Nullable(Int32)
//
// The check runs on the base type, that is after the Nullable and the
// LowCardinality wrappers come off, because the server names the inner
// type in its own message: a Nullable(UInt8) body passes and a
// Nullable(Int32) body fails.
func predicateBodyBaseType(value CHType) bool {
	if len(value.Params) != 0 {
		return false
	}
	switch value.normalizedName() {
	case "uint8", "bool", "boolean", "nothing":
		return true
	default:
		return false
	}
}

// predicateBodyDomain is the measured domain of the lambda body of the
// higher-order array functions that read the body as a predicate.
var predicateBodyDomain = argumentDomain{
	name: "the lambda predicate",
	accepts: func(value CHType) bool {
		return predicateBodyBaseType(value)
	},
	expected: "a UInt8, a Bool or a Nullable of them",
}

// checkLambdaPredicateBody refuses a lambda body whose type the server
// refuses. It mirrors checkArgumentDomain, with a message that names the
// lambda body instead of an argument position, because the user must
// change the body expression and not an argument.
//
// A body type that inference could not fill in stays accepted, for the
// same reason as in checkArgumentDomain: an empty type is not evidence of
// an impossible call.
func checkLambdaPredicateBody(displayName string, bodyType CHType) error {
	if bodyType.Name == "" {
		return nil
	}
	base, _, _ := splitCHWrappers(bodyType)
	if predicateBodyDomain.accepts(base) {
		return nil
	}
	return fmt.Errorf(
		"function %s reads its lambda body as a predicate and does not accept the body type %s; ClickHouse needs %s here; %s",
		displayName, bodyType.String(), predicateBodyDomain.expected, pinTypeHint,
	)
}

// intervalUnitRank orders the INTERVAL units from the smallest to the
// largest, so that a domain rule can compare a unit against a floor.
// The value has no meaning except the order.
var intervalUnitRank = map[string]int{
	"nanosecond": 0, "microsecond": 1, "millisecond": 2,
	"second": 3, "minute": 4, "hour": 5,
	"day": 6, "week": 7, "month": 8, "quarter": 9, "year": 10,
}

// toStartOfIntervalUnitFloor gives the smallest INTERVAL unit that
// toStartOfInterval accepts for one argument base type, as the rank in
// intervalUnitRank. A type that is absent has no floor.
//
// Measured on ClickHouse 25.8.29.51 with real columns:
//
//	Date and Date32    accept DAY and larger; SECOND, MINUTE, HOUR and
//	                   the three sub-second units give Code: 43
//	DateTime           accepts SECOND and larger; NANOSECOND,
//	                   MICROSECOND and MILLISECOND give Code: 43
//	DateTime64(3)      accepts every unit
//
// The floor follows the resolution that the argument type can hold: a
// date-only type has no time of day, and a DateTime holds whole seconds.
var toStartOfIntervalUnitFloor = map[string]int{
	"date":     6, // day
	"date32":   6, // day
	"datetime": 3, // second
}

// checkToStartOfIntervalDomain refuses a toStartOfInterval call whose unit
// is finer than the resolution of its first argument.
//
// ClickHouse reports this one at execution time, not at analysis time, so
// a missing rule here does not surface as a type error. It surfaces as a
// query that fails against the server after the generated Go compiled.
func checkToStartOfIntervalDomain(displayName string, base CHType, unit string, argument CHType) error {
	floor, hasFloor := toStartOfIntervalUnitFloor[base.normalizedName()]
	if !hasFloor {
		return nil
	}
	rank, knownUnit := intervalUnitRank[normalizeIntervalUnitName(unit)]
	if !knownUnit || rank >= floor {
		return nil
	}
	smallest := "DAY"
	if floor == 3 {
		smallest = "SECOND"
	}
	return fmt.Errorf(
		"function %s does not accept INTERVAL %s for an argument of type %s; ClickHouse needs INTERVAL %s or a larger unit here, because %s holds no finer resolution; %s",
		displayName, strings.ToUpper(normalizeIntervalUnitName(unit)), argument.String(),
		smallest, base.String(), pinTypeHint,
	)
}

// normalizeIntervalUnitName lowercases an INTERVAL unit and removes a
// plural "s", so that DAY, day and days all match one key.
func normalizeIntervalUnitName(unit string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(unit)), "s")
}

// The comparability domain
//
// A comparison operator, and nullIf, need a pair of operand types that
// ClickHouse can compare. A pair that the server refuses must not get a
// result type here: the predicate result is always UInt8 or Bool, thus a
// rule that looks only at the shape of the result gives a type to an
// expression that the server will not run.
//
// The rule below comes from a full cross sweep of the fixture columns,
// that is 28 distinct base types compared against each other in both
// directions, on ClickHouse 25.8.29.51. Every cell used a REAL COLUMN
// and a real "SELECT a <= b FROM t", never a literal and never
// toTypeName alone, because the server folds constants and answers a
// type for expressions that it then refuses to execute.
//
// The sweep found three refusal codes, and all three are refusals:
//
//	Code: 386  "There is no supertype for types X, Y"
//	Code: 43   "Illegal types of arguments (X, Y) of function lessOrEquals"
//	Code: 43   "No operation lessOrEquals between X and Y"
//	Code: 48   "Conversion from IPv4 to Int64 is not supported"
//
// The measured boundary is a partition into comparison classes. A pair
// is comparable when the two base types share a class:
//
//	numeric      Int8..Int256, UInt8..UInt256, Float32, Float64,
//	             Decimal*, Bool, Enum8, Enum16
//	stringLike   String, FixedString, Enum8, Enum16
//	temporal     Date, Date32, DateTime, DateTime64
//	uuid         UUID
//	ip           IPv4, IPv6
//	array        Array
//	tuple        Tuple
//	map          Map
//	variant      Variant, with the exact same alternative set
//	json         JSON, with the exact same type
//
// The Enum types are in TWO classes on purpose. Measured: "e8 <= i8" and
// "e8 <= s" both give a value, thus an Enum compares with a number and
// with a String alike.
//
// A type that the sweep did not cover keeps every pair accepted, because
// the governing rule refuses only what a measurement refused. A false
// refusal breaks a query that works today.
//
// KNOWN IRREGULAR CELLS, LEFT ACCEPTED ON PURPOSE
//
// The classes above are not a perfect fit for the server. The cells
// below are measured refusals that this rule still accepts, because a
// carve-out for them needs a per-pair table, and a wrong carve-out costs
// a false refusal:
//
//	Enum vs Date, Date32 refused (Code: 43), but "e8 <= dt" is ACCEPTED,
//	                     thus the temporal side is not uniform.
//	IPv4 vs Int8, Float64, Enum
//	                     refused (Code: 48), but "ip4 <= b" and
//	                     "ip4 <= u256" are ACCEPTED, thus the numeric
//	                     side is not uniform either.
//	Date, Date32 vs number
//	                     refused, but "dt <= i8" and "dt64 <= i8" are
//	                     ACCEPTED, thus this edge splits by the temporal
//	                     type and not by the class. The measured
//	                     DateTime-vs-Decimal cell is no longer in this
//	                     list. oneDateTimeOneDecimal refuses it below.
//
// Array(Int32) vs Array(String) is NOT in the list above. The Array branch
// inside comparableBaseTypes applies this predicate recursively. Two Arrays
// compare when their element types compare at every nesting depth.
//
// Each of those stays a silently wrong type for now. They are a smaller
// and much less regular set than the class mismatches, and every one of
// them needs its own measurement to carve out safely.
//
// Enum and Decimal use the same explicit exception. See oneEnumOneDecimal
// below and its call site in comparableBaseTypes.

// comparisonClass names one measured set of mutually comparable base
// types. A base type can be in more than one class: an Enum is both
// numeric and string-like.
type comparisonClass uint16

const (
	comparisonClassNumeric comparisonClass = 1 << iota
	comparisonClassStringLike
	comparisonClassTemporal
	comparisonClassUUID
	comparisonClassIP
	comparisonClassArray
	comparisonClassTuple
	comparisonClassMap
	comparisonClassVariant
	comparisonClassJSON
)

// comparisonClassesOf gives the classes of one base type, that is the
// type after the Nullable and the LowCardinality wrappers come off. A
// zero result means "not covered by the sweep", and the caller then
// accepts the pair.
func comparisonClassesOf(base CHType) comparisonClass {
	if strings.HasPrefix(base.normalizedName(), "interval") {
		// Interval counts compare with native numbers and DateTime.
		// Decimal and the other date/time types are excluded pairwise.
		return comparisonClassNumeric
	}
	switch base.normalizedName() {
	case "int8", "int16", "int32", "int64", "int", "int128", "int256",
		"uint8", "uint16", "uint32", "uint64", "uint128", "uint256",
		"float32", "float64", "bool", "boolean",
		"decimal", "decimal32", "decimal64", "decimal128", "decimal256":
		return comparisonClassNumeric
	case "enum", "enum8", "enum16":
		// Measured: an Enum compares with a number ("e8 <= i8" is 1)
		// and with a String ("e8 <= s" is 1) alike.
		return comparisonClassNumeric | comparisonClassStringLike
	case "string", "fixedstring":
		return comparisonClassStringLike
	case "date", "date32":
		// A date-only type compares inside the temporal family only.
		// Measured on 25.8.29.51 with real columns: "d <= i8",
		// "d <= f64" and "d <= dec" all give Code: 43, and the same
		// three pairs with a Date32 give Code: 43 as well.
		return comparisonClassTemporal
	case "datetime", "datetime64":
		// A DateTime carries a second count, thus it ALSO compares
		// with a number. Measured on 25.8.29.51 with real columns:
		// "dt <= i8" is 0 and "dt <= f64" is 0, while the date-only
		// types refuse the same pairs.
		//
		// The Decimal cell splits inside this pair of types. A plain
		// DateTime refuses a Decimal, while DateTime64 accepts it.
		// oneDateTimeOneDecimal applies that narrow refusal below.
		return comparisonClassTemporal | comparisonClassNumeric
	case "uuid":
		return comparisonClassUUID
	case "ipv4", "ipv6":
		return comparisonClassIP
	case "array":
		return comparisonClassArray
	case "tuple":
		return comparisonClassTuple
	case "map":
		return comparisonClassMap
	case "variant":
		return comparisonClassVariant
	case "json":
		return comparisonClassJSON
	default:
		// Not covered by the sweep. SimpleAggregateFunction and
		// AggregateFunction land here, and so does every type that
		// the fixture holds no column of.
		return 0
	}
}

// comparableBaseTypes reports whether ClickHouse can compare the two
// base types. A type that the sweep did not cover keeps the pair
// accepted, because an unknown is not evidence of a refusal.
func comparableBaseTypes(left, right CHType) bool {
	if left.Name == "" || right.Name == "" {
		return true
	}
	// Geometry aliases participate as their Array/Tuple structures, not
	// as unknown scalar types that bypass the comparability boundary.
	left = withoutGeometryAliases(left)
	right = withoutGeometryAliases(right)
	// Interval counts compare with integers and other intervals, but
	// not with Decimal, Date, Date32 or DateTime64 column values. This
	// boundary is symmetric and must precede the unknown-class fallback.
	for _, pair := range [][2]CHType{{left, right}, {right, left}} {
		if !strings.HasPrefix(pair[0].normalizedName(), "interval") {
			continue
		}
		switch pair[1].normalizedName() {
		case "date", "date32", "datetime64":
			return false
		}
		if arithmeticDecimalType(pair[1]) {
			return false
		}
	}
	// An AggregateFunction state is NOT an unknown. It is measurably
	// incomparable with EVERY type, itself included. Measured on
	// 25.8.29.51 over real columns:
	//
	//	agg = agg2 -> Code: 43     agg = i64 -> Code: 43
	//	agg = s    -> Code: 43     agg > agg2 -> Code: 43
	//
	// Without this fact the pair reached the "not covered by the sweep"
	// branch below and was ACCEPTED, thus chgen answered UInt8 for an
	// expression that the server refuses: a silently wrong type, this
	// project's worst defect class. The type oracle found it as the
	// blindness signatures v3-fn-groupBitOr and v3-window-leadInFrame,
	// on the inner expressions less(agg, i64) and greater(e8, agg).
	//
	// SimpleAggregateFunction is DIFFERENT and must not come here: it
	// compares as its inner type (`sagg = i64` is accepted), and the
	// callers strip that marker before the check.
	if holdsAggregateFunctionState(left) || holdsAggregateFunctionState(right) {
		return false
	}
	// JSON has an exact-type boundary. The full current fixture sweep found no
	// accepted pair between JSON and a different real column type. Two
	// real JSON columns compare. A constant text also compares, but
	// checkComparableOperandExprs handles that constant conversion before
	// this type-pair predicate runs.
	//
	// The two witnesses can disagree for JSON. Measured on ClickHouse
	// 25.8.29.51 with real columns:
	//
	//	JSON vs JSON                UInt8, and execution succeeds
	//	JSON vs Decimal             Code: 43 in both witnesses
	//	JSON vs String or Dynamic   analysis gives a type, execution
	//	                            refuses with Code: 386 or Code: 43
	//
	// The early check is necessary because Dynamic has no comparison
	// class. A class-intersection check alone treats that unknown class as
	// accepted and keeps the silent wrong answer.
	if left.normalizedName() == "json" || right.normalizedName() == "json" {
		return left.normalizedName() == "json" && right.normalizedName() == "json" &&
			left.String() == right.String()
	}
	// A plain DateTime does not compare with a Decimal. DateTime64 is
	// different and keeps the numeric class. Measured on ClickHouse
	// 25.8.29.51 over real columns, in both operand orders:
	//
	//	dt <= dec      Code: 43     dec >= dt      Code: 43
	//	dt64 <= dec    accepted    dec >= dt64    accepted
	//
	// The class table cannot hold this split because DateTime and
	// DateTime64 both compare with integer and float columns. Keep the
	// narrow pair refusal here.
	if oneDateTimeOneDecimal(left, right) {
		return false
	}
	// Dynamic compares with scalar values, but it does not compare with
	// the measured Tuple and Map shapes. These cells were measured on
	// ClickHouse 25.8.29.51 with a real value SELECT:
	//
	//	equals(dyn, tup)              Code: 43
	//	greaterOrEquals(dyn, m_tup)   Code: 43
	//	equals(dyn, dec)              accepted
	//
	// Name only the measured composite types. Do not turn Dynamic into
	// a unary refusal, because that change would reject its legal scalar
	// comparisons.
	if oneDynamicOneMeasuredComposite(left, right) {
		return false
	}
	// A Dynamic value can compare with a scalar value, but it cannot
	// compare with a Variant. Analysis gives Nullable(UInt8) for this
	// pair, while every value operation refuses it with Code 43. Keep
	// this pair before the unknown-class fallback because Dynamic has no
	// comparison class.
	if oneDynamicOneVariant(left, right) {
		return false
	}
	// An Enum refuses a Decimal, and only a Decimal, among every other
	// type that shares comparisonClassNumeric with it. Measured on
	// ClickHouse 25.8.29.51 over real columns, both operand orders,
	// confirmed by a real "SELECT e8 >= x FROM probe":
	//
	//	e8 >= d128   Code: 43     d128 >= e8   Code: 43
	//	e8 >= dec    Code: 43     dec >= e8    Code: 43
	//	e8 >= i64    OK           e8 >= f64    OK
	//	e8 >= u8     OK           e8 >= i128   OK
	//
	// Every other numeric type keeps comparing with an Enum, INCLUDING
	// Int128, Float64 and a fractional literal, thus the refusal must
	// name the Decimal type alone and not widen to "Enum vs a number":
	// comparisonClassNumeric keeps holding both Enum and Decimal so that
	// each still compares with the plain integers and floats, and this
	// one pair is carved out here instead.
	if oneEnumOneDecimal(left, right) {
		return false
	}
	leftClasses := comparisonClassesOf(left)
	rightClasses := comparisonClassesOf(right)
	if leftClasses == 0 || rightClasses == 0 {
		return true
	}
	if leftClasses&rightClasses == 0 {
		return false
	}
	// A Variant compares only with the exact same Variant type. A
	// different set of alternatives is not a common comparison domain.
	// Measured on ClickHouse 25.8.29.51 with real columns and both
	// witnesses:
	//
	//	Variant(Int32, String) vs Variant(Int32, String)  accepted
	//	Variant(Int32, String) vs Variant(Int32, UInt64)  Code: 43
	//	Variant(Int32, String) vs Int32                   Code: 43
	//
	// The recursive Array and Tuple checks call this same predicate, so
	// an incompatible Variant descendant refuses at its own node.
	if leftClasses == comparisonClassVariant && rightClasses == comparisonClassVariant {
		return left.String() == right.String()
	}
	// Two Arrays share comparisonClassArray, but the class table alone
	// is too coarse: it says only "both are arrays" and not "their
	// elements compare". The element check is recursive and reuses this
	// same function, because an Array of Arrays must resolve down to a
	// scalar pair before the class table can answer anything.
	//
	// Measured on ClickHouse 25.8.29.51 over real columns of one table
	// (a_i32 Array(Int32), a_i64 Array(Int64), a_s Array(String),
	// a_dec Array(Decimal(18,4)), a_f64 Array(Float64),
	// a_aai32 Array(Array(Int32)), a_aai64 Array(Array(Int64)),
	// a_aas Array(Array(String))), confirmed by a real
	// "SELECT equals(a, b) FROM t" and not toTypeName alone:
	//
	//	equals(a_i32, a_i64)     1          elements share the numeric class
	//	equals(a_i32, a_s)       Code: 43   elements share no class
	//	equals(a_aai32, a_aai64) 1          nested elements share the
	//	                                    numeric class, two levels down
	//	equals(a_aai32, a_aas)   Code: 43   nested elements share no class
	//
	// The rule is exactly "two Arrays compare when their element types
	// compare by this same predicate", stated once and applied at every
	// depth, and NOT "the element types must be equal": Array(Int32) and
	// Array(Int64) compare because Int32 and Int64 share the numeric
	// class, the same class rule that already lets a bare Int32 column
	// compare with a bare Int64 column.
	//
	// The element check is not "the element types must compare by the
	// class table". It is "the element types must have a common
	// supertype". A comparison of two Arrays needs a shared element type
	// to compare element-by-element; a bare scalar comparison does not
	// need one. Where the element pair has no supertype, the Array
	// comparison has nothing to compare, even when the SAME pair, as a
	// bare scalar pair, still runs.
	//
	// This supertype test replaces a hand-written list of wide integer and
	// Decimal type names. A fixed list cannot follow a server change.
	// commonCHType already computes the server's supertype lattice. The
	// greatest, least, Array, and Map rules use the same function. Thus, the
	// Array comparability rule asks that function the question directly
	// instead of keeping its own copy of the boundary.
	//
	// Measured on ClickHouse 25.8.29.51 over real columns, both
	// directions, confirmed by a real "SELECT equals(a, b) FROM t" and
	// not by toTypeName alone. The supertype witness (arrayConcat)
	// predicts the comparison outcome in every cell:
	//
	//	elements                      arrayConcat            comparison
	//	Int8/16/32, UInt8/16/32 vs Float32 or Float64
	//	                              Array(Float32 or Float64)  compares
	//	DateTime vs Date              Array(DateTime)        compares
	//	Date vs Date32                Array(Date32)          compares
	//	DateTime vs Date32            Array(DateTime64(0))   compares
	//	DateTime vs DateTime64(3)     Array(DateTime64(3))   compares
	//	Decimal(9,2) vs Decimal(38,4) Array(Decimal(38,4))   compares
	//	Decimal(9,2) vs Int32/64/UInt64
	//	                              Array(Decimal(...))    compares
	//	Enum8 vs Float32/Float64/String
	//	                              Array(Float.../String) compares
	//	String vs FixedString        Array(String)          compares
	//	Nullable(Int32) vs Float64    Array(Nullable(Float64))  compares
	//	LowCardinality(Int32) vs Float64  Array(Float64)     compares
	//	Int64, UInt64, Int128, Int256, UInt128 vs Float32/Float64
	//	                              Code 386               Code 43
	//	Decimal(9,2) vs Float64       Code 386               Code 43
	//	Decimal(38,4) vs Int128       Code 386               Code 43
	//	Enum8 vs Decimal(9,2)         Code 386               Code 43
	//
	// The Code 386 message names the cause, for example: "There is no
	// supertype for types Float64, Int64 because some of them are
	// integers and some are floating point, but there is no floating
	// point type, that can exactly represent all required integers". An
	// Int64 does not convert to a Float64 without a loss of precision.
	//
	// Every cell above was measured with commonCHType too, over the
	// SAME pairs, and it agrees with the server on every one: it widens
	// Int32/Float64 to Float64 and refuses Int64/Float64, DateTime and
	// Date to DateTime, Decimal(9,2) and Decimal(38,4) to Decimal(38,4)
	// and refuses Decimal(38,4) and Int128, and so on. commonCHType is
	// therefore reused here rather than kept as a separate predicate,
	// because a second copy of the boundary is exactly the shape that
	// went stale before.
	if leftClasses == comparisonClassArray && rightClasses == comparisonClassArray {
		if len(left.Params) != 1 || len(right.Params) != 1 {
			// An Array type that inference could not fill in its element
			// is not evidence of an impossible call; see the same
			// reasoning in checkArgumentDomain.
			return true
		}
		leftElem, _, _ := splitCHWrappers(left.Params[0])
		rightElem, _, _ := splitCHWrappers(right.Params[0])
		if _, err := commonCHType(leftElem, rightElem); err != nil {
			return false
		}
		return comparableBaseTypes(leftElem, rightElem)
	}
	// Two Tuples, or two Maps, share their comparison class in the same
	// way as two Arrays. A class-only check answers "both are Tuples" or
	// "both are Maps" and does not inspect a member. Thus, a
	// Tuple(Int32,String) compared equal to a Tuple(String,String) with
	// no member ever typed. This is the worst defect class: chgen answered
	// UInt8 for a pair that the server refuses.
	//
	// A Tuple and a Map do NOT read alike, though. Measured on
	// ClickHouse 25.8.29.51 over real columns, confirmed by a real
	// "SELECT equals(a, b) FROM t" and not by toTypeName alone (the
	// fixture holds tup Tuple(Int32, String), tup_ss Tuple(String,
	// String), tup_ii Tuple(Int32, Int32), tup_i64s Tuple(Int64,
	// String), tup_f64s Tuple(Float64, String), tup_i64f64
	// Tuple(Int64, Float64), tup_i32 Tuple(Int32), tup_i32s_i32
	// Tuple(Int32, String, Int32), tup_u64 Tuple(UInt64, String),
	// tup_i128 Tuple(Int128, String), tup_dec92 Tuple(Decimal(9,2),
	// String), m Map(String, Int64), m_ss Map(String, String), m_is
	// Map(Int32, String), m_sf Map(String, Float64)):
	//
	//	pair                                       comparison
	//	tup           vs tup_ss                    Code 386 (member has no
	//	                                            common class: numeric
	//	                                            vs string-like)
	//	tup           vs tup_ii                     Code 386 (same, other
	//	                                            position)
	//	tup           vs tup_i64s                   compares
	//	tup           vs tup_f64s                   compares
	//	tup_i64f64    vs tup_ii                     compares
	//	tup           vs tup_i32                    Code 43 (differing
	//	                                            arity)
	//	tup           vs tup_i32s_i32                Code 43 (differing
	//	                                            arity)
	//	tup_ii        vs tup_f64s                   Code 386 (position 1
	//	                                            has no common class)
	//	tup_i64f64    vs tup                        Code 386 (position 1
	//	                                            has no common class)
	//	m             vs m_ss                       Code 43
	//	m             vs m_is                       Code 43
	//	m             vs m_sf                       Code 43
	//	m             vs m                          compares
	//
	// So far a Tuple and a Map read alike: same class per position, and a
	// different arity refuses a Tuple. But the wide-integer and Decimal
	// members that Array refuses do NOT refuse inside a
	// Tuple. Measured on the same server, same method:
	//
	//	tup_u64  Tuple(UInt64, String) vs tup Tuple(Int32, String)
	//	                                             compares (0)
	//	tup_i128 Tuple(Int128, String) vs tup_dec92 Tuple(Decimal(9,2), String)
	//	                                             compares (0)
	//
	// even though "SELECT toTypeName(if(1, tup_u64, tup))" gives Code
	// 386, no supertype for Int32 and UInt64. A Tuple position compares
	// like a BARE scalar pair, which already accepts Int32 vs UInt64
	// (both comparisonClassNumeric), and does not additionally demand a
	// supertype. Map does demand it, measured the same way:
	//
	//	mk_i32 Map(Int32, String) vs mk_u64 Map(UInt64, String)
	//	                                             Code 43 (key)
	//	m_i32  Map(String, Int32) vs m_u64 Map(String, UInt64)
	//	                                             Code 43 (value)
	//	m_i128 Map(String, Int128) vs m_dec92 Map(String, Decimal(9,2))
	//	                                             Code 43 (value)
	//
	// and Map's value type reads exactly like an Array element on every
	// measured witness cell (Int32/Float64
	// widens, Date/DateTime widens, Decimal(9,2)/Decimal(38,4) widens,
	// Decimal(9,2)/Int64 refuses), confirmed again here on m_i32/m_f64,
	// m_date/m_dt, m_dec9/m_dec38 and m_dec9/m_i64_2.
	//
	// The fix therefore gives Tuple a per-position recursive call with
	// NO extra supertype gate, and gives Map the Array treatment (the
	// supertype gate, then the recursive call) on BOTH the key and the
	// value. A Tuple element name does not change the result: measured
	// "tup_named Tuple(a Int32, b String) vs tup Tuple(Int32, String)"
	// compares, so ParamNames plays no part here.
	//
	// A nested container inside a Tuple or a Map member still resolves
	// through the SAME recursive call, so a Tuple(Array(Int32)) vs a
	// Tuple(Array(UInt64)) refuses: the Array rule inside the recursion
	// demands the supertype that Int32/UInt64 does not have, confirmed
	// with "SELECT equals(tup_au64, tup_ai64) FROM tw" giving Code 43.
	if leftClasses == comparisonClassTuple && rightClasses == comparisonClassTuple {
		if len(left.Params) != len(right.Params) {
			// A differing arity is not "not covered by the sweep"; the
			// server refuses it with Code 43, measured above.
			return false
		}
		if len(left.Params) == 0 {
			// An empty Tuple type is not evidence of an impossible
			// call; see the same reasoning as the Array branch above
			// for a type inference could not fill in.
			return true
		}
		for i := range left.Params {
			leftElem, _, _ := splitCHWrappers(left.Params[i])
			rightElem, _, _ := splitCHWrappers(right.Params[i])
			if !comparableBaseTypes(leftElem, rightElem) {
				return false
			}
		}
		return true
	}
	if leftClasses == comparisonClassMap && rightClasses == comparisonClassMap {
		if len(left.Params) != 2 || len(right.Params) != 2 {
			// A Map type that inference could not fill in its key and
			// value is not evidence of an impossible call; see the
			// same reasoning in the Array branch above.
			return true
		}
		for i := 0; i < 2; i++ {
			leftElem, _, _ := splitCHWrappers(left.Params[i])
			rightElem, _, _ := splitCHWrappers(right.Params[i])
			if _, err := commonCHType(leftElem, rightElem); err != nil {
				return false
			}
			if !comparableBaseTypes(leftElem, rightElem) {
				return false
			}
		}
		return true
	}
	return true
}

func oneDateTimeOneDecimal(left, right CHType) bool {
	return left.normalizedName() == "datetime" && arithmeticDecimalType(right) ||
		right.normalizedName() == "datetime" && arithmeticDecimalType(left)
}

func oneDynamicOneMeasuredComposite(left, right CHType) bool {
	isComposite := func(value CHType) bool {
		switch value.normalizedName() {
		case "tuple", "map":
			return true
		default:
			return false
		}
	}
	return left.normalizedName() == "dynamic" && isComposite(right) ||
		right.normalizedName() == "dynamic" && isComposite(left)
}

func oneDynamicOneVariant(left, right CHType) bool {
	return left.normalizedName() == "dynamic" && right.normalizedName() == "variant" ||
		right.normalizedName() == "dynamic" && left.normalizedName() == "variant"
}

// oneEnumOneDecimal reports whether the pair is one Enum and one Decimal,
// in either order. See the measured note in comparableBaseTypes above.
func oneEnumOneDecimal(left, right CHType) bool {
	return (enumBaseType(left) && arithmeticDecimalType(right)) ||
		(enumBaseType(right) && arithmeticDecimalType(left))
}

// holdsAggregateFunctionState reports whether the type is an
// AggregateFunction state. SimpleAggregateFunction is deliberately NOT
// one of these: it carries a normal value and compares as that value.
//
// The name arrives in TWO spellings and the predicate must accept both.
// The schema parser stores a column as the bare name "AggregateFunction"
// and drops the parameters, while an inferred type carries them as
// "AggregateFunction(uniq, UInt64)". A predicate written on one spelling
// alone reads as working while it never fires on real schema columns,
// which is how this rule first passed its unit probe and still let
// `agg = i64` through.
func holdsAggregateFunctionState(t CHType) bool {
	return t.Name == "AggregateFunction" || strings.HasPrefix(t.Name, "AggregateFunction(")
}

// A CONSTANT operand is comparable with everything, thus the check must
// see the expressions and not only the types.
//
// ClickHouse parses a constant of one type into the type of the other
// operand, and the pair then compares. Measured on 25.8.29.51 with the
// SECOND witness, that is a real "SELECT e FROM t" and never toTypeName
// alone:
//
//	dec <= s          Code: 43, "No operation lessOrEquals between
//	                  Decimal(18, 4) and String"      (a COLUMN String)
//	dec <= '1.5'      1                               (a CONSTANT String)
//	nullIf(dec, s)    Code: 43                        (a COLUMN String)
//	nullIf(dec, toString(-129))
//	                  1.2345                          (a CONSTANT String)
//	i8 = s            Code: 386                       (a COLUMN String)
//	i8 = '1'          0                               (a CONSTANT String)
//	uid = '61f0c404-5cb3-11e7-907b-a6006ad3dba0'
//	                  1
//	ip4 = '1.2.3.4'   1
//	d = '2024-01-02'  1
//
// This is the constant-folding trap itself: a rule measured over
// literals reports the wrong boundary. The class rule therefore applies
// only when BOTH operands are non-constant.
//
// A constant of a composite type is still refused, but at EXECUTION and
// with another cause ("arr_i = '1'" gives Code: 130, "Array does not
// start with '[' character"). That is a parse failure of the constant
// and not an incomparable pair, thus this rule leaves it alone.

// checkComparableOperandExprs refuses a pair of operands that the server
// refuses to compare. A constant usually folds into the type of the
// other side, thus a constant operand usually exempts the pair.
//
// The exemption is DIRECTIONAL. A String constant folds into every
// column type, but a NUMERIC constant does not fold into a column that
// holds text or a calendar date. Measured on ClickHouse 25.8.29.51 with
// real columns and a VALUE select:
//
//	NUMERIC constant, column on the left
//	  s  > 1    Code: 386   no supertype for String, UInt8
//	  fs > 1    Code: 386   no supertype for FixedString(4), UInt8
//	  d  > 1    Code: 43    illegal types (Date, UInt8)
//	  u  > 1    Code: 43    illegal types (UUID, UInt8)
//	  e8 > 1    0           an Enum compares with a number
//	  ip > 1    1
//	  dt > 1    1
//
//	STRING constant, column on the left
//	  i32 > '1'   1
//	  dec > '1'   1
//	  d   > '2024-01-01'   1
//	  u   > '<uuid>'       0
//	  ip  > '1.2.3.4'      0
//
// A String constant therefore keeps the blanket exemption. A numeric
// constant is judged by the SAME measured class table that a column
// pair uses, so this adds a rule and not a second table: the constant
// is treated as the numeric type it is written as.
func checkComparableOperandExprs(
	displayName string,
	leftExpr, rightExpr clickhouse.Expr,
	left, right CHType,
	scope queryScope,
) error {
	leftConst := isConstLiteralExpr(leftExpr, scope)
	rightConst := isConstLiteralExpr(rightExpr, scope)
	if !leftConst && !rightConst {
		return checkComparableOperands(displayName, left, right)
	}
	// A pair of constants folds whole and never reaches the server as a
	// pair of types.
	if leftConst && rightConst {
		return nil
	}
	constType, columnType := left, right
	if rightConst {
		constType, columnType = right, left
	}
	// Only a NUMERIC constant can be refused. Every other constant
	// keeps the blanket exemption, because the sweep above shows the
	// String constant folding into every column type.
	constBase, _, _ := splitCHWrappers(constType)
	if comparisonClassesOf(constBase) != comparisonClassNumeric {
		return nil
	}
	return checkComparableOperands(displayName, columnType, constType)
}

// checkHasElementPair refuses a has(haystack, needle) call whose needle
// cannot compare with the ELEMENT of the haystack. It does nothing for
// every other function, so that the one caller needs no name test.
//
// hasArgumentDomain judges the haystack alone, and the result type is a
// fixed Bool. Without this check chgen answers Bool for a call that the
// server refuses.
//
// Measured on ClickHouse 25.8.29.51 with real columns and a VALUE
// select, never toTypeName alone:
//
//	has(arr_i32, i32)       0
//	has(arr_i32, arr_i32)   Code: 386   no supertype for Int32,
//	                                    Array(Int32)
//	has(arr_s, arr_s)       Code: 386   no supertype for String,
//	                                    Array(String)
//	has(arr_s, arr_i32)     Code: 386   no supertype for String,
//	                                    Array(Int32)
//
// This is the pair refusal that hasArgumentDomain names but does not
// state, because Code: 386 is about the PAIR and not about the domain
// of the first argument.
//
// A Map haystack compares the needle against its KEY, not its value.
// Measured on ClickHouse 25.8.29.51 with real columns and a VALUE
// select (m is Map(String, Int64), m_is is Map(Int32, String)):
//
//	has(m, arr_i)     Code: 386   no supertype for String, Array(Int32)
//	has(m_is, arr_i)  Code: 386   no supertype for Int32, Array(Int32)
//	has(m, i32)       Code: 386   no supertype for String, Int32; the
//	                              KEY is String, thus an Int32 needle
//	                              is refused although the Map VALUE is
//	                              Int64
//	has(m, s)         UInt8       String needle against a String key
//	has(m_is, i32)    UInt8       Int32 needle against an Int32 key
//
// A haystack that is neither an Array nor a Map keeps its domain
// refusal from hasArgumentDomain.
func checkHasElementPair(
	displayName, name string,
	args []clickhouse.Expr,
	argTypes []CHType,
	scope queryScope,
) error {
	if name != "has" || len(args) != 2 || len(argTypes) != 2 {
		return nil
	}
	haystack, _, _ := splitCHWrappers(argTypes[0])
	if inner, ok := simpleAggregateWrapperInner(haystack); ok {
		haystack, _, _ = splitCHWrappers(inner)
	}
	var element CHType
	switch {
	case arrayBaseType(haystack) && len(haystack.Params) == 1:
		element, _, _ = splitCHWrappers(haystack.Params[0])
	case haystack.normalizedName() == "map" && len(haystack.Params) == 2:
		// The element to compare is the KEY, Params[0]. The Map VALUE,
		// Params[1], never takes part in this check.
		element, _, _ = splitCHWrappers(haystack.Params[0])
	default:
		return nil
	}
	// The needle carries its own wrappers. comparisonClassesOf knows no
	// SimpleAggregateFunction, thus a marker left on the needle would
	// answer "not covered by the sweep" and the pair would be accepted.
	// The marker comes off here for the same reason as on the haystack:
	// the server compares the value inside it.
	needle, _, _ := splitCHWrappers(argTypes[1])
	if inner, ok := simpleAggregateWrapperInner(needle); ok {
		needle, _, _ = splitCHWrappers(inner)
	}
	return checkComparableOperandExprs(
		"function "+displayName,
		args[0], args[1], element, needle, scope,
	)
}

// checkComparableOperands refuses a pair of operand types that the server
// refuses to compare. displayName names the operator or the function, so
// that the message points at the expression that the user must change.
//
// The caller must rule out a constant operand first; see
// checkComparableOperandExprs.
//
// An operand type that inference could not fill in stays accepted, for
// the same reason as in checkArgumentDomain: an empty type says "not
// known yet" and not "impossible".
func checkComparableOperands(displayName string, left, right CHType) error {
	leftBase, _, _ := splitCHWrappers(left)
	rightBase, _, _ := splitCHWrappers(right)
	if comparableBaseTypes(leftBase, rightBase) {
		return nil
	}
	return fmt.Errorf(
		"%s cannot compare an operand of type %s with an operand of type %s; ClickHouse refuses this pair, because the two types are not in one comparable family; %s",
		displayName, left.String(), right.String(), pinTypeHint,
	)
}
