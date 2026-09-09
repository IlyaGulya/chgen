package engine

import (
	"fmt"
	"strings"
)

// This file makes wrapper transport a first-class concept.
//
// A ClickHouse type that reaches a type rule can carry three transport
// wrappers around the value that the rule reasons about:
//
//	LowCardinality(X)                 a storage encoding
//	Nullable(X)                       a null flag
//	SimpleAggregateFunction(f, X)     an incremental-merge marker
//
// None of the three changes what the value IS. Each of them changes what
// the SERVER gives back, and each function moves them differently. Before
// this file the three were handled apart: two booleans carried
// LowCardinality and Nullable, and six separate call sites removed
// SimpleAggregateFunction by hand. A rule that saw a wrapper it did not
// expect gave a silently wrong type, which is the worst defect class.
//
// The design here has three parts:
//
//   - wrapperStack holds the FULL stack, thus no caller has to model a
//     wrapper that the split does not return.
//   - a wrapperTransport says, per function and per wrapper, whether the
//     result keeps the wrapper, drops it, or drops it under a measured
//     condition. This replaces the four-value class enum, which was too
//     coarse to express the measured grid.
//   - applyWrapperTransport is the ONE applier. A rule never sees a
//     wrapped type and never returns one; wrapperTransportGuardTest pins
//     that.
//
// Every rule in this package therefore reasons about BARE base types
// only, with two CLOSED and measured exemptions: a wrapperOpaque rule
// and a value-preserving rule own their wrappers. The guard test
// TestFunctionTypeRulesNeverSeeWrappedTypes enforces the property and
// keeps both exemption lists closed and tight.
//
// The design was proved on real work. The two defects that this file
// first recorded as unfixed are now both corrected, and each correction
// went into the transport table with no name check in the applier:
//
//   - the regression gave wrapperAggregate a conditional simpleAggregate
//     disposition, because a value-preserving aggregate over
//     SimpleAggregateFunction(f, Nullable(T)) must answer the bare
//     Nullable inner type. The regression then widened that condition: the
//     marker survives only over a BARE SCALAR inner type, and a
//     nullability test kept it over a COMPOSITE inner type. See
//     aggregateKeepsSimpleAggregateCondition.
//   - the regression gave lower and upper their own transport, because they
//     KEEP the marker although they compute a new String. The first
//     guess, that the split is value-preserving against value-computing,
//     was WRONG: lower and trim both compute a new String and only lower
//     keeps the wrapper. See caseFoldingTransport.
//
// Both measurements are on ClickHouse 25.8.29.51 with real columns in a
// real table, never over literals, because the server folds constants.

// wrapperStack is the transport wrappers that sit around a base type,
// in the ClickHouse nesting order
//
//	LowCardinality(Nullable(SimpleAggregateFunction(f, base)))
//
// Not every nesting is legal on the server; the stack simply records
// which wrappers were present so the applier can put back the ones the
// function keeps.
type wrapperStack struct {
	// lowCardinality is true when a LowCardinality wrapper was present
	// at the top level.
	lowCardinality bool
	// nullable is true when a Nullable wrapper was present, either
	// outside the SimpleAggregateFunction or as its inner type. Both
	// spellings mean the same thing to the caller: the value can be
	// null. Measured on ClickHouse 25.8.29.51 with real columns:
	// sum(saggn) is Nullable(Int64), exactly as sum(ni64) is, where
	// saggn is SimpleAggregateFunction(sum, Nullable(Int64)).
	nullable bool
	// outerNullable is true when a Nullable is required OUTSIDE any
	// SimpleAggregateFunction wrapper. It is set when the Nullable came
	// from a source other than the inner type of that wrapper: a
	// Nullable at the top level, or a later data argument such as the
	// ordering argument of argMax.
	//
	// The two flags are independent. A type can need a Nullable inside
	// the wrapper AND one outside it, thus one boolean cannot carry
	// both.
	outerNullable bool
	// simpleAggregate holds the SimpleAggregateFunction wrapper when
	// one was present, so the applier can put back the SAME wrapper
	// with the SAME aggregate-function parameter. It is nil when there
	// was none.
	simpleAggregate *simpleAggregateWrapper
}

// simpleAggregateWrapper remembers a SimpleAggregateFunction wrapper so
// it can be rebuilt exactly. The function parameter (the "sum" of
// SimpleAggregateFunction(sum, Int64)) is part of the type, thus it must
// survive the round trip.
type simpleAggregateWrapper struct {
	// wrapper is the original SimpleAggregateFunction type. The applier
	// rebuilds from it and replaces only the inner type, which keeps
	// the aggregate-function parameter and any literal parameters.
	wrapper CHType
	// innerNullable is true when the inner type of the wrapper was
	// itself Nullable. The applier needs this to rebuild
	// SimpleAggregateFunction(f, Nullable(T)) rather than
	// SimpleAggregateFunction(f, T).
	innerNullable bool
	// innerLowCardinality is true when the inner type of the wrapper
	// was itself LowCardinality. The split lifts that wrapper into the
	// stack, so that a rule never sees it on its base; the applier
	// needs this flag to rebuild
	// SimpleAggregateFunction(f, LowCardinality(T)) when the marker
	// survives.
	innerLowCardinality bool
}

// splitWrapperStack removes every transport wrapper from a type and
// reports both the bare base type and the stack that was removed.
//
// It understands the nested forms that the server produces:
//
//	LowCardinality(Nullable(X))
//	SimpleAggregateFunction(f, Nullable(X))
//
// A Nullable INSIDE a SimpleAggregateFunction sets the same nullable
// flag as one outside it, because the value can be null either way. The
// applier puts the Nullable back in whichever position the transport
// asks for.
func splitWrapperStack(value CHType) (base CHType, stack wrapperStack) {
	base = value
	if strings.EqualFold(base.Name, "LowCardinality") && len(base.Params) == 1 {
		stack.lowCardinality = true
		base = base.Params[0]
	}
	if strings.EqualFold(base.Name, "Nullable") && len(base.Params) == 1 {
		stack.nullable = true
		// This Nullable sits OUTSIDE any SimpleAggregateFunction, thus
		// it must go back outside one.
		stack.outerNullable = true
		base = base.Params[0]
	}
	if strings.EqualFold(base.Name, "SimpleAggregateFunction") && len(base.Params) == 2 {
		wrapper := simpleAggregateWrapper{wrapper: base}
		inner := base.Params[1]
		// A LowCardinality INSIDE the marker is a transport wrapper
		// exactly as one outside it, thus it belongs in the stack and
		// not in the base. The regression added this step.
		//
		// Before it, a rule saw LowCardinality(FixedString(8)) as its
		// "bare" base and the file's own contract, that a rule reasons
		// about bare base types only, was broken for this one shape.
		// The damage was measurable: caseFoldingFunctionType tests the
		// base for the name FixedString, the wrapped name did not
		// match, and lower over
		// SimpleAggregateFunction(anyLast, LowCardinality(FixedString(8)))
		// answered String where the server answers
		// LowCardinality(FixedString(8)). Measured on ClickHouse
		// 25.8.29.51 with a real column of a real AggregatingMergeTree
		// table:
		//
		//	lower(saflcfs)  LowCardinality(FixedString(8))
		//	upper(saflcfs)  LowCardinality(FixedString(8))
		//
		// The flag is remembered on the wrapper as well, so that a
		// marker which SURVIVES can be rebuilt with its inner
		// LowCardinality in place. The measured marker rule drops the
		// marker over every LowCardinality inner, thus that path is not
		// reachable today; it is kept correct so that a later rule
		// change cannot turn this into a silently wrong type.
		if strings.EqualFold(inner.Name, "LowCardinality") && len(inner.Params) == 1 {
			wrapper.innerLowCardinality = true
			stack.lowCardinality = true
			inner = inner.Params[0]
		}
		if strings.EqualFold(inner.Name, "Nullable") && len(inner.Params) == 1 {
			wrapper.innerNullable = true
			stack.nullable = true
			inner = inner.Params[0]
		}
		stack.simpleAggregate = &wrapper
		base = inner
	}
	return base, stack
}

// applyWrapperStack puts a whole stack back around a base type. It is the
// inverse of splitWrapperStack for the wrappers the caller kept.
//
// The SimpleAggregateFunction wrapper goes on first, then Nullable, then
// LowCardinality, which is the ClickHouse nesting order. When the stack
// keeps both a SimpleAggregateFunction and a Nullable, the Nullable goes
// back where it came from: inside the wrapper when it was inside, and
// outside it otherwise.
// A Nullable that came from INSIDE the wrapper goes back inside it. An
// outer Nullable is applied on top as well when the stack still carries
// one, because a later data argument can contribute a Nullable of its
// own (for example the ordering argument of argMax). The two are
// independent, thus rebuilding the inner one must not consume the outer
// one.
func applyWrapperStack(base CHType, stack wrapperStack) CHType {
	result := base
	nullable := stack.nullable
	lowCardinality := stack.lowCardinality
	if stack.simpleAggregate != nil {
		inner := result
		if nullable && stack.simpleAggregate.innerNullable {
			inner = wrapNullable(inner)
			// The Nullable is accounted for INSIDE the wrapper. An
			// outer one is added below only when a source other than
			// this wrapper contributed it.
			nullable = stack.outerNullable
		}
		if lowCardinality && stack.simpleAggregate.innerLowCardinality {
			// The LowCardinality came from INSIDE the marker, thus it
			// goes back inside it and must not also be applied on top.
			// The split lifted it into the stack only so that the rule
			// would see a bare base type.
			inner = wrapLowCardinality(inner)
			lowCardinality = false
		}
		rebuilt := stack.simpleAggregate.wrapper
		params := make([]CHType, len(rebuilt.Params))
		copy(params, rebuilt.Params)
		params[1] = inner
		rebuilt.Params = params
		result = rebuilt
	}
	if nullable {
		result = wrapNullable(result)
	}
	if lowCardinality {
		// wrapLowCardinality holds the measured rule that some result
		// families cannot carry the wrapper at all. The test is on the
		// FACTS of the result type, never on the function name, thus
		// the applier stays free of a name check.
		result = wrapLowCardinality(result)
	}
	return result
}

// ---------------------------------------------------------------------
// The wrapper constructors.
//
// wrapNullable and wrapLowCardinality are the ONLY two functions in the
// package that build a transport wrapper node from a bare type. They
// live together here because they hold the same obligation: a wrapper
// that the server refuses must not go on the type, because a silently
// wrong type is worse than no wrapper at all. Each of them carries a
// measured rejection rule (canBeInsideNullable and
// resultRejectsLowCardinality), and a caller that built the node by hand
// would skip that rule.
//
// TestOnlyWrapperTransportBuildsWrapperTypes reads the package with
// go/parser and fails when any other file builds a LowCardinality, a
// Nullable or a SimpleAggregateFunction CHType node outside its
// allowlist. That guard is what makes "the applier did not see it"
// impossible rather than only unlikely: the regression was exactly a
// wrapLowCardinality call site that never asked
// resultRejectsLowCardinality.
// ---------------------------------------------------------------------

// wrapNullable puts a Nullable wrapper on a type, unless the type is
// already Nullable or cannot go inside Nullable. A base that cannot go
// inside Nullable keeps no wrapper, because the server gives back the
// bare type. See canBeInsideNullable for the measurements.
func wrapNullable(value CHType) CHType {
	if strings.EqualFold(value.Name, "Nullable") || !canBeInsideNullable(value) {
		return value
	}
	return CHType{Name: "Nullable", Params: []CHType{value}}
}

// lowCardinalityRejectedResultNames holds the result type families that
// cannot go inside a LowCardinality wrapper. The list is measured, not
// copied from the server message.
//
// The server sentence "DataTypeLowCardinality is supported only for
// numbers, strings, Date or DateTime" is not correct: UUID, IPv4, IPv6
// and Bool also go inside LowCardinality. Thus the honest rule is the
// opposite list, that is the families that give Code: 43.
//
// Measured with CREATE TABLE t (c LowCardinality(T)) at
// allow_suspicious_low_cardinality_types=1, then confirmed against the
// expression results over a LowCardinality(String) column:
//
//	Accepted, thus the wrapper stays: every Int and UInt width,
//	Float32, Float64, BFloat16, String, FixedString, Date, Date32,
//	DateTime, UUID, IPv4, IPv6, Bool, and Nullable of each.
//	  toDate(lc)         -> LowCardinality(Date)
//	  toInt128OrZero(lc) -> LowCardinality(Int128)
//	  toUUIDOrNull(lc)   -> LowCardinality(Nullable(UUID))
//
//	Refused with Code: 43, thus the wrapper drops: Decimal of every
//	width, DateTime64 of every precision, Enum8, Enum16, and the
//	containers Array, Tuple and Map.
//	  toDecimal64(lc, 2)        -> Decimal(18, 2)
//	  toDateTime64OrZero(lc, 1) -> DateTime64(1)
//
// The setting state is deliberate. With the setting off, a numeric or a
// Date column gives Code: 455, which is a policy guard against a
// wasteful column, not a statement that the type cannot exist. Code 455
// does not stop an EXPRESSION from having that type, and chgen infers
// expression types. Only Code 43 is a type refusal, thus only the
// Code 43 families belong in this list.
var lowCardinalityRejectedResultNames = map[string]struct{}{
	"decimal":    {},
	"datetime64": {},
	"enum":       {},
	"enum8":      {},
	"enum16":     {},
	"array":      {},
	"tuple":      {},
	"map":        {},
}

// resultRejectsLowCardinality reports whether a result type cannot carry
// a LowCardinality wrapper. The test is on the result type FAMILY, and
// it looks through a Nullable wrapper, because the server decides on the
// inner type: LowCardinality(Nullable(Date)) exists, while
// LowCardinality(Nullable(DateTime64(3))) is Code: 43.
//
// See lowCardinalityRejectedResultNames for the measured table.
func resultRejectsLowCardinality(base CHType) bool {
	value := base
	if strings.EqualFold(value.Name, "Nullable") && len(value.Params) == 1 {
		value = value.Params[0]
	}
	_, rejected := lowCardinalityRejectedResultNames[strings.ToLower(value.Name)]
	return rejected
}

// wrapLowCardinality puts a LowCardinality wrapper on a type, unless the
// type already has one or cannot go inside one. It is the counterpart of
// wrapNullable, and it holds the same obligation: a wrapper that the
// server refuses must not go on the type, because a silently wrong type
// is worse than no wrapper at all.
//
// Every path that gives a result a LowCardinality wrapper goes through
// here, thus the measured rule applies once for all of them and no
// caller has to name a function. Measured on ClickHouse 25.8.29.51 over
// real LowCardinality columns:
//
//	toDateTime(lcdt)      LowCardinality(DateTime)   the family is kept
//	toDate(lcdt)          LowCardinality(Date)       the family is kept
//	toDateTime64(lcdt, 3) DateTime64(3)              the wrapper drops
//	toDateTime64(lcd, 3)  DateTime64(3)              the wrapper drops
//	toDateTime64(lcnd, 3) Nullable(DateTime64(3))    the wrapper drops
//
// See resultRejectsLowCardinality for the measured family table.
func wrapLowCardinality(base CHType) CHType {
	if strings.EqualFold(base.Name, "LowCardinality") || resultRejectsLowCardinality(base) {
		return base
	}
	return CHType{Name: "LowCardinality", Params: []CHType{base}}
}

// wrapperDisposition says what a function does with ONE transport
// wrapper of its arguments.
type wrapperDisposition int

const (
	// wrapperDrop: the result never carries this wrapper. An aggregate
	// drops LowCardinality this way (measured: max(lc) is String).
	wrapperDrop wrapperDisposition = iota
	// wrapperKeep: the result carries this wrapper whenever an
	// argument had it.
	wrapperKeep
	// wrapperConditional: the result carries this wrapper only when the
	// transport's condition says so. The condition is a measured
	// predicate over the call, not a guess. See wrapperCondition.
	wrapperConditional
	// wrapperAdd: the result ALWAYS carries this wrapper, whether or
	// not an argument had it. This is how a function that CREATES a
	// wrapper states the fact.
	//
	// nullIf is the one such function today: it makes the value NULL
	// when its two arguments are equal, thus the result is nullable
	// even when both arguments are not (measured on ClickHouse
	// 25.8.29.51 with real columns: nullIf(i32, i32) is
	// Nullable(Int32)).
	//
	// The disposition exists so that the rule does not have to add the
	// wrapper by hand. A rule that adds a wrapper itself puts it in the
	// wrong PLACE for a SimpleAggregateFunction argument, because the
	// applier can only rebuild the marker AROUND a finished result. See
	// nullIfFunctionResult.
	wrapperAdd
)

// wrapperCondition decides a wrapperConditional disposition from the
// facts of one call: the bare base type of the first argument, the
// stacks of every argument, and the argument count.
//
// A condition is measured behaviour, thus it takes the call facts and
// NOT the function name. A rule that needs the name is a rule that was
// not understood; put the measurement in the condition instead.
type wrapperCondition func(call wrapperCall) bool

// wrapperCall carries the facts a condition may use. It deliberately
// does not carry the function name: a condition must describe measured
// behaviour, not a name-based special case.
type wrapperCall struct {
	// base is the bare base type of the first argument, with every
	// transport wrapper already removed.
	base CHType
	// stacks holds the wrapper stack of each argument, in order.
	stacks []wrapperStack
	// argCount is the number of arguments in the call.
	argCount int
}

// wrapperTransport is the per-function transport spec: one disposition
// for each of the three transport wrappers, plus the conditions that a
// wrapperConditional disposition needs.
//
// This replaces the functionWrapperClass enum at the decision point. The
// enum stays as the SOURCE of a transport (see transportForClass),
// because most functions need nothing more than their class. A function
// whose measured behaviour does not fit its class carries its own
// transport in the registry instead of a special case in the applier.
type wrapperTransport struct {
	// lowCardinality says what happens to a LowCardinality wrapper.
	lowCardinality wrapperDisposition
	// lowCardinalityWhen decides the wrapperConditional case for
	// LowCardinality. It must be set when the disposition is
	// wrapperConditional.
	lowCardinalityWhen wrapperCondition
	// nullable says what happens to a Nullable wrapper.
	nullable wrapperDisposition
	// nullableWhen decides the wrapperConditional case for Nullable.
	nullableWhen wrapperCondition
	// simpleAggregate says what happens to a SimpleAggregateFunction
	// wrapper.
	simpleAggregate wrapperDisposition
	// simpleAggregateWhen decides the wrapperConditional case for
	// SimpleAggregateFunction.
	simpleAggregateWhen wrapperCondition
	// stripNested is true when the result additionally loses every
	// LowCardinality wrapper INSIDE a container member, not only the
	// one at the top level. Every true aggregate does this (measured:
	// argMin(arr, ni32) is Array(String) where arr is
	// Array(LowCardinality(String))). The dispositions carry only the
	// top level, thus this needs its own field.
	stripNested bool
}

// keeps reports the disposition of one wrapper as a plain boolean, given
// the facts of the call.
func (transport wrapperTransport) keeps(disposition wrapperDisposition, when wrapperCondition, call wrapperCall) bool {
	switch disposition {
	case wrapperKeep, wrapperAdd:
		return true
	case wrapperConditional:
		return when != nil && when(call)
	default:
		return false
	}
}

// resultStack computes the wrapper stack of the RESULT from the stacks of
// the arguments and the transport of the function.
//
// The argument stacks are combined first (any argument that had a
// wrapper contributes it), then the transport decides which of the
// combined wrappers survive into the result.
func (transport wrapperTransport) resultStack(call wrapperCall) wrapperStack {
	var combined wrapperStack
	for _, stack := range call.stacks {
		combined.lowCardinality = combined.lowCardinality || stack.lowCardinality
		combined.nullable = combined.nullable || stack.nullable
		combined.outerNullable = combined.outerNullable || stack.outerNullable
		if combined.simpleAggregate == nil && stack.simpleAggregate != nil {
			combined.simpleAggregate = stack.simpleAggregate
		}
	}
	keepsMarker := combined.simpleAggregate != nil &&
		transport.keeps(transport.simpleAggregate, transport.simpleAggregateWhen, call)

	// A LowCardinality that came from INSIDE the marker is already in
	// combined.lowCardinality, because splitWrapperStack lifts it into
	// the stack. It is therefore decided by the ordinary LowCardinality
	// disposition of the transport, with no special case here.
	//
	// That is what gives the two transports their opposite and measured
	// answers, with no name check anywhere. Measured on ClickHouse
	// 25.8.29.51 with real columns of a real AggregatingMergeTree table,
	// where saflc is
	// SimpleAggregateFunction(anyLast, LowCardinality(String)):
	//
	//	max(saflc)    String                  the aggregate drops the LC
	//	lower(saflc)  LowCardinality(String)  case folding keeps the LC
	//
	// The aggregate transport drops LowCardinality and the case-folding
	// transport keeps it, thus each family gets its own answer from the
	// disposition it already declared.
	var result wrapperStack
	if combined.lowCardinality && transport.keeps(transport.lowCardinality, transport.lowCardinalityWhen, call) {
		result.lowCardinality = true
	}
	// A wrapperAdd disposition does not need an argument to have
	// contributed the wrapper: the function CREATES it. Every other
	// disposition still needs a source argument.
	if (combined.nullable || transport.nullable == wrapperAdd) &&
		transport.keeps(transport.nullable, transport.nullableWhen, call) {
		result.nullable = true
		// The added Nullable goes OUTSIDE a SimpleAggregateFunction
		// marker, because the marker holds the value and the null flag
		// sits on top of it. An INNER Nullable that the argument
		// already had keeps its own place: applyWrapperStack rebuilds
		// it inside the marker from the innerNullable flag, and it
		// consumes the outer request only when the marker really
		// carried it.
		//
		// Measured on ClickHouse 25.8.29.51 with real columns:
		//
		//	nullIf(saf, saf)    Nullable(SimpleAggregateFunction(anyLast, Int32))
		//	nullIf(safn, safn)  SimpleAggregateFunction(anyLast, Nullable(Int32))
		result.outerNullable = combined.outerNullable ||
			(transport.nullable == wrapperAdd && !markerCarriesInnerNullable(combined))
	}
	if keepsMarker {
		result.simpleAggregate = combined.simpleAggregate
	}
	return result
}

// markerCarriesInnerNullable reports whether the combined stack holds a
// SimpleAggregateFunction marker whose inner type was itself Nullable.
//
// Such a marker already satisfies a request for a Nullable result: the
// server gives the marker back UNCHANGED and adds no second Nullable
// around it. The applier must therefore NOT also ask for an outer one.
func markerCarriesInnerNullable(stack wrapperStack) bool {
	return stack.simpleAggregate != nil && stack.simpleAggregate.innerNullable
}

// transportForClass gives the transport that a wrapper class implies.
// Most functions need nothing more than this: the class already says
// what the measurements found.
//
//   - wrapperOpaque never reaches the applier, because such a function's
//     own rule already handles the wrappers. It is listed for
//     completeness, and it drops everything.
//   - wrapperTransparent keeps Nullable when any argument is Nullable,
//     and keeps LowCardinality only under the measured condition that
//     exactly one argument is LowCardinality and each other argument is
//     a constant literal. That condition needs the argument
//     EXPRESSIONS, thus the caller supplies it; see
//     transparentLowCardinalityCondition.
//   - wrapperAggregate drops LowCardinality at every depth and keeps
//     Nullable.
//
// The SimpleAggregateFunction disposition is wrapperKeep for every class
// here, because the wrapper stays unless a function is measured to drop
// it. The value-computing aggregates (sum, avg, quantile, groupBit*)
// drop it, and they are separated from the value-preserving ones (max,
// min, any, argMax) by the presence of a measured argument domain, not
// by the class. See transportForFunction.
func transportForClass(class functionWrapperClass) wrapperTransport {
	switch class {
	case wrapperClassUnset:
		// wrapperClassUnset means either an unknown function name or a
		// registry spec that forgot to set class. Both are build-time
		// defects: TestEverySpecDeclaresAClass fails the build on the
		// second one, and no live code path calls transportForClass
		// with the first one, because functionClassFor is read only
		// after a successful rule lookup. Reaching this case at
		// runtime is a caller bug, not a case to answer quietly, so
		// panic instead of returning a transport that would silently
		// read like wrapperOpaque.
		panic(fmt.Sprintf("transportForClass called with wrapperClassUnset (class %d); this is a caller bug, not a legal class", class))
	case wrapperOpaque:
		// wrapperOpaque never reaches this function in practice,
		// because applyFunctionWrappers and its callers short-circuit
		// on class == wrapperOpaque before asking for a transport. The
		// case is explicit so that the switch stays exhaustive over
		// functionWrapperClass; its transport, if ever read, drops
		// every wrapper.
		return wrapperTransport{
			lowCardinality:  wrapperDrop,
			nullable:        wrapperDrop,
			simpleAggregate: wrapperDrop,
		}
	case wrapperTransparent:
		return wrapperTransport{
			lowCardinality: wrapperConditional,
			nullable:       wrapperKeep,
			// A transparent function that COMPUTES a new value drops
			// the SimpleAggregateFunction marker (measured on
			// ClickHouse 25.8.29.51 with real columns:
			// toString(sagg) is String, trim(saggs) is String,
			// concat(saggs, s) is String, length(saggs) is UInt64).
			//
			// This drop is the DEFAULT of the class, and it is not the
			// answer for every transparent function. lower and upper
			// keep the marker, thus they carry their own transport in
			// wrapperTransportOverrides. That is a per-function
			// property of the server and not a rule that the class can
			// hold: lower and trim both compute a new String, and only
			// lower keeps the wrapper. Measure a name before you add
			// it here. See caseFoldingTransport for the family table.
			simpleAggregate: wrapperDrop,
		}
	case wrapperAggregate:
		return wrapperTransport{
			lowCardinality: wrapperDrop,
			nullable:       wrapperKeep,
			// A value-preserving aggregate keeps the
			// SimpleAggregateFunction marker only when the inner type
			// of the wrapper is a BARE SCALAR. For every other inner
			// type, Nullable and composite alike, the server gives the
			// inner type back and the marker is gone. See
			// aggregateKeepsSimpleAggregateCondition for the measured
			// grid.
			simpleAggregate:     wrapperConditional,
			simpleAggregateWhen: aggregateKeepsSimpleAggregateCondition,
			stripNested:         true,
		}
	default:
		// A functionWrapperClass value with no case above. The only
		// way to reach this today is to add a new member to the enum
		// in resolver.go without adding a case here;
		// TestTransportForClassCoversEveryMember (see
		// wrappergrid_class_zero_value_test.go) fails the build the
		// moment that happens, so this branch should never execute in
		// a build that passes tests. Panic rather than guess a
		// transport for a class nobody has measured.
		panic(fmt.Sprintf("transportForClass has no case for class %d; add one and measure the wrapper behaviour before landing it", class))
	}
}

// transportForFunction gives the transport of one function. It is the
// single place that maps a name to its transport.
//
// Almost every function uses the transport that its class implies. Only
// a function whose MEASURED behaviour differs from its class carries its
// own entry here, and each such entry states the measurement.
//
// This is the replacement for the name-based special case that used to
// live inside the applier. The applier now asks for a transport and
// applies it; it knows no names.
func transportForFunction(name string, class functionWrapperClass) wrapperTransport {
	if transport, special := wrapperTransportOverrides[name]; special {
		return transport
	}
	return transportForClass(class)
}

// wrapperTransportOverrides holds the functions whose measured transport
// does not follow their class.
var wrapperTransportOverrides = map[string]wrapperTransport{
	// Millisecond conversion keeps Nullable but decodes LowCardinality,
	// even with a constant timezone (measured on 25.8.29.51 real columns).
	"fromunixtimestamp64milli": {
		lowCardinality: wrapperDrop, nullable: wrapperKeep, simpleAggregate: wrapperDrop,
	},
	"greatest":                  greatestLeastTransport,
	"least":                     greatestLeastTransport,
	"and":                       logicOperatorTransport,
	"or":                        logicOperatorTransport,
	"xor":                       logicOperatorTransport,
	"lower":                     caseFoldingTransport,
	"upper":                     caseFoldingTransport,
	"laginframe":                windowValuePreservingTransport,
	"leadinframe":               windowValuePreservingTransport,
	"lag":                       windowValuePreservingTransport,
	"lead":                      windowValuePreservingTransport,
	"nth_value":                 windowValuePreservingTransport,
	"first_value_respect_nulls": windowValuePreservingTransport,
	"firstvaluerespectnulls":    windowValuePreservingTransport,
	"last_value_respect_nulls":  windowValuePreservingTransport,
	"lastvaluerespectnulls":     windowValuePreservingTransport,
	"nullif":                    nullIfTransport,
	// round, roundBankers, floor, ceil and trunc/truncate (the regression)
	// keep the SimpleAggregateFunction marker over a bare scalar inner,
	// exactly the shape caseFoldingTransport already states for lower
	// and upper: measured on ClickHouse 25.8.29.51 over a real
	// AggregatingMergeTree table, round(sagg, 1) is
	// SimpleAggregateFunction(sum, Int64), unchanged, while
	// round(saggn, 1) over SimpleAggregateFunction(sum, Nullable(Int64))
	// is a bare Nullable(Int64), no marker (the composite inner drops
	// it). This family is scalar, like lower and upper, and not a true
	// aggregate: it is registered with class wrapperTransparent, so its
	// class transport alone would drop the marker unconditionally, the
	// same gap nullIf had. Reusing caseFoldingTransport rather than a
	// new value keeps the ONE measured condition in one place.
	"round":        caseFoldingTransport,
	"roundbankers": caseFoldingTransport,
	"floor":        caseFoldingTransport,
	"ceil":         caseFoldingTransport,
	"trunc":        caseFoldingTransport,
	"truncate":     caseFoldingTransport,
}

// nullIfTransport is the transport of nullIf.
//
// nullIf(a, b) is "a = b ? NULL : a". It gives its FIRST argument back
// rather than computing a new value, thus it keeps the
// SimpleAggregateFunction marker. Its class is wrapperTransparent, whose
// class transport DROPS the marker, so nullIf needs its own entry.
//
// The marker survives only when the inner type is NOT LowCardinality.
// Over a LowCardinality inner type the server drops the marker and
// answers about the value alone. Measured on ClickHouse 25.8.29.51 with
// real columns of a real AggregatingMergeTree table with one row, never
// over literals, because the server folds constants. The grid was taken
// over TWO type families, so that the rule is not read off one column
// (saf/safs are markers over a bare scalar, safn is a marker over a
// Nullable inner type, saflc/saflcs are markers over a LowCardinality
// inner type):
//
//	nullIf(saf, saf)        Nullable(SimpleAggregateFunction(anyLast, Int32))
//	nullIf(safs, safs)      Nullable(SimpleAggregateFunction(anyLast, String))
//	nullIf(safn, safn)      SimpleAggregateFunction(anyLast, Nullable(Int32))
//	nullIf(saflc, saflc)    Nullable(Int32)
//	nullIf(saflcs, saflcs)  Nullable(String)
//
// The Nullable row is the interesting one. nullIf must make the value
// nullable, and when the inner type is ALREADY Nullable the server
// satisfies that by giving the marker back UNCHANGED. It does not add a
// second Nullable around it. This needs no special case here: the split
// lifts the inner Nullable into the stack and remembers where it came
// from, and applyWrapperStack puts it back INSIDE the marker. The
// reading was verified at EXECUTION and not only by analysis, because
// toTypeName is blind to an execution-time failure; on the fixture row
// nullIf(safn, safn) really answers NULL with isNull = 1.
//
// The marker rule does not depend on the second argument (measured:
// nullIf(saf, 1), nullIf(saf, i32) and nullIf(saf, saf) all keep the
// marker; nullIf(saflc, 1) and nullIf(saflc, saf) both drop it).
//
// LowCardinality follows the ordinary transparent rule, which keeps the
// wrapper only when every other argument is a constant. That rule is
// resolved by the caller, exactly as it is for every other transparent
// name, thus the disposition here is conditional:
//
//	nullIf(saflc, 1)      LowCardinality(Nullable(Int32))
//	nullIf(lci32, 1)      LowCardinality(Nullable(Int32))
//	nullIf(saflc, saflc)  Nullable(Int32)
//	nullIf(lci32, lci32)  Nullable(Int32)
//
// A composite inner type never reaches this transport: the server
// refuses the call, because a Nullable cannot hold an Array or a Map
// (measured: nullIf(safarr, safarr) is Code: 43, "Nested type
// Array(String) cannot be inside Nullable type").
var nullIfTransport = wrapperTransport{
	// The disposition is conditional, and the CALLER resolves it with
	// withResolvedLowCardinality before the applier runs, exactly as it
	// does for the wrapperTransparent class transport. The condition
	// field therefore stays nil: the rule needs the argument
	// EXPRESSIONS, which the wrapperCall facts deliberately do not
	// carry.
	lowCardinality: wrapperConditional,
	// nullIf CREATES the Nullable, thus the disposition adds it rather
	// than keeping one that an argument had (measured: nullIf(i32, i32)
	// is Nullable(Int32), although neither argument is nullable).
	nullable:        wrapperAdd,
	simpleAggregate: wrapperConditional,
	// nullIf gives its argument back, thus it belongs to the
	// value-preserving family and shares that family's ONE marker rule.
	// It is not an aggregate: an aggregate READS the value and drops the
	// marker over a Nullable inner, while nullIf keeps it there.
	// Measured on ClickHouse 25.8.29.51:
	//
	//	max(safn)          Nullable(Int32)                                    the marker goes
	//	nullIf(safn, safn) SimpleAggregateFunction(anyLast, Nullable(Int32))   the marker stays
	//	nullIf(saflc, saflc) Nullable(Int32)                                   the marker goes
	//
	// The family rule also drops the marker over an Array, a Map and a
	// Tuple inner type. nullIf cannot disagree there, because a Nullable
	// cannot hold those types and the server refuses the call with Code
	// 43 before a type comes back. Thus the shared rule and the measured
	// nullIf cells agree in every reachable cell.
	simpleAggregateWhen: callKeepsValuePreservingScalarMarker,
}

// logicOperatorTransport is the transport of the logic operators AND, OR
// and XOR.
//
// The key of each entry is the FUNCTION spelling of the operator,
// because the operator path looks the transport up by that name. chgen
// has no type rule for the names and, or and xor today, thus a call of
// the function form still refuses; TestLogicOperatorFunctionFormStillRefuses
// pins that. The entries hold the measurement for BOTH spellings, so
// that a rule added later for the names cannot take a second, unmeasured
// answer.
//
// These three REMOVE a LowCardinality wrapper, and they remove it
// always: at every arity, and whether one operand or every operand
// carries the wrapper. The predicate operators around them KEEP the
// wrapper under the usual "one LowCardinality operand and every other
// operand constant" rule, thus the logic operators need their own
// transport.
//
// Measured on ClickHouse 25.8.29.51 with real table columns, never over
// literals, because the server folds constants. The column lc is
// LowCardinality(String) and lc2 is a second LowCardinality(String).
// The empty-of-concat form is the way to get a LowCardinality(UInt8)
// operand, because the server refuses such a COLUMN as a suspicious
// type:
//
//	empty(concat('', lc))                              LowCardinality(UInt8)
//	empty(concat('', lc)) AND empty('abc')             UInt8
//	empty('abc') AND empty(concat('', lc))             UInt8
//	empty(concat('', lc)) AND empty(concat('', lc2))   UInt8
//	empty(concat('', lc)) OR empty('abc')              UInt8
//	empty('abc') OR empty(concat('', lc))              UInt8
//	empty(concat('', lc)) OR empty(concat('', lc2))    UInt8
//	xor(empty(concat('', lc)), empty('abc'))           UInt8
//	xor(empty(concat('', lc)), empty(concat('', lc2))) UInt8
//	and(empty(concat('', lc)), empty(concat('', lc2)), empty('abc')) UInt8
//	or(empty(concat('', lc)), empty(concat('', lc2)), empty('abc'))  UInt8
//
// NOT is NOT in this list. It KEEPS the wrapper, thus it stays with the
// usual rule. Measured on the same server:
//
//	NOT empty(concat('', lc))       LowCardinality(UInt8)
//	NOT NOT empty(concat('', lc))   LowCardinality(UInt8)
//
// The comparison and text predicates are the contrast. Each of them
// keeps the wrapper where the logic operators remove it (measured on
// the same server):
//
//	empty(concat('', lc)) =  empty('abc')  LowCardinality(UInt8)
//	empty(concat('', lc)) != empty('abc')  LowCardinality(UInt8)
//	empty(concat('', lc)) IN (0, 1)        LowCardinality(UInt8)
//	lc LIKE '%a%'                          LowCardinality(UInt8)
//	lc REGEXP 'a'                          LowCardinality(UInt8)
//
// Nullable is a separate axis and it SURVIVES the logic operators, thus
// the removal is of LowCardinality alone. Measured with lcn, a
// LowCardinality(Nullable(String)) column:
//
//	empty(concat('', lcn))                             LowCardinality(Nullable(UInt8))
//	empty(concat('', lcn)) AND empty(concat('', lcn2)) Nullable(UInt8)
//	empty(concat('', lcn)) OR empty('x')               Nullable(UInt8)
//
// The types above are from toTypeName, which is analysis. The same
// expressions also execute and give values, thus the answer is not an
// analysis-only artifact.
var logicOperatorTransport = wrapperTransport{
	lowCardinality: wrapperDrop,
	nullable:       wrapperKeep,
	// The logic operators read each operand as a condition and compute
	// a new value, thus they keep no SimpleAggregateFunction marker,
	// exactly as a value-computing transparent function does.
	simpleAggregate: wrapperDrop,
	// These are scalar operators, thus they must not remove a
	// LowCardinality wrapper inside a container member.
	stripNested: false,
}

// caseFoldingTransport is the transport of lower and upper.
//
// A wrapperTransparent function normally DROPS the
// SimpleAggregateFunction marker, because it computes a new value. lower
// and upper compute a new value too, and the server KEEPS the marker for
// them. The split is therefore NOT value-preserving against
// value-computing: it is a property of the server implementation of each
// function, thus each name must be measured on its own.
//
// Measured on ClickHouse 25.8.29.51 with real columns in a real table,
// over the whole String family. The wrapper SURVIVES for
//
//	lower, upper, reverse, lowerUTF8, upperUTF8, reverseUTF8,
//	toValidUTF8, normalizeUTF8NFC
//
// and it is DROPPED for
//
//	trim, trimLeft, trimRight, toString, hex, base64Encode,
//	substring, concat, replaceAll, extract, leftPad, length,
//	empty, notEmpty, position, splitByChar, cityHash64
//
// Both groups hold unary String-to-String functions, thus neither the
// arity nor the result type gives the answer. Of the surviving group
// only lower and upper are in the registry. reverse and the UTF8
// spellings are absent, thus they need no entry until they are added.
//
// The wrapper is kept with its EXACT aggregate-function parameter and
// its exact inner type (measured: lower over
// SimpleAggregateFunction(max, String) gives
// SimpleAggregateFunction(max, String), and over
// SimpleAggregateFunction(min, FixedString(8)) gives
// SimpleAggregateFunction(min, FixedString(8))). applyWrapperStack
// rebuilds from the original wrapper, thus this is automatic.
//
// The keep is CONDITIONAL on the inner type: the marker survives only
// over a BARE SCALAR inner type. When the inner type is Nullable the
// server DROPS the wrapper (measured: lower(saggn) is Nullable(String),
// where saggn is SimpleAggregateFunction(min, Nullable(String))). Note
// that SimpleAggregateFunction ACCEPTS a Nullable inner type, unlike
// AggregateFunction.
//
// the regression widened the condition to the measured scalar rule. A
// LowCardinality inner type also drops the marker, and the
// LowCardinality that the marker hid then becomes a LowCardinality of
// the result, which this transport KEEPS (measured: lower(saflc) is
// LowCardinality(String), where saflc is
// SimpleAggregateFunction(anyLast, LowCardinality(String))). The
// aggregate transport drops that same uncovered wrapper, which is why
// max(saflc) is a bare String. resultStack routes the uncovered wrapper
// through the ordinary LowCardinality disposition, so neither answer
// needs a name check.
//
// LowCardinality is kept, because lower and upper are on the
// argsFirstOnly path: that path reads the first argument only, thus no
// other argument can break the "all others constant" requirement
// (measured: lower(lc) is LowCardinality(String)). The disposition is
// fixed here and not left wrapperConditional, because the argsFirstOnly
// caller skips withResolvedLowCardinality for a function that carries
// its own transport.
var caseFoldingTransport = wrapperTransport{
	lowCardinality:      wrapperKeep,
	nullable:            wrapperKeep,
	simpleAggregate:     wrapperConditional,
	simpleAggregateWhen: caseFoldingKeepsSimpleAggregateCondition,
	// lower and upper are scalar, thus they must not strip a
	// LowCardinality wrapper inside a container member.
	stripNested: false,
}

// callKeepsSimpleAggregateMarker reports whether the
// SimpleAggregateFunction marker of the first wrapped argument survives
// a function that READS that argument as a value.
//
// It is the ONE bridge from the wrapper stack of a call to the measured
// rule in simpleAggregateMarkerSurvives. The two transports that read
// the marker as a value both go through it, thus the rule has exactly
// one statement in the package and the two cannot drift apart.
//
// The helper reads the ORIGINAL wrapper type and not the split base.
// splitWrapperStack removes a Nullable from INSIDE the marker before it
// reports the base, thus the base of
// SimpleAggregateFunction(anyLast, Nullable(Int32)) is a bare Int32 and
// a rule that read the base would answer "scalar" for a Nullable inner.
// Params[1] of the stored wrapper is the inner type exactly as the
// server spelled it.
func callKeepsSimpleAggregateMarker(call wrapperCall) bool {
	return callMarkerInnerSatisfies(call, simpleAggregateMarkerSurvives)
}

// callKeepsValuePreservingScalarMarker is the twin bridge for the
// value-preserving scalar and window family. It uses the same stack walk
// and a different measured rule. See
// simpleAggregateMarkerSurvivesValuePreserving for the exact roster, the
// measurement table, and the one cell where the two rules disagree.
func callKeepsValuePreservingScalarMarker(call wrapperCall) bool {
	return callMarkerInnerSatisfies(call, simpleAggregateMarkerSurvivesValuePreserving)
}

// callMarkerInnerSatisfies finds the inner type of the marker that the
// call carries and applies the given rule to it. A call with no marker
// keeps nothing, thus it answers false.
func callMarkerInnerSatisfies(call wrapperCall, survives func(CHType) bool) bool {
	inner, ok := callMarkerInner(call)
	if !ok {
		return false
	}
	return survives(inner)
}

// callMarkerInner reports the inner type T of the
// SimpleAggregateFunction(f, T) marker that the call carries, and
// whether the call carries one at all.
//
// It holds the ONE stack walk of this file, so that a new marker rule
// adds a rule only and cannot make the walk drift. A caller that must
// tell "no marker" apart from "a marker that does not survive" uses this
// helper rather than the boolean bridges.
func callMarkerInner(call wrapperCall) (CHType, bool) {
	for _, stack := range call.stacks {
		if stack.simpleAggregate == nil {
			continue
		}
		wrapper := stack.simpleAggregate.wrapper
		if len(wrapper.Params) != 2 {
			return CHType{}, false
		}
		return wrapper.Params[1], true
	}
	return CHType{}, false
}

// caseFoldingKeepsSimpleAggregateCondition holds when the inner type of
// the SimpleAggregateFunction wrapper is a BARE SCALAR. See
// caseFoldingTransport for the measurements of the scalar cells.
//
// The condition read the inner NULLABILITY only until the regression, and a
// nullability test keeps the marker over a COMPOSITE inner type, which
// is a silently wrong type.
//
// lower and upper take text, thus LowCardinality is the ONE composite
// inner type that this family accepts, and it decides the rule on its
// own. Measured on ClickHouse 25.8.29.51 with real columns of a real
// AggregatingMergeTree table, where saflc is
// SimpleAggregateFunction(anyLast, LowCardinality(String)) and saflcn is
// SimpleAggregateFunction(anyLast, LowCardinality(Nullable(String))):
//
//	lower(saflc)   LowCardinality(String)
//	upper(saflc)   LowCardinality(String)
//	lower(saflcn)  LowCardinality(Nullable(String))
//	upper(saflcn)  LowCardinality(Nullable(String))
//
// Neither inner type is Nullable at the marker's own level, thus the old
// condition kept the marker on all four. The marker is gone, and the
// LowCardinality is KEPT: lower and upper are scalar, so unlike a true
// aggregate they do not strip the wrapper that the marker hid. Compare
// max(saflc), which is a bare String. That difference is why the two
// transports keep their own dispositions and share only this condition.
//
// An Array, Map or Tuple inner is Code 43, ILLEGAL_TYPE_OF_ARGUMENT, for
// this family. Code 43 is type evidence, but it is evidence about the
// ARGUMENT DOMAIN of lower and upper and not about the marker, thus
// those cells cannot decide this rule and they are not used for it.
func caseFoldingKeepsSimpleAggregateCondition(call wrapperCall) bool {
	return callKeepsSimpleAggregateMarker(call)
}

// aggregateKeepsSimpleAggregateCondition decides whether a
// value-preserving aggregate keeps the SimpleAggregateFunction wrapper.
//
// The wrapper survives only when the INNER type of that wrapper is a
// BARE SCALAR. Any other inner type makes the server answer that inner
// type with no marker at all.
//
// Measured on ClickHouse 25.8.29.51 with real columns of a real table,
// where sagg is SimpleAggregateFunction(sum, Int64), saggn is
// SimpleAggregateFunction(sum, Nullable(Int64)), saggs is
// SimpleAggregateFunction(min, String) and saggsn is
// SimpleAggregateFunction(min, Nullable(String)):
//
//	max(sagg)                  SimpleAggregateFunction(sum, Int64)
//	max(saggn)                 Nullable(Int64)
//	max(saggs)                 SimpleAggregateFunction(min, String)
//	max(saggsn)                Nullable(String)
//
// The same pair of answers came from min, any, anyLast, anyHeavy,
// argMax, argMin and each of their -If variants, thus the split is on
// the inner type and not on the name.
//
// THE RULE IS NOT ABOUT NULLABILITY. The condition read the inner
// nullability only until the regression, and the four cells above cannot see
// the difference, because a nullability rule and the measured rule agree
// on every scalar inner and on every Nullable inner. They disagree on a
// NON-Nullable COMPOSITE inner, and there the old condition gave a
// silently wrong type. Measured on the same server with the columns of
// simple_aggregate_marker_composite_inner_test.go, all of whose
// inner types are non-Nullable:
//
//	max(safarrb)          Array(Int32)
//	max(safarrs)          Array(String)
//	max(safmapb)          Map(String, Int32)
//	max(saftup)           Tuple(Int32, Int32)
//	max(saflc)            String
//	anyLast(safarrb)      Array(Int32)
//	first_value(safarrb)  Array(Int32)
//	argMax(saftup, i32)   Tuple(Int32, Int32)
//
// The LowCardinality cell needs two rules at once: the marker drops, and
// then the aggregate strips the LowCardinality that the marker hid,
// because every true aggregate removes LowCardinality at every depth.
// That second step is the stripNested field of the transport, not this
// condition. The scalar case-folding neighbour lower(saflc) keeps the
// LowCardinality, which is why the two transports are separate.
//
// The condition reads the ORIGINAL wrapper of the first argument, not
// the outer Nullable of the call. That distinction matters: a Nullable
// ordering argument adds a Nullable OUTSIDE a wrapper that stays.
// Measured:
//
//	argMax(sagg, ni64)   Nullable(SimpleAggregateFunction(sum, Int64))
//	argMax(saggn, ni64)  Nullable(Int64)
//
// The rule itself lives in simpleAggregateMarkerSurvives, which the regression
// measured through four independent witnesses. This condition calls it
// through callKeepsSimpleAggregateMarker rather than restating it.
func aggregateKeepsSimpleAggregateCondition(call wrapperCall) bool {
	return callKeepsSimpleAggregateMarker(call)
}

// windowValuePreservingTransport is the transport of the window functions
// that give one input value back without reading through its marker.
//
// Their class is wrapperAggregate, because they give a data value back,
// but they do not unwrap a Nullable inner type the way a true aggregate
// does. Measured on ClickHouse
// 25.8.29.51 with real columns:
//
//	lagInFrame(sagg, 1)     SimpleAggregateFunction(sum, Int64)
//	lagInFrame(saggn, 1)    SimpleAggregateFunction(sum, Nullable(Int64))
//	lagInFrame(saggsn, 1)   SimpleAggregateFunction(min, Nullable(String))
//	leadInFrame(saggn, 1)   SimpleAggregateFunction(sum, Nullable(Int64))
//
// lag, lead, nth_value, and the RESPECT NULLS value functions follow the
// same rule. The neighbour functions first_value and last_value DO unwrap,
// thus they keep the class transport (measured: first_value(saggn) is
// Nullable(Int64), first_value(sagg) keeps the wrapper).
var windowValuePreservingTransport = wrapperTransport{
	lowCardinality:      wrapperDrop,
	nullable:            wrapperKeep,
	simpleAggregate:     wrapperConditional,
	simpleAggregateWhen: callKeepsValuePreservingScalarMarker,
	stripNested:         true,
}

// wrapperValuePreservingRules is the CLOSED list of argsGeneric rules
// that give their ARGUMENT back rather than computing a new value, and
// that therefore own the SimpleAggregateFunction wrapper of that
// argument.
//
// Such a rule receives the wrapper and keeps it, exactly as a
// wrapperOpaque rule does.
//
// Keep this list SHORT. A name belongs here only when a measurement
// shows the server nests the wrapper under a wrapper that the rule adds,
// AND the ONE applier cannot rebuild that nesting itself.
//
// The list is EMPTY today. nullIf was its only member, and the regression
// measured that the exemption gave the wrong answer for two of the three
// inner types. The name now carries nullIfTransport instead, and the ONE
// applier rebuilds the nesting through the innerNullable flag of the
// stack. See nullIfTransport for the measured grid.
//
// The list and its guard test stay, because the shape they describe is
// real: a rule CAN nest a wrapper under a wrapper that it adds. Keep it
// empty until a measurement shows another such name.
var wrapperValuePreservingRules = map[string]bool{}

// greatestLeastTransport is the transport of greatest and least.
//
// These two names are the reason the class enum was too coarse. Their
// class is wrapperAggregate, because they take a supertype over their
// arguments, but they are SCALAR functions, and the measured grid is
// asymmetric on two independent axes:
//
//   - LowCardinality is kept at arity 1 only.
//   - SimpleAggregateFunction is dropped at arity 2 or more, and only
//     when the inner type is numeric.
//
// Both axes are expressed as conditions over the facts of the call. The
// applier holds no name-based special case; before this spec it did.
//
// See greatestLeastKeepsLowCardinality and greatestLeastNumericInner for
// the full measurement tables.
var greatestLeastTransport = wrapperTransport{
	lowCardinality:     wrapperConditional,
	lowCardinalityWhen: greatestLeastKeepsLowCardinalityCondition,
	nullable:           wrapperKeep,
	simpleAggregate:    wrapperConditional,
	// The wrapper SURVIVES when the drop condition does not hold, thus
	// the condition here is the negation of the drop.
	simpleAggregateWhen: greatestLeastKeepsSimpleAggregateCondition,
	// greatest and least are scalar, thus they must not strip a
	// LowCardinality wrapper inside a container member.
	stripNested: false,
}

// greatestLeastKeepsLowCardinalityCondition holds at arity 1 only.
// Measured: greatest(lc) is LowCardinality(String), greatest(lc, lc) is
// String.
//
// The measurement table lives on greatestLeastKeepsLowCardinality, and
// this condition delegates to it so that there is ONE source of truth.
func greatestLeastKeepsLowCardinalityCondition(call wrapperCall) bool {
	return greatestLeastKeepsLowCardinality(call.argCount)
}

// greatestLeastKeepsSimpleAggregateCondition holds at arity 1, and at
// arity 2 or more when the inner type is NOT numeric. Measured:
// greatest(sagg) keeps the wrapper, greatest(sagg, i64) is Int64, and
// greatest(saggs, s) keeps the wrapper because String is not numeric.
func greatestLeastKeepsSimpleAggregateCondition(call wrapperCall) bool {
	// The inner type decides first. A marker over a LowCardinality,
	// Array, Map or Tuple inner does not survive this family at ANY
	// arity, thus that test comes before the arity test. See
	// simpleAggregateMarkerSurvivesValuePreserving.
	if marker, ok := callMarkerInner(call); ok &&
		!simpleAggregateMarkerSurvivesValuePreserving(marker) {
		return false
	}
	if call.argCount < 2 {
		return true
	}
	return !greatestLeastNumericInner(call.base)
}

// withResolvedLowCardinality returns the transport with its
// LowCardinality disposition already decided.
//
// The measured LowCardinality rule of a wrapperTransparent function
// needs the argument EXPRESSIONS, not only their types: the wrapper
// survives when exactly one argument is LowCardinality and every other
// argument is a constant literal in the SQL text. The wrapperCall facts
// deliberately carry no expressions, thus a caller that has already
// evaluated that rule fixes the disposition here.
//
// A caller on the argsFirstOnly path passes true, because that path
// reads the FIRST argument only. There is no other argument that could
// break the "all others constant" requirement, so the wrapper always
// survives (measured: lower(lc) is LowCardinality(String),
// upper(lcn) is LowCardinality(Nullable(String))).
func (transport wrapperTransport) withResolvedLowCardinality(keep bool) wrapperTransport {
	if transport.lowCardinality != wrapperConditional {
		return transport
	}
	resolved := transport
	if keep {
		resolved.lowCardinality = wrapperKeep
	} else {
		resolved.lowCardinality = wrapperDrop
	}
	resolved.lowCardinalityWhen = nil
	return resolved
}

// applyWrapperTransport is the ONE applier. It puts the surviving
// wrappers on a result that a rule produced from BARE base types.
//
// This is the only place that decides which wrappers a function result
// carries. A caller that needs a different answer changes the transport
// of the function, never this function.
func applyWrapperTransport(result CHType, transport wrapperTransport, call wrapperCall) CHType {
	stack := transport.resultStack(call)
	if transport.stripNested && !stack.lowCardinality {
		result = stripNestedLowCardinality(result)
	}
	return applyWrapperStack(result, stack)
}
