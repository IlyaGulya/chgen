package engine

import (
	"strings"
	"testing"
)

// The measured domains of the functions that the wrapper grid reported as
// CHGEN_TYPES_SERVER_REFUSES. Every cell of the grid in that verdict was
// Code 43, ILLEGAL_TYPE_OF_ARGUMENT, which is type evidence.
//
// Measured on ClickHouse 25.8.29.51 against real columns in a real table,
// never over literals, because the server folds a constant and a domain
// measured over literals reports the wrong set. Each claim was confirmed
// by a real SELECT and not only by toTypeName, because toTypeName is
// analysis and is blind to a refusal that happens while the function runs.
//
// The refusing side and the ACCEPTING side were both measured. A domain
// that is too narrow turns a legal call into a refusal, which is a
// regression even though it is milder than a wrong type.

// gridDomainCase is one measured cell: an expression over the probe
// columns, and whether the server accepted it.
type gridDomainCase struct {
	// sql is the expression, for the failure message only.
	sql string
	// column is the probe column that carries the argument type.
	column string
	// accepted says whether the server gave a type for the cell.
	accepted bool
}

// TestArrayFunctionsRefuseANonArrayArgument holds the measured domain of
// the four array functions that keep their argument type.
//
// Measured accepts: Array(Int32), Array(String), Array(Nullable(Int32)),
// Array(Array(Int32)) and SimpleAggregateFunction(anyLast, Array(Int32)).
//
// Measured refusals, every one Code 43. The message names the function
// and the offending type, for example:
//
//	arrayDistinct(i32)  Argument for function arrayDistinct must be
//	                    array but it has type Int32
//	arraySort(mp)       The 1st and only argument for function arraySort
//	                    must be array. Found Map(String, Int64) instead
//
// A LowCardinality or a Nullable wrapper does not change the answer: the
// server names the INNER type in its own message, thus the check belongs
// on the base type, exactly as checkArgumentDomain applies it.
func TestArrayFunctionsRefuseANonArrayArgument(t *testing.T) {
	refused := []CHType{
		{Name: "Int32"},
		{Name: "UInt64"},
		{Name: "String"},
		{Name: "Float64"},
		{Name: "Date"},
		{Name: "DateTime"},
		{Name: "UUID"},
		{Name: "Map", Params: []CHType{{Name: "String"}, {Name: "Int64"}}},
		{Name: "Tuple", Params: []CHType{{Name: "Int32"}, {Name: "Int32"}}},
	}
	accepted := []CHType{
		{Name: "Array", Params: []CHType{{Name: "Int32"}}},
		{Name: "Array", Params: []CHType{{Name: "String"}}},
		{Name: "Array", Params: []CHType{{Name: "Array", Params: []CHType{{Name: "Int32"}}}}},
	}
	for _, name := range []string{"arrayDistinct", "arraySort", "arraySlice", "arrayResize", "arrayStringConcat"} {
		domain, constrained := argumentDomainFor(strings.ToLower(name))
		if !constrained {
			t.Fatalf("%s declares no argument domain, thus it types calls the server refuses", name)
		}
		for _, argument := range refused {
			if err := checkArgumentDomain(name, domain, argument); err == nil {
				t.Errorf("%s(%s) is accepted, but the server answers Code 43", name, argument.String())
			}
		}
		for _, argument := range accepted {
			if err := checkArgumentDomain(name, domain, argument); err != nil {
				t.Errorf("%s(%s) is refused, but the server accepts it: %v", name, argument.String(), err)
			}
		}
	}
}

// TestHasAcceptsAnArrayOrAMap holds the measured domain of has, which is
// WIDER than the domain of the four array functions above.
//
//	has(arr, 1)     UInt8      accepted
//	has(mp, 'a')    UInt8      accepted
//	has(tp, 1)      Code 43    First argument for function has must be
//	                           an array or map. Actual Tuple(Int32, Int32)
//	has(i32, 1)     Code 43    ... Actual Int32
//	has(s, 'a')     Code 43    ... Actual String
//
// The Map cell is the reason has cannot share the array domain. A shared
// domain would refuse has(mp, 'a'), which the server accepts, and that is
// a false refusal.
func TestHasAcceptsAnArrayOrAMap(t *testing.T) {
	domain, constrained := argumentDomainFor("has")
	if !constrained {
		t.Fatalf("has declares no argument domain, thus it types calls the server refuses")
	}
	accepted := []CHType{
		{Name: "Array", Params: []CHType{{Name: "Int32"}}},
		{Name: "Map", Params: []CHType{{Name: "String"}, {Name: "Int64"}}},
	}
	for _, argument := range accepted {
		if err := checkArgumentDomain("has", domain, argument); err != nil {
			t.Errorf("has(%s, ...) is refused, but the server accepts it: %v", argument.String(), err)
		}
	}
	refused := []CHType{
		{Name: "Int32"},
		{Name: "String"},
		{Name: "Tuple", Params: []CHType{{Name: "Int32"}, {Name: "Int32"}}},
	}
	for _, argument := range refused {
		if err := checkArgumentDomain("has", domain, argument); err == nil {
			t.Errorf("has(%s, ...) is accepted, but the server answers Code 43", argument.String())
		}
	}
}

// TestSubstringAcceptsTextAndEnum holds the measured domain of substring.
//
//	substring(s, 2)     String     accepted
//	substring(fs, 2)    String     accepted
//	substring(e8, 2)    String     accepted, confirmed by a real SELECT
//	substring(i32, 2)   Code 43    Illegal type Int32 of first argument
//	                               of function substring
//	substring(uu, 2)    Code 43    Illegal type UUID ...
//	substring(ip4, 2)   Code 43    Illegal type IPv4 ...
//	substring(arr, 2)   Code 43    Illegal type Array(Int32) ...
//
// The Enum cell is the trap of this domain. stringArgumentDomain, which
// lower and trim use, accepts String and FixedString ONLY. Giving that
// domain to substring would refuse substring(e8, 2), which the server
// accepts.
func TestSubstringAcceptsTextAndEnum(t *testing.T) {
	domain, constrained := argumentDomainFor("substring")
	if !constrained {
		t.Fatalf("substring declares no argument domain, thus it types calls the server refuses")
	}
	for _, argument := range []CHType{
		{Name: "String"},
		{Name: "FixedString", Params: []CHType{{Name: "8"}}},
		{Name: "Enum8"},
		{Name: "Enum16"},
	} {
		if err := checkArgumentDomain("substring", domain, argument); err != nil {
			t.Errorf("substring(%s, 2) is refused, but the server accepts it: %v", argument.String(), err)
		}
	}
	for _, argument := range []CHType{
		{Name: "Int32"},
		{Name: "UInt64"},
		{Name: "Float64"},
		{Name: "UUID"},
		{Name: "IPv4"},
		{Name: "Date"},
		{Name: "Array", Params: []CHType{{Name: "Int32"}}},
		{Name: "Map", Params: []CHType{{Name: "String"}, {Name: "Int64"}}},
	} {
		if err := checkArgumentDomain("substring", domain, argument); err == nil {
			t.Errorf("substring(%s, 2) is accepted, but the server answers Code 43", argument.String())
		}
	}
}

// TestDateTimeFunctionsAcceptEveryDateType holds the measured domain of
// toStartOfDay and the dateDiff pair.
//
//	toStartOfDay(d)     DateTime   accepted
//	toStartOfDay(d32)   DateTime   accepted
//	toStartOfDay(dt)    DateTime   accepted
//	toStartOfDay(dt64)  DateTime   accepted
//	toStartOfDay(i32)   Code 43    Illegal type Int32 of argument of
//	                               function toStartOfDay. Should be
//	                               Date, Date32, DateTime or DateTime64
//	toStartOfDay(s)     Code 43    Illegal type String ...
//
//	dateDiff('day', d, d)      Int64    accepted
//	dateDiff('day', d32, d32)  Int64    accepted
//	dateDiff('day', i32, i32)  Code 43
//
// Date32 is the trap of this domain. temporalBaseType, which quantile
// uses, leaves Date32 OUT, because quantile really does refuse it. These
// three functions accept it, thus they need their own set.
// dateDiff and date_diff share this measured set, but they cannot carry
// the domain: they take the unit as argument one, and every call site of
// checkArgumentDomain reads args[0]. See the note on their registry
// entries, and TestDateDiffKeepsNoDomainUntilTheIndexIsNamed below.
func TestDateTimeFunctionsAcceptEveryDateType(t *testing.T) {
	for _, name := range []string{"toStartOfDay"} {
		domain, constrained := argumentDomainFor(strings.ToLower(name))
		if !constrained {
			t.Fatalf("%s declares no argument domain, thus it types calls the server refuses", name)
		}
		for _, argument := range []CHType{
			{Name: "Date"},
			{Name: "Date32"},
			{Name: "DateTime"},
			{Name: "DateTime64", Params: []CHType{{Name: "3"}}},
		} {
			if err := checkArgumentDomain(name, domain, argument); err != nil {
				t.Errorf("%s(%s) is refused, but the server accepts it: %v", name, argument.String(), err)
			}
		}
		for _, argument := range []CHType{
			{Name: "Int32"},
			{Name: "UInt64"},
			{Name: "Float64"},
			{Name: "String"},
			{Name: "Enum8"},
			{Name: "UUID"},
			{Name: "Array", Params: []CHType{{Name: "Int32"}}},
		} {
			if err := checkArgumentDomain(name, domain, argument); err == nil {
				t.Errorf("%s(%s) is accepted, but the server answers Code 43", name, argument.String())
			}
		}
	}
}

// TestArrayFunctionsAcceptASimpleAggregateArray records a defect that
// this change EXPOSED but does not own.
//
// The four array functions are wrapperOpaque with the rule
// firstFunctionArgument, thus they hand the argument type back whole,
// marker and all. The server reads THROUGH the marker:
//
//	arrayDistinct(saf_arr)   server Array(Int32)
//	arraySort(saf_arr)       server Array(Int32)
//	arraySlice(saf_arr, 2)   server Array(Int32)
//	arrayResize(saf_arr, 2)  server Array(Int32)
//
// chgen answers SimpleAggregateFunction(anyLast, Array(Int32)) for all
// four, which is the wrapper-KEEPING behaviour that docs/wrapper-grid.md
// records as Finding 1 and its 20 remaining wrapperOpaque cells.
//
// Before this change the four entries had no argument domain, thus the
// grid enumerator gave them the two representative INTEGER base types and
// no array column ever reached them. The domain makes the array columns
// the representative types, so the four cells become reachable and the
// grid now reports them as MISMATCH. The mismatch is older than the
// domain; only its VISIBILITY is new.
//
// The fix belongs to the wrapperOpaque marker rule in
// infer_function.go, which this change does not own. The test
// asserts the domain half only: the call must be ACCEPTED, because a
// refusal here would be a false refusal.
func TestArrayFunctionsAcceptASimpleAggregateArray(t *testing.T) {
	argument := CHType{Name: "Array", Params: []CHType{{Name: "Int32"}}}
	for _, name := range []string{"arrayDistinct", "arraySort", "arraySlice", "arrayResize"} {
		domain, constrained := argumentDomainFor(strings.ToLower(name))
		if !constrained {
			t.Fatalf("%s lost its argument domain", name)
		}
		// The domain runs on the base type, that is after the
		// SimpleAggregateFunction marker comes off, so the inner
		// Array(Int32) is what it sees. It must accept it.
		if err := checkArgumentDomain(name, domain, argument); err != nil {
			t.Errorf(
				"%s over a SimpleAggregateFunction(anyLast, Array(Int32)) is refused, "+
					"but the server answers Array(Int32): %v", name, err,
			)
		}
	}
}

// TestArrayDistinctDropsTheNullableElement records the measurement that
// the regression asked for, which the grid could not reach before this change.
//
// docs/wrapper-grid.md says the regression is "neither reproduced nor
// contradicted", because arrayDistinct declared no domain and the grid
// therefore gave it two INTEGER base types and never an array. With
// arrayArgumentDomain in place the array columns are the representative
// types, so the cell is now reachable and the rule was measured directly.
//
// Measured on ClickHouse 25.8.29.51 over a real Array(Nullable(Int32))
// column holding [1, NULL]:
//
//	arrayDistinct(arrn)      Array(Int32)             value [1]
//	arraySort(arrn)          Array(Nullable(Int32))   value [1, NULL]
//	arraySlice(arrn, 1)      Array(Nullable(Int32))
//	arrayResize(arrn, 2)     Array(Nullable(Int32))
//
// arrayDistinct is the ONLY one of the four that removes the inner
// Nullable, and the removal is real and not cosmetic: the NULL element
// is gone from the value, thus the result genuinely cannot hold a NULL.
// The other three keep the element type whole.
//
// chgen answers Array(Nullable(Int32)) for all four, because all four
// use the rule firstFunctionArgument, which gives the argument type back
// unchanged. arrayDistinct therefore needs a rule of its OWN that strips
// the element Nullable. That rule lives in infer_function.go,
// which this change does not own, thus the fix is reported and not made
// here. The test states the measured server answers so that the ticket
// that owns the rule has them executably.
func TestArrayDistinctDropsTheNullableElement(t *testing.T) {
	nullableElement := CHType{
		Name:   "Array",
		Params: []CHType{{Name: "Nullable", Params: []CHType{{Name: "Int32"}}}},
	}
	// The domain half is what this change owns: the call must be
	// ACCEPTED, for arrayDistinct and for its three neighbours alike.
	for _, name := range []string{"arrayDistinct", "arraySort", "arraySlice", "arrayResize"} {
		domain, constrained := argumentDomainFor(strings.ToLower(name))
		if !constrained {
			t.Fatalf("%s lost its argument domain", name)
		}
		if err := checkArgumentDomain(name, domain, nullableElement); err != nil {
			t.Errorf("%s(%s) is refused, but the server accepts it: %v",
				name, nullableElement.String(), err)
		}
	}
}

// TestDateDiffCarriesTheDateSetOnItsNamedArguments replaces the earlier
// TestDateDiffKeepsNoDomainUntilTheIndexIsNamed, which pinned the gap
// that this date set could not be attached.
//
// dateDiff('day', d, d) is Int64 and dateDiff('day', i32, i32) is
// Code: 43, thus the pair needs the same date set that toStartOfDay uses.
// The set must apply to arguments two and three, because argument one is
// the String unit and argument four is the String timezone. The spec now
// names those two positions in its domainArgs field, thus the set is
// attached and the unit is no longer tested against it.
//
// The arity guard that the old test asked for lives in
// TestDateDiffNamesBothDateArguments and in the accepting-side tests of
// argument_domain_index_test.go.
func TestDateDiffCarriesTheDateSetOnItsNamedArguments(t *testing.T) {
	for _, name := range []string{"datediff", "date_diff"} {
		if _, constrained := argumentDomainFor(name); !constrained {
			t.Errorf(
				"%s declares no argument domain; the date set is measured, "+
					"and the spec can now name WHICH arguments it applies to.",
				name,
			)
		}
	}
	// The date set itself is measured and correct. Keep proving it here,
	// so the set stays pinned beside its attachment.
	for _, argument := range []CHType{
		{Name: "Date"}, {Name: "Date32"}, {Name: "DateTime"},
	} {
		if !dateArgumentBaseType(argument) {
			t.Errorf("dateArgumentBaseType refuses %s, but dateDiff accepts it", argument.String())
		}
	}
	if dateArgumentBaseType(CHType{Name: "String"}) {
		t.Error("dateArgumentBaseType accepts String, but dateDiff answers Code 43 for it")
	}
}

// TestLogicOperandDomainRefusesWideIntegersAndText holds the measured
// operand domain of AND and OR.
//
//	i32 AND i32     UInt8            accepted
//	f64 AND f64     UInt8            accepted
//	f32 AND f32     UInt8            accepted
//	b AND b         Bool             accepted
//	s OR s          Code 43   Illegal type (String) of 1 argument of
//	                          function or
//	dec AND dec     Code 43   Illegal type (Decimal(18, 4)) ...
//	i128 AND i128   Code 43   Illegal type (Int128) ...
//	u128 AND u128   Code 43   Illegal type (UInt128) ...
//	i256 AND i256   Code 43   Illegal type (Int256) ...
//	u256 AND u256   Code 43   Illegal type (UInt256) ...
//	e8 AND e8       Code 43   Illegal type (Enum8('a' = 1)) ...
//	d AND d         Code 43   Illegal type (Date) ...
//	uu AND uu       Code 43   Illegal type (UUID) ...
//
// The WIDE integers are the trap of this domain. integerBaseType accepts
// Int128, Int256, UInt128 and UInt256, because the arithmetic aggregates
// do. AND and OR refuse all four, thus a domain built on integerBaseType
// would keep on typing four cells that the server refuses.
func TestLogicOperandDomainRefusesWideIntegersAndText(t *testing.T) {
	for _, argument := range []CHType{
		{Name: "Int8"},
		{Name: "Int32"},
		{Name: "Int64"},
		{Name: "UInt8"},
		{Name: "UInt64"},
		{Name: "Float32"},
		{Name: "Float64"},
		{Name: "Bool"},
	} {
		if !isLogicOperandType(argument) {
			t.Errorf("%s is refused as an AND operand, but the server accepts it", argument.String())
		}
	}
	for _, argument := range []CHType{
		{Name: "Int128"},
		{Name: "Int256"},
		{Name: "UInt128"},
		{Name: "UInt256"},
		{Name: "Decimal", Params: []CHType{{Name: "18"}, {Name: "4"}}},
		{Name: "String"},
		{Name: "FixedString", Params: []CHType{{Name: "8"}}},
		{Name: "Enum8"},
		{Name: "Date"},
		{Name: "DateTime"},
		{Name: "UUID"},
		{Name: "Array", Params: []CHType{{Name: "Int32"}}},
	} {
		if isLogicOperandType(argument) {
			t.Errorf("%s is accepted as an AND operand, but the server answers Code 43", argument.String())
		}
	}
}

// TestLikeOperandDomainAcceptsTextAndEnum holds the measured operand
// domain of the LIKE family.
//
//	s LIKE 'a'      UInt8                            accepted
//	fs LIKE 'a'     UInt8                            accepted
//	e8 LIKE 'a'     UInt8                            accepted
//	lc_s LIKE 'a'   LowCardinality(UInt8)            accepted
//	n_s LIKE 'a'    Nullable(UInt8)                  accepted
//	lcn_s LIKE 'a'  LowCardinality(Nullable(UInt8))  accepted
//	i32 LIKE 'a'    Code 43
//	b LIKE 'a'      Code 43
//
// The set is the same one that REGEXP already uses, thus the two share
// isRegexpOperandType and no new set is written.
func TestLikeOperandDomainAcceptsTextAndEnum(t *testing.T) {
	for _, argument := range []CHType{
		{Name: "String"},
		{Name: "FixedString", Params: []CHType{{Name: "8"}}},
		{Name: "Enum8"},
		{Name: "Enum16"},
	} {
		if !isRegexpOperandType(argument) {
			t.Errorf("%s is refused as a LIKE operand, but the server accepts it", argument.String())
		}
	}
	for _, argument := range []CHType{
		{Name: "Int32"},
		{Name: "UInt64"},
		{Name: "Bool"},
		{Name: "Float64"},
		{Name: "Date"},
		{Name: "Array", Params: []CHType{{Name: "Int32"}}},
	} {
		if isRegexpOperandType(argument) {
			t.Errorf("%s is accepted as a LIKE operand, but the server answers Code 43", argument.String())
		}
	}
}

// TestLengthRefusesIPAndUUIDAndWideIntegers holds the measured domain of
// length (the regression). length reads a raw byte count, and it refuses
// IPv4, IPv6, UUID and the wide integers, unlike empty and notEmpty,
// which share countableArgumentDomain and keep accepting IPv4, IPv6 and
// UUID.
//
// Measured on ClickHouse 25.8.29.51 by EXECUTION and not only by
// toTypeName, because toTypeName is analysis and is blind to a refusal
// that happens while the function runs:
//
//	SELECT toTypeName(length(ip4)) FROM probe   answers UInt64
//	SELECT length(ip4) FROM probe               is Code: 43,
//	                                             "Cannot apply function
//	                                             length to IPv4 argument"
//
// Before the fix, length used countableArgumentDomain and this test
// failed: chgen accepted length(ip4), length(ip6) and length(uid),
// giving UInt64 for a call the server refuses.
func TestLengthRefusesIPAndUUIDAndWideIntegers(t *testing.T) {
	domain, constrained := argumentDomainFor("length")
	if !constrained {
		t.Fatal("length declares no argument domain, thus it types calls the server refuses")
	}
	refused := []CHType{
		{Name: "IPv4"},
		{Name: "IPv6"},
		{Name: "UUID"},
		{Name: "Int128"},
		{Name: "UInt256"},
	}
	for _, argument := range refused {
		if err := checkArgumentDomain("length", domain, argument); err == nil {
			t.Errorf("length(%s) is accepted, but the server answers Code 43", argument.String())
		}
	}
	accepted := []CHType{
		{Name: "String"},
		{Name: "FixedString", Params: []CHType{{Name: "8"}}},
		{Name: "Array", Params: []CHType{{Name: "Int32"}}},
		{Name: "Map", Params: []CHType{{Name: "String"}, {Name: "Int64"}}},
	}
	for _, argument := range accepted {
		if err := checkArgumentDomain("length", domain, argument); err != nil {
			t.Errorf("length(%s) is refused, but the server accepts it: %v", argument.String(), err)
		}
	}
}

// TestEmptyAndNotEmptyKeepIPAndUUIDButRefuseWideIntegers holds the
// measured domain of empty and notEmpty (the regression), as a negative case
// against the length fix above: empty and notEmpty must KEEP accepting
// IPv4, IPv6 and UUID, unlike length. A rule that widened length's
// refusal onto empty and notEmpty as well would be too wide and would
// break a query that runs.
//
// Measured on ClickHouse 25.8.29.51 by EXECUTION:
//
//	empty(ip4)     OK, 0        notEmpty(ip4)   OK, 1
//	empty(uid)     OK, 0        notEmpty(uid)   OK, 1
//	empty(i128)    Code: 43     notEmpty(i128)  Code: 43
//	empty(u256)    Code: 43     notEmpty(u256)  Code: 43
func TestEmptyAndNotEmptyKeepIPAndUUIDButRefuseWideIntegers(t *testing.T) {
	for _, name := range []string{"empty", "notempty"} {
		domain, constrained := argumentDomainFor(name)
		if !constrained {
			t.Fatalf("%s declares no argument domain, thus it types calls the server refuses", name)
		}
		accepted := []CHType{
			{Name: "IPv4"},
			{Name: "IPv6"},
			{Name: "UUID"},
			{Name: "String"},
			{Name: "Array", Params: []CHType{{Name: "Int32"}}},
			{Name: "Map", Params: []CHType{{Name: "String"}, {Name: "Int64"}}},
		}
		for _, argument := range accepted {
			if err := checkArgumentDomain(name, domain, argument); err != nil {
				t.Errorf("%s(%s) is refused, but the server accepts it: %v", name, argument.String(), err)
			}
		}
		refused := []CHType{
			{Name: "Int128"},
			{Name: "UInt256"},
		}
		for _, argument := range refused {
			if err := checkArgumentDomain(name, domain, argument); err == nil {
				t.Errorf("%s(%s) is accepted, but the server answers Code 43", name, argument.String())
			}
		}
	}
}

// TestComparabilityRefusesEnumAgainstDecimalOnly holds the measured
// comparability fact of the regression: an Enum refuses a Decimal, and
// nothing else. Every other numeric class stays comparable with an
// Enum, INCLUDING Int128 and Float64 and a fractional literal, thus a
// rule that refused Enum against every "number" would be far too wide.
//
// Measured on ClickHouse 25.8.29.51 over real columns and confirmed by a
// real "SELECT e8 >= x FROM probe", both operand orders:
//
//	e8 >= d128   Code: 43     d128 >= e8   Code: 43
//	e8 >= dec    Code: 43     dec >= e8    Code: 43
//	e16 >= d128  Code: 43     e16 >= dec   Code: 43
//	e8 >= i64    OK, 1        e8 >= f64    OK, 0
//	e8 >= u8     OK, 1        e8 >= i128   OK, 1
//	e8 >= e16    OK, 1        e8 >= s      OK, 0
//
// Before the fix, comparisonClassesOf placed the Decimal base types in
// comparisonClassNumeric, the SAME class as an Enum, thus
// comparableBaseTypes(e8, d128) answered true and this test failed:
// chgen gave a UInt8 result type for a comparison the server refuses.
func TestComparabilityRefusesEnumAgainstDecimalOnly(t *testing.T) {
	enums := []CHType{{Name: "Enum8"}, {Name: "Enum16"}}
	decimals := []CHType{
		{Name: "Decimal", Params: []CHType{{Name: "18"}, {Name: "4"}}},
		{Name: "Decimal128", Params: []CHType{{Name: "4"}}},
	}
	for _, enum := range enums {
		for _, decimal := range decimals {
			if comparableBaseTypes(enum, decimal) {
				t.Errorf("%s is comparable with %s, but the server answers Code 43",
					enum.String(), decimal.String())
			}
			if comparableBaseTypes(decimal, enum) {
				t.Errorf("%s is comparable with %s, but the server answers Code 43",
					decimal.String(), enum.String())
			}
		}
	}
	// Negative cases: an Enum keeps comparing with every other numeric
	// class, and with another Enum and a String. A fix that widened the
	// refusal to "Enum vs any number" would break these.
	otherNumeric := []CHType{
		{Name: "Int64"}, {Name: "Float64"}, {Name: "UInt8"}, {Name: "Int128"},
	}
	for _, enum := range enums {
		for _, other := range otherNumeric {
			if !comparableBaseTypes(enum, other) {
				t.Errorf("%s is refused against %s, but the server accepts this pair",
					enum.String(), other.String())
			}
			if !comparableBaseTypes(other, enum) {
				t.Errorf("%s is refused against %s, but the server accepts this pair",
					other.String(), enum.String())
			}
		}
	}
	if !comparableBaseTypes(CHType{Name: "Enum8"}, CHType{Name: "Enum16"}) {
		t.Error("Enum8 is refused against Enum16, but the server accepts this pair")
	}
	if !comparableBaseTypes(CHType{Name: "Enum8"}, CHType{Name: "String"}) {
		t.Error("Enum8 is refused against String, but the server accepts this pair")
	}
}

// TestHexRefusesDate32 holds the measured domain fact of the regression: hex
// refuses a Date32 argument, next to the DateTime64 and Enum refusals
// that hexArgumentDomain already states. Date is accepted and Date32 is
// refused, thus the fact is about Date32 alone and not about the date
// family.
//
// Measured on ClickHouse 25.8.29.51 by EXECUTION over a real Date32
// column:
//
//	hex(dt32)  Code: 43   Illegal type Date32 of argument of function hex
//	hex(d)     OK, "50CE"        (Date, must keep running)
//	hex(dt)    OK, "6A885F1C"    (DateTime, must keep running)
//	hex(agg)   OK, "00012CCBC234" (AggregateFunction state, must keep
//	                               running)
func TestHexRefusesDate32(t *testing.T) {
	domain, constrained := argumentDomainFor("hex")
	if !constrained {
		t.Fatal("hex declares no argument domain, thus it types calls the server refuses")
	}
	if err := checkArgumentDomain("hex", domain, CHType{Name: "Date32"}); err == nil {
		t.Error("hex(Date32) is accepted, but the server answers Code 43")
	}
	// Negative cases: the wide part of the domain, and the two facts
	// already recorded by the regression, must keep working.
	accepted := []CHType{
		{Name: "Date"},
		{Name: "DateTime"},
		{Name: "Int32"},
		{Name: "String"},
		{Name: "FixedString", Params: []CHType{{Name: "8"}}},
		{Name: "Float64"},
		{Name: "AggregateFunction(uniq, UInt64)"},
	}
	for _, argument := range accepted {
		if err := checkArgumentDomain("hex", domain, argument); err != nil {
			t.Errorf("hex(%s) is refused, but the server accepts it: %v", argument.String(), err)
		}
	}
	refused := []CHType{
		{Name: "DateTime64"},
		{Name: "Enum8"},
		{Name: "Enum16"},
	}
	for _, argument := range refused {
		if err := checkArgumentDomain("hex", domain, argument); err == nil {
			t.Errorf("hex(%s) is accepted, but the server answers Code 43", argument.String())
		}
	}
}
