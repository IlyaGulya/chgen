package engine

//go:generate go run ../tooling/cmd/gensemantics -root .

import (
	"fmt"
	"strings"
)

// The function registry. One map, one entry per function, so that the
// type rule of a function and the wrapper class of that function are one
// lexical unit. Before this file the same knowledge was in four maps:
// functionTypeRules, functionWrapperClasses,
// functionsWithIndependentResultType and functionsWithFirstArgumentResult.
// A merge could carry one of the four and drop another, and a rule
// without its evaluation strategy took the wrong path in silence.
//
// The comments beside an entry are measurements against a live
// ClickHouse server. Do not change one without a new measurement.

// argumentStrategy says how inferFunctionType reaches the arguments of a
// call before it applies the type rule.
type argumentStrategy int

const (
	// argsUnset is the zero value, and it is never legal. A spec that
	// forgets the strategy therefore fails the guard test
	// TestEverySpecDeclaresAStrategy instead of silently taking the
	// generic path.
	argsUnset argumentStrategy = iota
	// These functions have a fixed result type. Their arguments do not need to be
	// resolved first, which is important for predicates such as countIf(a = ?)
	// where the placeholder itself has no result type.
	argsIndependent
	// These functions derive their result from the first argument. ClickHouse
	// allows arbitrary expressions in their remaining arguments (for example the
	// tuple used as the ordering key of argMax), so resolving those arguments is
	// unnecessary and would reject otherwise typeable queries.
	argsFirstOnly
	// argsGeneric: every argument is resolved before the rule runs.
	argsGeneric
)

// functionFact is a measured rule that separates members of one broad
// function class. A fact is a bit so that one spec can hold more than one.
type functionFact uint8

const (
	functionFactComparesArgPair functionFact = 1 << iota
	functionFactDynamicForcesNullable
)

// resultRuleMode says which path computes the result type. Its zero value is
// illegal so that an omitted fact cannot select a path.
type resultRuleMode uint8

const (
	resultRuleUnknown resultRuleMode = iota
	resultRuleGeneric
	resultRuleSpecialRoute
)

// argumentDomainMode says how the server limits the value arguments. Its zero
// value is illegal. An unrestricted domain and a special-route domain are
// different facts even though neither one needs an argumentDomain value.
type argumentDomainMode uint8

const (
	argumentDomainUnknown argumentDomainMode = iota
	argumentDomainUnrestricted
	argumentDomainRestricted
	argumentDomainSpecialRoute
)

// semanticEvidenceRef names the measurement set that supports a function
// specification. The empty value is illegal.
type semanticEvidenceRef string

const registryMeasurementEvidence semanticEvidenceRef = "clickhouse-25.8.29.51/registry-real-columns"

// measuredFunctionFact connects one function fact to its measurement. The
// fact and the evidence must both be set.
type measuredFunctionFact struct {
	fact     functionFact
	evidence semanticEvidenceRef
}

func measuredFacts(facts ...functionFact) []measuredFunctionFact {
	result := make([]measuredFunctionFact, 0, len(facts))
	for _, factSet := range facts {
		known := functionFactComparesArgPair | functionFactDynamicForcesNullable
		if unknown := factSet &^ known; unknown != 0 {
			panic(fmt.Sprintf("measured function facts contain unknown bits %d", unknown))
		}
		for _, fact := range []functionFact{
			functionFactComparesArgPair,
			functionFactDynamicForcesNullable,
		} {
			if factSet&fact == 0 {
				continue
			}
			var evidence semanticEvidenceRef
			switch fact {
			case functionFactComparesArgPair:
				evidence = "clickhouse-25.8.29.51/real-columns/comparable-pair"
			case functionFactDynamicForcesNullable:
				evidence = "clickhouse-25.8.29.51/real-dynamic-column/ignore-witness"
			}
			result = append(result, measuredFunctionFact{fact: fact, evidence: evidence})
		}
	}
	return result
}

// functionSpec holds everything the resolver knows about one function.
type functionSpec struct {
	// family selects one reusable result and probe contract.
	family functionSemanticFamily
	// The derived fields seal the family contract on the production spec.
	// A semantic source must leave these fields unset.
	derivedFamilyIdentity functionSemanticFamily
	derivedFamilyResult   functionResultPolicy
	derivedFamilyProbe    functionProbePolicy
	// signature is the measured server call shape. The registry builder
	// derives the common form from gen and applies measured family overrides.
	signature *functionSignature
	// resultMode states whether the generic rule or a special route computes
	// the result. It must be set.
	resultMode resultRuleMode
	// rule gives the result type. It is nil for a function whose
	// result depends on an argument EXPRESSION and not only on the
	// argument types; inferFunctionType routes such a name to its own
	// inference function before the registry lookup. A nil rule is
	// therefore not a gap: it says "this name never reaches the
	// generic rule path".
	rule functionTypeRule
	// class says how the Nullable and LowCardinality wrappers of the
	// arguments move into the result.
	class functionWrapperClass
	// strategy says how the arguments are reached. It must be set.
	strategy argumentStrategy
	// domain is the measured argument domain for argumentDomainRestricted.
	// Other modes keep it nil.
	domain *argumentDomain
	// domainMode distinguishes an unrestricted domain, a restricted domain,
	// and a domain that a special route checks. It must be set.
	domainMode argumentDomainMode
	// domainArgs names WHICH arguments the domain applies to, by
	// zero-based index. A restricted domain must name every position.
	//
	// The field is a LIST and not a single index on purpose. dateDiff
	// needs the date set on arguments one AND two, and it must NOT
	// apply the set to argument zero (the String unit) or to argument
	// three (the String timezone). A single index could name one date
	// only, thus dateDiff('day', d, i32) would keep a type although
	// the server answers Code: 43. That is a silent wrong type, which
	// is the worst defect class, so the shape must be able to name
	// EVERY constrained position.
	//
	// A call with fewer arguments than a named index is a REFUSAL and
	// not a silent skip. See checkArgumentDomainAt.
	domainArgs []int
	// gen is the generator recipe: how to spell a call to this function. It
	// must be set because it also states the kind, arity, and constant
	// positions.
	// The recipe never holds the argument domain or the result type,
	// because the domain field and the rule field already hold them
	// executably. See gen_spec.go.
	gen *genSpec
	// comparesArgPair marks a function that compares its first two
	// arguments against each other, the same way the "=", "<" and
	// their neighbour OPERATORS do. Such a function needs the shared
	// comparability check (checkComparableOperandExprs), because a
	// fixed or first-argument result type does not say that the PAIR
	// is legal: nullIf(dec, s) and equals(dec, s) both answer
	// Code: 43, "No operation equals between Decimal(18, 4) and
	// String", on ClickHouse 25.8.29.51, and a rule that reads only
	// the result shape would hide that.
	//
	// This is a FACT ON THE SPEC and not a name literal, so that a
	// newly added comparing function is one field here rather than
	// one more "if name == ..." at a call site. See
	// functionComparesArgPairFor and its call sites in
	// inferFunctionType.
	// facts is the production bit set that the semantic registry builder
	// derives from measuredFacts. A semantic source entry must not set it.
	facts functionFact
	// measuredFacts is the single source for a fact and its evidence.
	measuredFacts []measuredFunctionFact
	// dynamicForcesNullable marks a function whose FIXED result type
	// gains a Nullable wrapper when any argument is Dynamic. The
	// server admits that a Dynamic column may hold no value for the
	// operation, thus the result of such a call can be NULL.
	//
	// This is a FACT ON THE SPEC and not a name literal, for the same
	// reason as comparesArgPair above.
	//
	// The field is needed because the function CLASS cannot separate
	// the two behaviours: toString, toInt64, toDate, hex, cityHash64
	// and equals are ALL wrapperTransparent, yet only some of them
	// wrap. Measured on ClickHouse 25.8.29.51 with a real Dynamic
	// column (dyn, holding CAST(3 AS Int32)) in a real table, every
	// cell paired with ignore(...), so the answer is the EXECUTION
	// type and not an analysis-time guess:
	//
	//	WRAPS                              DOES NOT WRAP
	//	equals(dyn, i32)    Nullable(UInt8)    toString(dyn)  String
	//	less(dyn, i32)      Nullable(UInt8)    toInt64(dyn)   Int64
	//	not(dyn)            Nullable(UInt8)    toDate(dyn)    Date
	//	cityHash64(dyn)     Nullable(UInt64)   toFloat64(dyn) Float64
	//	sipHash64(dyn)      Nullable(UInt64)   isNull(dyn)    UInt8
	//	xxHash64(dyn)       Nullable(UInt64)   isNotNull(dyn) UInt8
	//	farmHash64(dyn)     Nullable(UInt64)   toTypeName(dyn) String
	//	halfMD5(dyn)        Nullable(UInt64)
	//	javaHash(dyn)       Nullable(Int32)
	//	metroHash64(dyn)    Nullable(UInt64)
	//	murmurHash2_64(dyn) Nullable(UInt64)
	//	hex(dyn)            Nullable(String)
	//	bin(dyn)            Nullable(String)
	//
	// The separating rule: a CONVERSION names its target type and
	// consumes the Dynamic, thus it answers that exact type. A hash,
	// a comparison, or hex and bin, reads the value GENERICALLY, thus
	// the server keeps the "there may be no value" case in the type.
	// isNull and isNotNull answer ABOUT the null, thus they never
	// carry it; they are wrapperOpaque already and need no mark.
	//
	// The wrapper PROPAGATES through the ordinary Nullable machinery
	// once it is on the result. That was measured as well and needs
	// no rule of its own:
	//
	//	array(equals(dyn, d128))              Array(Nullable(UInt8))
	//	not(equals(dyn, d128))                Nullable(UInt8)
	//	groupBitOr(equals(dyn, d128))         Nullable(UInt8)
	//	sum(equals(dyn, d128))                Nullable(UInt64)
	//	plus(equals(dyn, d128), u8)           Nullable(UInt16)
	//	equals(abs(i128), equals(dyn, d128))  Nullable(UInt8)
	//
	// The mark is for Dynamic ALONE. Variant and JSON do not reach
	// these functions: the server refuses them outright, for example
	// equals(variant, d128) is Code 43, "Illegal types of arguments
	// (Variant(Int32, String), Decimal(38, 4))", and cityHash64(js)
	// is Code 48, "Method getDataAt is not supported".
	//
	// A Dynamic also cannot go INSIDE Nullable: toNullable(dyn) is
	// Code 43, "Nested type Dynamic cannot be inside Nullable". Thus
	// this mark must NEVER become a general "a Dynamic argument is a
	// Nullable argument" flag. The type-PASSTHROUGH functions keep
	// the bare Dynamic: identity(dyn), abs(dyn), plus(dyn, i32),
	// any(dyn), argMin(dyn, d) and greatest(dyn, i32) all answer
	// Dynamic, and coalesce(dyn, i32) answers Dynamic, not Int32.
	// parameterPolicy says how a rule can return a parametric type when
	// the curated parameter verdict table does not name the rule. The
	// zero value is unknown and must refuse. Most rules need no value:
	// a parameterless result passes, and a curated rule uses its verdict
	// rows. A lattice rule must state that its parameters are computed.
	parameterPolicy parameterResultPolicy
	// evidence names the measurement set for the base specification.
	evidence semanticEvidenceRef
}

// absFunctionType types abs. Measured on ClickHouse 25.8.29.51 with real
// columns:
//
//	abs(Int8)      -> UInt8     abs(UInt8)   -> UInt8
//	abs(Int32)     -> UInt32    abs(UInt32)  -> UInt32
//	abs(Int64)     -> UInt64    abs(UInt64)  -> UInt64
//	abs(Int128)    -> UInt128   abs(UInt128) -> UInt128
//	abs(Int256)    -> UInt256   abs(UInt256) -> UInt256
//	abs(Float32)   -> Float32   abs(Float64) -> Float64
//	abs(Decimal(18,4)) -> Decimal(18,4)      (the scale is unchanged)
//	abs(Bool)      -> UInt8
//	abs(String)    -> Code: 43   abs(Array(Int32)) -> Code: 43
//
// abs never keeps a signed integer type: every signed width widens to
// the unsigned type of the SAME width, because the magnitude of a
// negative value can equal the width limit. An unsigned argument stays
// at its own width, because it already holds a magnitude.
//
// avgArgumentDomain is reused rather than a new domain, because it is
// the same measured accept-set: an integer, a float or a Decimal.
func absFunctionType(args []CHType) (CHType, error) {
	first, err := firstFunctionArgument(args)
	if err != nil {
		return CHType{}, err
	}
	if arithmeticDecimalType(first) || floatBaseType(first) {
		return first, nil
	}
	class, ok := arithmeticIntegerClassOf(first)
	if !ok {
		return CHType{}, fmt.Errorf("function abs argument: unsupported type %s", first.String())
	}
	name, ok := arithmeticIntegerTypeNames[arithmeticIntegerClass{signed: false, size: class.size}]
	if !ok {
		return CHType{}, fmt.Errorf("function abs argument: unsupported integer width for %s", first.String())
	}
	return CHType{Name: name}, nil
}

// negateFunctionType types negate, which is also the unary "-" operator.
// Measured on ClickHouse 25.8.29.51 with real columns:
//
//	negate(Int8)    -> Int8      negate(UInt8)   -> Int16
//	negate(Int16)   -> Int16     negate(UInt16)  -> Int32
//	negate(Int32)   -> Int32     negate(UInt32)  -> Int64
//	negate(Int64)   -> Int64     negate(UInt64)  -> Int64   (capped, not Int128)
//	negate(Int128)  -> Int128    negate(UInt128) -> Int128
//	negate(Int256)  -> Int256    negate(UInt256) -> Int256
//	negate(Float32) -> Float32   negate(Float64) -> Float64
//	negate(Decimal(18,4)) -> Decimal(18,4)   (the scale is unchanged)
//	negate(Bool)    -> Int16
//	negate(String)  -> Code: 43  negate(Array(Int32)) -> Code: 43
//
// A signed argument keeps its own width, because its range already holds
// the negation. An unsigned argument widens to the signed type of DOUBLE
// its width, capped at 64 bits, which is the same cap that
// promotedArithmeticSize uses for the binary arithmetic operators.
func negateFunctionType(args []CHType) (CHType, error) {
	first, err := firstFunctionArgument(args)
	if err != nil {
		return CHType{}, err
	}
	if arithmeticDecimalType(first) || floatBaseType(first) {
		return first, nil
	}
	class, ok := arithmeticIntegerClassOf(first)
	if !ok {
		return CHType{}, fmt.Errorf("function negate argument: unsupported type %s", first.String())
	}
	size := class.size
	if !class.signed {
		size = promotedArithmeticSize(size)
	}
	name, ok := arithmeticIntegerTypeNames[arithmeticIntegerClass{signed: true, size: size}]
	if !ok {
		return CHType{}, fmt.Errorf("function negate argument: unsupported integer width for %s", first.String())
	}
	return CHType{Name: name}, nil
}

// functionRuleFor reports the type rule of a function, and false when
// the name is unknown or its spec has no rule.
func functionRuleFor(name string) (functionTypeRule, bool) {
	spec, ok := functionRegistry[name]
	if !ok || spec.rule == nil {
		return nil, false
	}
	return spec.rule, true
}

// functionClassFor reports the wrapper class of a function. An unknown
// name gives wrapperOpaque, which is the zero value and the previous
// behaviour of a lookup in functionWrapperClasses.
func functionClassFor(name string) functionWrapperClass {
	return functionRegistry[name].class
}

// functionStrategyFor reports the argument strategy of a function.
func functionStrategyFor(name string) argumentStrategy {
	return functionRegistry[name].strategy
}

// functionComparesArgPairFor reports whether a function compares its
// first two arguments against each other and therefore needs the
// shared comparability check. An unknown name gives false, which is
// the safe default: the check runs only where a measurement marked
// the spec.
func functionComparesArgPairFor(name string) bool {
	return functionRegistry[name].facts&functionFactComparesArgPair != 0
}

// functionDynamicForcesNullableFor reports whether a Dynamic argument
// puts a Nullable wrapper on the fixed result of this function. An
// unmarked function answers false, which is the safe default: the
// wrapper is added only where a measurement marked the spec. See the
// measured function facts.
func functionDynamicForcesNullableFor(name string) bool {
	return functionRegistry[name].facts&functionFactDynamicForcesNullable != 0
}

// isDynamicCHType reports whether a type is the Dynamic family. The
// test is the bare constructor name: Dynamic takes no parameter in a
// column declaration, and the server reports it as the plain name.
func isDynamicCHType(value CHType) bool {
	return strings.EqualFold(value.Name, "Dynamic")
}

func higherOrderSpecialSpec(spelling string, fold bool) functionSpec {
	sorts := []argSort{argSortLambda, argSortValue}
	if fold {
		sorts = append(sorts, argSortValue)
	}
	return functionSpec{
		family: semanticFamilyLambdaDedicated, class: wrapperOpaque, strategy: argsIndependent,
		gen:        &genSpec{spelling: spelling, minArity: len(sorts), maxArity: len(sorts), argSorts: sorts, place: placementScalar},
		resultMode: resultRuleSpecialRoute, domainMode: argumentDomainUnrestricted,
		parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence,
	}
}

func higherOrderPartialSortSpec(spelling string) functionSpec {
	return functionSpec{
		family: semanticFamilyLambdaDedicated, class: wrapperOpaque, strategy: argsIndependent,
		gen: &genSpec{spelling: spelling, minArity: 3, maxArity: 3,
			argSorts: []argSort{argSortLambda, argSortIndex, argSortValue}, place: placementScalar},
		resultMode: resultRuleSpecialRoute, domainMode: argumentDomainUnrestricted,
		parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence,
	}
}

// The class field of a spec classifies each function in the registry.
// The classes come from measurements on ClickHouse 25.8.29.51 with
// DESCRIBE (SELECT <expression> FROM t), for example:
//
//	toString(Nullable(Int32))          -> Nullable(String)
//	toString(LowCardinality(String))   -> LowCardinality(String)
//	equals(lc, lc)                     -> UInt8 (two non-constant args
//	                                     remove LowCardinality)
//	lc = 'a'                           -> LowCardinality(UInt8)
//	avg(Nullable(Int32))               -> Nullable(Float64)
//	any(LowCardinality(String))        -> String
//	argMax(f64, Nullable(Int32))       -> Nullable(Float64)
//	count(Nullable(String))            -> UInt64
//	groupArray(Nullable(String))       -> Array(String)
//
// The class lives in the same spec as the rule, so a rule can no longer
// exist without a class.
// functionSemanticSpecs is the single typed source for registered function
// semantics. The production registry and the generated audit roster come from
// this map. Do not add a second function list.
var functionSemanticSpecs = map[string]functionSpec{
	// count takes no argument in the "count(*)" form, thus its recipe
	// has a template and an arity of zero.
	"count":       {family: semanticFamilyAggregateFixedResult, rule: fixedFunctionType("UInt64"), class: wrapperOpaque, strategy: argsIndependent, gen: &genSpec{spelling: "count", minArity: 0, maxArity: 0, place: placementAggregate, template: "count(*)"}, resultMode: resultRuleGeneric, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"countif":     {family: semanticFamilyAggregateFixedResult, rule: fixedFunctionType("UInt64"), class: wrapperOpaque, strategy: argsIndependent, gen: &genSpec{spelling: "countIf", minArity: 1, maxArity: 1, argSorts: []argSort{argSortPredicate}, place: placementAggregate}, resultMode: resultRuleGeneric, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"uniq":        {family: semanticFamilyAggregateFixedResult, rule: fixedFunctionType("UInt64"), class: wrapperOpaque, strategy: argsIndependent, gen: &genSpec{spelling: "uniq", minArity: 1, maxArity: -1, argSorts: []argSort{argSortValue}, place: placementAggregate}, resultMode: resultRuleGeneric, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"uniqexact":   {family: semanticFamilyAggregateFixedResult, rule: fixedFunctionType("UInt64"), class: wrapperOpaque, strategy: argsIndependent, gen: &genSpec{spelling: "uniqExact", minArity: 1, maxArity: -1, argSorts: []argSort{argSortValue}, place: placementAggregate}, resultMode: resultRuleGeneric, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"uniqexactif": {family: semanticFamilyAggregateFixedResult, rule: fixedFunctionType("UInt64"), class: wrapperOpaque, strategy: argsIndependent, gen: conditionalAggregateCall("uniqExactIf", 1), resultMode: resultRuleGeneric, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	// uniqCombined is not opaque, unlike the rest of the uniq family.
	// It gives Nullable(UInt64) when an argument is Nullable. Measured
	// on ClickHouse 25.8.29.51 with real columns:
	//
	//	uniqCombined(String)                           -> UInt64
	//	uniqCombined(LowCardinality(String))           -> UInt64
	//	uniqCombined(Nullable(String))                 -> Nullable(UInt64)
	//	uniqCombined(LowCardinality(Nullable(String))) -> Nullable(UInt64)
	//	uniqCombined(Nullable(Int64))                  -> Nullable(UInt64)
	//
	// The trigger is the Nullable of the argument. The LowCardinality
	// wrapper does not change the result, and it never stays on the
	// result. The class wrapperAggregate says exactly that.
	//
	// The Nullable is a value, not a decoration. Over rows where the
	// argument is NULL in every row, uniqCombined(ns) gives NULL, but
	// uniq(ns) gives 0.
	//
	// The neighbours stay opaque. They were measured on the same
	// Nullable(String) column and they keep the bare UInt64:
	// uniq(ns), uniqExact(ns) and count(ns) are all UInt64.
	"uniqcombined": {family: semanticFamilyAggregateFixedResultParametric, rule: fixedFunctionType("UInt64"), class: wrapperAggregate, strategy: argsIndependent, gen: &genSpec{spelling: "uniqCombined", minArity: 1, maxArity: -1, argSorts: []argSort{argSortValue}, place: placementAggregate}, resultMode: resultRuleGeneric, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	// uniqCombined64 follows uniqCombined exactly: it gives
	// Nullable(UInt64) when the argument is Nullable and a bare UInt64
	// otherwise. Measured on ClickHouse 25.8.29.51 with real columns:
	//
	//	uniqCombined64(String)                           -> UInt64
	//	uniqCombined64(LowCardinality(String))           -> UInt64
	//	uniqCombined64(Nullable(String))                 -> Nullable(UInt64)
	//	uniqCombined64(LowCardinality(Nullable(String))) -> Nullable(UInt64)
	//
	// The class is wrapperAggregate, the same as uniqCombined, so that
	// the Nullable of any data argument reaches the result. Both functions
	// are variadic. aggregateDataArgCount must therefore count every
	// argument for both names.
	"uniqcombined64": {family: semanticFamilyAggregateFixedResultParametric, rule: fixedFunctionType("UInt64"), class: wrapperAggregate, strategy: argsIndependent, gen: &genSpec{spelling: "uniqCombined64", minArity: 1, maxArity: -1, argSorts: []argSort{argSortValue}, place: placementAggregate}, resultMode: resultRuleGeneric, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	// uniqHLL12 and uniqTheta are opaque, like uniq and uniqExact: they
	// give a bare UInt64 whatever the argument wrapper. Measured on
	// ClickHouse 25.8.29.51 with real columns:
	//
	//	uniqHLL12(String)           -> UInt64
	//	uniqHLL12(Nullable(String)) -> UInt64
	//	uniqTheta(String)           -> UInt64
	"uniqhll12":     {family: semanticFamilyAggregateFixedResult, rule: fixedFunctionType("UInt64"), class: wrapperOpaque, strategy: argsIndependent, gen: &genSpec{spelling: "uniqHLL12", minArity: 1, maxArity: -1, argSorts: []argSort{argSortValue}, place: placementAggregate}, resultMode: resultRuleGeneric, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"uniqtheta":     {family: semanticFamilyAggregateFixedResult, rule: fixedFunctionType("UInt64"), class: wrapperOpaque, strategy: argsIndependent, gen: &genSpec{spelling: "uniqTheta", minArity: 1, maxArity: -1, argSorts: []argSort{argSortValue}, place: placementAggregate}, resultMode: resultRuleGeneric, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"countdistinct": {family: semanticFamilyAggregateFixedResult, rule: fixedFunctionType("UInt64"), class: wrapperOpaque, strategy: argsIndependent, gen: &genSpec{spelling: "countDistinct", minArity: 1, maxArity: -1, argSorts: []argSort{argSortValue}, place: placementAggregate}, resultMode: resultRuleGeneric, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	// cityHash64 is variadic (measured: cityHash64(s, i32) is UInt64).
	"cityhash64": {family: semanticFamilyFixedResult, rule: fixedFunctionType("UInt64"), class: wrapperTransparent, strategy: argsIndependent, gen: &genSpec{spelling: "cityHash64", minArity: 1, maxArity: -1, argSorts: []argSort{argSortValue}, place: placementScalar}, measuredFacts: measuredFacts(functionFactDynamicForcesNullable), resultMode: resultRuleGeneric, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	// arrayExists takes a lambda first and the array second. It is a
	// predicate: it answers plain UInt8 even over Array(Bool)
	// (measured on 25.8.29.51: arrayExists(x -> x, Array(Bool)) is
	// UInt8).
	"arrayexists":             {family: semanticFamilyLambdaPredicate, rule: fixedFunctionType("UInt8"), class: wrapperOpaque, strategy: argsIndependent, gen: &genSpec{spelling: "arrayExists", minArity: 2, maxArity: 2, argSorts: []argSort{argSortLambda, argSortValue}, place: placementScalar}, resultMode: resultRuleGeneric, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"arrayall":                higherOrderSpecialSpec("arrayAll", false),
	"arrayavg":                higherOrderSpecialSpec("arrayAvg", false),
	"arraycount":              higherOrderSpecialSpec("arrayCount", false),
	"arraycumsum":             higherOrderSpecialSpec("arrayCumSum", false),
	"arraycumsumnonnegative":  higherOrderSpecialSpec("arrayCumSumNonNegative", false),
	"arrayfill":               higherOrderSpecialSpec("arrayFill", false),
	"arrayfilter":             higherOrderSpecialSpec("arrayFilter", false),
	"arrayfirst":              higherOrderSpecialSpec("arrayFirst", false),
	"arrayfirstindex":         higherOrderSpecialSpec("arrayFirstIndex", false),
	"arrayfirstornull":        higherOrderSpecialSpec("arrayFirstOrNull", false),
	"arrayfold":               higherOrderSpecialSpec("arrayFold", true),
	"arraylast":               higherOrderSpecialSpec("arrayLast", false),
	"arraylastindex":          higherOrderSpecialSpec("arrayLastIndex", false),
	"arraylastornull":         higherOrderSpecialSpec("arrayLastOrNull", false),
	"arraymap":                higherOrderSpecialSpec("arrayMap", false),
	"arraymax":                higherOrderSpecialSpec("arrayMax", false),
	"arraymin":                higherOrderSpecialSpec("arrayMin", false),
	"arrayproduct":            higherOrderSpecialSpec("arrayProduct", false),
	"arraypartialsort":        higherOrderPartialSortSpec("arrayPartialSort"),
	"arraypartialreversesort": higherOrderPartialSortSpec("arrayPartialReverseSort"),
	"arrayreversefill":        higherOrderSpecialSpec("arrayReverseFill", false),
	"arrayreversesort":        higherOrderSpecialSpec("arrayReverseSort", false),
	"arrayreversesplit":       higherOrderSpecialSpec("arrayReverseSplit", false),
	"arraysplit":              higherOrderSpecialSpec("arraySplit", false),
	"arraysum":                higherOrderSpecialSpec("arraySum", false),
	// empty is a predicate: empty(Array(Bool)) is UInt8, not Bool
	// (measured on 25.8.29.51).
	"empty": {family: semanticFamilyPredicate, rule: fixedFunctionType("UInt8"), class: wrapperTransparent, strategy: argsIndependent, domain: &countableArgumentDomain, gen: scalarCall("empty", 1), resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},

	// dateDiff gives Int64 for every unit and every temporal argument
	// pair, and adds Nullable when a temporal argument is Nullable
	// (measured on ClickHouse 25.8.29.51: dateDiff('day', d, dt) is
	// Int64, dateDiff('day', toNullable(d), dt) is Nullable(Int64)).
	// The first argument of dateDiff is the unit, and it is a string
	// constant that the server reads at parse time.
	// dateDiff and date_diff read the SAME date set that toStartOfDay
	// reads, but they take the unit as their FIRST argument and the two
	// dates after it: dateDiff('day', d, d) is Int64, and
	// dateDiff('day', i32, i32) is Code: 43.
	//
	// The domainArgs field states that: the date set applies to
	// arguments one and two, and to no other position. Measured on
	// ClickHouse 25.8.29.51 with real columns in a real table:
	//
	//	dateDiff('day', d, dt)          Int64
	//	dateDiff('day', nd, d)          Nullable(Int64)
	//	dateDiff('day', i32, d)         Code: 43, "2nd argument 'startdate'"
	//	dateDiff('day', d, i32)         Code: 43, "3rd argument 'enddate'"
	//	dateDiff('day', d, dt, 'UTC')   Int64
	//
	// BOTH date positions need the check, because the server names
	// either one. A single index would leave dateDiff('day', d, i32)
	// with an Int64 answer, which is a silent wrong type.
	//
	// Argument zero stays free of the set: all 21 measured units
	// ('day', 'ns', 'quarter' and the rest) are String and each gives
	// Int64. Argument three stays free too: it is the String timezone.
	"datediff":  {family: semanticFamilyFixedResult, rule: fixedFunctionType("Int64"), class: wrapperTransparent, strategy: argsIndependent, domain: &dateArgumentDomain, domainArgs: []int{1, 2}, gen: &genSpec{spelling: "dateDiff", minArity: 3, maxArity: 3, argSorts: []argSort{argSortConstString, argSortValue, argSortValue}, place: placementScalar}, resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"date_diff": {family: semanticFamilyFixedResult, rule: fixedFunctionType("Int64"), class: wrapperTransparent, strategy: argsIndependent, domain: &dateArgumentDomain, domainArgs: []int{1, 2}, gen: &genSpec{spelling: "date_diff", minArity: 3, maxArity: 3, argSorts: []argSort{argSortConstString, argSortValue, argSortValue}, place: placementScalar}, resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},

	"tostring": {family: semanticFamilyConversion, rule: fixedFunctionType("String"), class: wrapperTransparent, strategy: argsIndependent, gen: scalarCall("toString", 1), resultMode: resultRuleGeneric, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	// concat is NOT a constant String. Over containers of one kind it
	// JOINS them and keeps the container type. See concatFunctionType
	// for the measured table. The "||" operator reads this same entry,
	// thus the two spellings cannot drift apart.
	//
	// The strategy is argsGeneric and NOT argsIndependent. The rule must
	// READ its arguments to tell the join case from the string case, and
	// argsIndependent calls the rule with nil.
	"concat": {family: semanticFamilyCommonSupertype, rule: concatFunctionType, class: wrapperTransparent, strategy: argsGeneric, gen: &genSpec{spelling: "concat", minArity: 2, maxArity: -1, argSorts: []argSort{argSortValue}, place: placementScalar}, measuredFacts: measuredFacts(functionFactDynamicForcesNullable), parameterPolicy: parameterResultLattice, resultMode: resultRuleGeneric, domainMode: argumentDomainUnrestricted, evidence: registryMeasurementEvidence},
	// substring takes an offset and an optional length. Both are
	// integers (measured: substring(s, 1) and substring(s, 1, 2) are
	// both String).
	// substring reads its first argument as text, and it accepts the
	// Enums as well as String and FixedString, thus it does NOT share
	// the narrower stringArgumentDomain that lower and trim use
	// (measured: substring(e8, 2) returns a row, trim(e8) is Code: 43).
	"substring": {family: semanticFamilyFixedResult, rule: fixedFunctionType("String"), class: wrapperTransparent, strategy: argsIndependent, domain: &textArgumentDomain, gen: &genSpec{spelling: "substring", minArity: 2, maxArity: 3, argSorts: []argSort{argSortValue, argSortConstInt, argSortConstInt}, place: placementScalar}, resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	// hex, not toHex: ClickHouse has no toHex function at all (measured
	// on 25.8.29.51: every argument gives Code: 46 UNKNOWN_FUNCTION).
	// hex gives String for every accepted argument and keeps the
	// Nullable and LowCardinality wrappers of its argument (measured:
	// hex(s) is String, hex(ns) is Nullable(String), hex(lc) is
	// LowCardinality(String)).
	// hex, the toXxx conversions and nullIf all read ONE value and
	// refuse a container. hex does NOT share scalarArgumentDomain
	// though: it also refuses a DateTime64 argument (Code: 44) and an
	// Enum argument (Code: 43), two more measured gaps that no other
	// member of the scalar family has. See hexArgumentDomain for the
	// measured grid.
	"hex": {family: semanticFamilyFixedResult, rule: fixedFunctionType("String"), class: wrapperTransparent, strategy: argsIndependent, domain: &hexArgumentDomain, gen: scalarCall("hex", 1), measuredFacts: measuredFacts(functionFactDynamicForcesNullable), resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	// Native-integer cursor decoding; see cursor_functions.go for the
	// measured promotion and timestamp rules.
	"bitshiftright": {
		family: semanticFamilyDedicated, rule: bitShiftRightFunctionType,
		class: wrapperTransparent, strategy: argsGeneric,
		domain: &bitShiftIntegerDomain, domainArgs: []int{0, 1},
		gen:        scalarCall("bitShiftRight", 2),
		resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted,
		parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence,
	},
	"fromunixtimestamp64milli": {
		family: semanticFamilyContextDependent,
		class:  wrapperTransparent, strategy: argsGeneric,
		domain: &bitShiftIntegerDomain, domainArgs: []int{0},
		gen: &genSpec{
			spelling: "fromUnixTimestamp64Milli", minArity: 1, maxArity: 2,
			argSorts: []argSort{argSortValue, argSortConstString}, place: placementScalar,
		},
		resultMode: resultRuleSpecialRoute, domainMode: argumentDomainRestricted,
		parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence,
	},
	"abs":    {family: semanticFamilyDedicated, rule: absFunctionType, class: wrapperTransparent, strategy: argsFirstOnly, domain: &avgArgumentDomain, gen: scalarCall("abs", 1), resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"negate": {family: semanticFamilyDedicated, rule: negateFunctionType, class: wrapperTransparent, strategy: argsFirstOnly, domain: &avgArgumentDomain, gen: scalarCall("negate", 1), resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	// round, roundBankers, floor, ceil and trunc/truncate all give their
	// FIRST argument's type back UNCHANGED, whatever the optional digits
	// argument says (measured on ClickHouse 25.8.29.51 over real
	// columns: round(d2, 1), round(d2, 0) and round(d2, 10) are all
	// Decimal(18, 4), the exact input scale and precision; round(b, 1)
	// is UInt8; roundBankers, floor, ceil and trunc give the identical
	// Decimal(18, 4) for the same d2 column). They share abs and
	// negate's accept-set (measured: round(s, 1), round(fs8, 1) and
	// round(e8, 1) are all Code: 43, "Expected: A number to round"), so
	// avgArgumentDomain is reused rather than a new measured set.
	// firstFunctionArgument is the identity rule with no per-type
	// branch, because every measured type, including the wide integers
	// and the Decimals, comes back unchanged. The regression.
	"round":        {family: semanticFamilyFirstArgument, rule: firstFunctionArgument, class: wrapperTransparent, strategy: argsFirstOnly, domain: &avgArgumentDomain, gen: &genSpec{spelling: "round", minArity: 1, maxArity: 2, argSorts: []argSort{argSortValue, argSortConstInt}, place: placementScalar}, resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"roundbankers": {family: semanticFamilyFirstArgument, rule: firstFunctionArgument, class: wrapperTransparent, strategy: argsFirstOnly, domain: &avgArgumentDomain, gen: &genSpec{spelling: "roundBankers", minArity: 1, maxArity: 2, argSorts: []argSort{argSortValue, argSortConstInt}, place: placementScalar}, resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"floor":        {family: semanticFamilyFirstArgument, rule: firstFunctionArgument, class: wrapperTransparent, strategy: argsFirstOnly, domain: &avgArgumentDomain, gen: &genSpec{spelling: "floor", minArity: 1, maxArity: 2, argSorts: []argSort{argSortValue, argSortConstInt}, place: placementScalar}, resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"ceil":         {family: semanticFamilyFirstArgument, rule: firstFunctionArgument, class: wrapperTransparent, strategy: argsFirstOnly, domain: &avgArgumentDomain, gen: &genSpec{spelling: "ceil", minArity: 1, maxArity: 2, argSorts: []argSort{argSortValue, argSortConstInt}, place: placementScalar}, resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"trunc":        {family: semanticFamilyFirstArgument, rule: firstFunctionArgument, class: wrapperTransparent, strategy: argsFirstOnly, domain: &avgArgumentDomain, gen: &genSpec{spelling: "trunc", minArity: 1, maxArity: 2, argSorts: []argSort{argSortValue, argSortConstInt}, place: placementScalar}, resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"truncate":     {family: semanticFamilyFirstArgument, rule: firstFunctionArgument, class: wrapperTransparent, strategy: argsFirstOnly, domain: &avgArgumentDomain, gen: &genSpec{spelling: "truncate", minArity: 1, maxArity: 2, argSorts: []argSort{argSortValue, argSortConstInt}, place: placementScalar}, resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	// upper and lower keep a FixedString(N) argument at its width and
	// give String for every other string-like argument (measured on
	// ClickHouse 25.8.29.51: upper(fs) is FixedString(8), upper(s) is
	// String, upper(lc) is LowCardinality(String)). trim is not in this
	// group: trim(fs) is String.
	"lower": {family: semanticFamilyDedicated, rule: caseFoldingFunctionType, class: wrapperTransparent, strategy: argsFirstOnly, domain: &stringArgumentDomain, gen: scalarCall("lower", 1), resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"upper": {family: semanticFamilyDedicated, rule: caseFoldingFunctionType, class: wrapperTransparent, strategy: argsFirstOnly, domain: &stringArgumentDomain, gen: scalarCall("upper", 1), resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	// reverse keeps a FixedString(N) argument at its width, the same
	// length-preserving shape as upper and lower, and gives String for
	// every other string-like argument (measured on ClickHouse
	// 25.8.29.51: reverse(fs8) is FixedString(8), reverse(fs16) is
	// FixedString(16), reverse(s) is String). It shares upper/lower's
	// stringArgumentDomain and REFUSES the Enums that substring accepts
	// (measured: reverse(e8) is Code: 43, "Illegal type Enum8('a' = 1,
	// 'b' = 2) of argument of function reverse"; substring(e8, 2) RUNS).
	// substring itself does NOT keep the width: substring(fs8, 1, 2) is
	// String, never FixedString, because a substring can be SHORTER than
	// its source, so keeping the width would be a silently wrong length.
	// the regression.
	//
	// reverse ALSO reads a container, which upper and lower do not, thus
	// it has its own rule and its own domain: an Array keeps its element
	// type but loses an inner LowCardinality, and a Tuple comes back with
	// its ELEMENT ORDER reversed. A Map stays refused. See
	// reverseFunctionType and reverseArgumentDomain for the measured
	// table. The regression.
	"reverse": {family: semanticFamilyDedicated, rule: reverseFunctionType, class: wrapperTransparent, strategy: argsFirstOnly, domain: &reverseArgumentDomain, gen: scalarCall("reverse", 1), resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	// trim also has a "trim(BOTH ' ' FROM s)" form. The recipe keeps
	// the plain one-argument form, which the server accepts too
	// (measured: trim(s) is String).
	"trim":      {family: semanticFamilyFixedResult, rule: fixedFunctionType("String"), class: wrapperTransparent, strategy: argsIndependent, domain: &stringArgumentDomain, gen: scalarCall("trim", 1), resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"trimleft":  {family: semanticFamilyFixedResult, rule: fixedFunctionType("String"), class: wrapperTransparent, strategy: argsIndependent, domain: &stringArgumentDomain, gen: scalarCall("trimLeft", 1), resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"trimright": {family: semanticFamilyFixedResult, rule: fixedFunctionType("String"), class: wrapperTransparent, strategy: argsIndependent, domain: &stringArgumentDomain, gen: scalarCall("trimRight", 1), resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},

	// toDate, toDate32 and toDateTime additionally refuse a Decimal
	// argument (measured: toDate(dec) is Code: 44 for every Decimal
	// width), thus they use dateFromValueArgumentDomain and not the
	// wider scalarArgumentDomain. toDateTime64 keeps
	// scalarArgumentDomain: toDateTime64(dec, 3) runs.
	"todate":       {family: semanticFamilyConversion, rule: fixedFunctionType("Date"), class: wrapperTransparent, strategy: argsIndependent, domain: &dateFromValueArgumentDomain, gen: scalarCall("toDate", 1), resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"tostartofday": {family: semanticFamilyConversion, rule: fixedFunctionType("DateTime"), class: wrapperTransparent, strategy: argsIndependent, domain: &dateArgumentDomain, gen: scalarCall("toStartOfDay", 1), resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	// toStartOfHour and toStartOfMinute are routed to
	// inferTimezoneCarryingType, exactly as toStartOfDay is: the fixed
	// "DateTime" rule here is a placeholder that the router never calls.
	// It exists so the class, the strategy and the generator recipe have
	// one home, the same reason toStartOfDay keeps one. See
	// inferTimezoneCarryingType in infer_operators.go and
	// hourMinuteStartOfArgumentDomain in argument_domain.go for the
	// measured behaviour and the narrower domain (the regression).
	"tostartofhour":   {family: semanticFamilyConversion, rule: fixedFunctionType("DateTime"), class: wrapperTransparent, strategy: argsIndependent, domain: &hourMinuteStartOfArgumentDomain, gen: scalarCall("toStartOfHour", 1), resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"tostartofminute": {family: semanticFamilyConversion, rule: fixedFunctionType("DateTime"), class: wrapperTransparent, strategy: argsIndependent, domain: &hourMinuteStartOfArgumentDomain, gen: scalarCall("toStartOfMinute", 1), resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	// toYYYYMMDD reads Date, Date32, DateTime, and DateTime64 and returns
	// the calendar date as UInt32. A Nullable or LowCardinality wrapper
	// stays on the result. The server also accepts an optional timezone.
	// This rule keeps the one-argument form that the consumer needs. The
	// optional form stays an explicit refusal until its timezone values have
	// their own measured validation rule.
	//
	// Measured on ClickHouse 25.8.29.51 with analysis and execution over
	// real columns:
	//
	//	toYYYYMMDD(d)     UInt32
	//	toYYYYMMDD(d32)   UInt32
	//	toYYYYMMDD(dt)    UInt32
	//	toYYYYMMDD(dt64)  UInt32
	//	toYYYYMMDD(nd)    Nullable(UInt32)
	//	toYYYYMMDD(lcd)   LowCardinality(UInt32)
	//
	// String, Int32, Decimal, UUID, and Array(Date) all give Code 43.
	"toyyyymm": {
		family:          semanticFamilyConversion,
		rule:            fixedFunctionType("UInt32"),
		class:           wrapperTransparent,
		strategy:        argsIndependent,
		domain:          &dateArgumentDomain,
		gen:             scalarCall("toYYYYMM", 1),
		resultMode:      resultRuleGeneric,
		domainMode:      argumentDomainRestricted,
		domainArgs:      []int{0},
		parameterPolicy: parameterResultCurated,
		evidence:        registryMeasurementEvidence,
	},
	"toyyyymmdd": {
		family:          semanticFamilyConversion,
		rule:            fixedFunctionType("UInt32"),
		class:           wrapperTransparent,
		strategy:        argsIndependent,
		domain:          &dateArgumentDomain,
		gen:             scalarCall("toYYYYMMDD", 1),
		resultMode:      resultRuleGeneric,
		domainMode:      argumentDomainRestricted,
		domainArgs:      []int{0},
		parameterPolicy: parameterResultCurated,
		evidence:        registryMeasurementEvidence,
	},
	"toyyyymmddhhmmss": {
		family:          semanticFamilyConversion,
		rule:            fixedFunctionType("UInt64"),
		class:           wrapperTransparent,
		strategy:        argsIndependent,
		domain:          &dateArgumentDomain,
		gen:             scalarCall("toYYYYMMDDhhmmss", 1),
		resultMode:      resultRuleGeneric,
		domainMode:      argumentDomainRestricted,
		domainArgs:      []int{0},
		parameterPolicy: parameterResultCurated,
		evidence:        registryMeasurementEvidence,
	},

	// The addXxx/subtractXxx date-arithmetic family is routed to
	// inferTemporalShiftType before the generic rule lookup, exactly as
	// toStartOfDay is routed to inferTimezoneCarryingType: the fixed
	// "DateTime" rule below is a placeholder that the router never
	// calls, kept only so the class, the strategy, the domain and the
	// generator recipe have one home per name. See
	// inferTemporalShiftType and temporalShiftFunctions in
	// infer_operators.go, and addSubtractTemporalArgumentDomain in
	// argument_domain.go, for the measured behaviour (the regression).
	"adddays":          {family: semanticFamilyFixedResult, rule: fixedFunctionType("DateTime"), class: wrapperTransparent, strategy: argsIndependent, domain: &addSubtractTemporalArgumentDomain, gen: &genSpec{spelling: "addDays", minArity: 2, maxArity: 2, argSorts: []argSort{argSortValue, argSortConstInt}, place: placementScalar}, resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"subtractdays":     {family: semanticFamilyFixedResult, rule: fixedFunctionType("DateTime"), class: wrapperTransparent, strategy: argsIndependent, domain: &addSubtractTemporalArgumentDomain, gen: &genSpec{spelling: "subtractDays", minArity: 2, maxArity: 2, argSorts: []argSort{argSortValue, argSortConstInt}, place: placementScalar}, resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"addweeks":         {family: semanticFamilyFixedResult, rule: fixedFunctionType("DateTime"), class: wrapperTransparent, strategy: argsIndependent, domain: &addSubtractTemporalArgumentDomain, gen: &genSpec{spelling: "addWeeks", minArity: 2, maxArity: 2, argSorts: []argSort{argSortValue, argSortConstInt}, place: placementScalar}, resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"subtractweeks":    {family: semanticFamilyFixedResult, rule: fixedFunctionType("DateTime"), class: wrapperTransparent, strategy: argsIndependent, domain: &addSubtractTemporalArgumentDomain, gen: &genSpec{spelling: "subtractWeeks", minArity: 2, maxArity: 2, argSorts: []argSort{argSortValue, argSortConstInt}, place: placementScalar}, resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"addmonths":        {family: semanticFamilyFixedResult, rule: fixedFunctionType("DateTime"), class: wrapperTransparent, strategy: argsIndependent, domain: &addSubtractTemporalArgumentDomain, gen: &genSpec{spelling: "addMonths", minArity: 2, maxArity: 2, argSorts: []argSort{argSortValue, argSortConstInt}, place: placementScalar}, resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"subtractmonths":   {family: semanticFamilyFixedResult, rule: fixedFunctionType("DateTime"), class: wrapperTransparent, strategy: argsIndependent, domain: &addSubtractTemporalArgumentDomain, gen: &genSpec{spelling: "subtractMonths", minArity: 2, maxArity: 2, argSorts: []argSort{argSortValue, argSortConstInt}, place: placementScalar}, resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"addquarters":      {family: semanticFamilyFixedResult, rule: fixedFunctionType("DateTime"), class: wrapperTransparent, strategy: argsIndependent, domain: &addSubtractTemporalArgumentDomain, gen: &genSpec{spelling: "addQuarters", minArity: 2, maxArity: 2, argSorts: []argSort{argSortValue, argSortConstInt}, place: placementScalar}, resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"subtractquarters": {family: semanticFamilyFixedResult, rule: fixedFunctionType("DateTime"), class: wrapperTransparent, strategy: argsIndependent, domain: &addSubtractTemporalArgumentDomain, gen: &genSpec{spelling: "subtractQuarters", minArity: 2, maxArity: 2, argSorts: []argSort{argSortValue, argSortConstInt}, place: placementScalar}, resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"addyears":         {family: semanticFamilyFixedResult, rule: fixedFunctionType("DateTime"), class: wrapperTransparent, strategy: argsIndependent, domain: &addSubtractTemporalArgumentDomain, gen: &genSpec{spelling: "addYears", minArity: 2, maxArity: 2, argSorts: []argSort{argSortValue, argSortConstInt}, place: placementScalar}, resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"subtractyears":    {family: semanticFamilyFixedResult, rule: fixedFunctionType("DateTime"), class: wrapperTransparent, strategy: argsIndependent, domain: &addSubtractTemporalArgumentDomain, gen: &genSpec{spelling: "subtractYears", minArity: 2, maxArity: 2, argSorts: []argSort{argSortValue, argSortConstInt}, place: placementScalar}, resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"addhours":         {family: semanticFamilyFixedResult, rule: fixedFunctionType("DateTime"), class: wrapperTransparent, strategy: argsIndependent, domain: &addSubtractTemporalArgumentDomain, gen: &genSpec{spelling: "addHours", minArity: 2, maxArity: 2, argSorts: []argSort{argSortValue, argSortConstInt}, place: placementScalar}, resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"subtracthours":    {family: semanticFamilyFixedResult, rule: fixedFunctionType("DateTime"), class: wrapperTransparent, strategy: argsIndependent, domain: &addSubtractTemporalArgumentDomain, gen: &genSpec{spelling: "subtractHours", minArity: 2, maxArity: 2, argSorts: []argSort{argSortValue, argSortConstInt}, place: placementScalar}, resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"addminutes":       {family: semanticFamilyFixedResult, rule: fixedFunctionType("DateTime"), class: wrapperTransparent, strategy: argsIndependent, domain: &addSubtractTemporalArgumentDomain, gen: &genSpec{spelling: "addMinutes", minArity: 2, maxArity: 2, argSorts: []argSort{argSortValue, argSortConstInt}, place: placementScalar}, resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"subtractminutes":  {family: semanticFamilyFixedResult, rule: fixedFunctionType("DateTime"), class: wrapperTransparent, strategy: argsIndependent, domain: &addSubtractTemporalArgumentDomain, gen: &genSpec{spelling: "subtractMinutes", minArity: 2, maxArity: 2, argSorts: []argSort{argSortValue, argSortConstInt}, place: placementScalar}, resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"addseconds":       {family: semanticFamilyFixedResult, rule: fixedFunctionType("DateTime"), class: wrapperTransparent, strategy: argsIndependent, domain: &addSubtractTemporalArgumentDomain, gen: &genSpec{spelling: "addSeconds", minArity: 2, maxArity: 2, argSorts: []argSort{argSortValue, argSortConstInt}, place: placementScalar}, resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"subtractseconds":  {family: semanticFamilyFixedResult, rule: fixedFunctionType("DateTime"), class: wrapperTransparent, strategy: argsIndependent, domain: &addSubtractTemporalArgumentDomain, gen: &genSpec{spelling: "subtractSeconds", minArity: 2, maxArity: 2, argSorts: []argSort{argSortValue, argSortConstInt}, place: placementScalar}, resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"todate32":         {family: semanticFamilyConversion, rule: fixedFunctionType("Date32"), class: wrapperTransparent, strategy: argsIndependent, domain: &dateFromValueArgumentDomain, gen: scalarCall("toDate32", 1), resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"todatetime":       {family: semanticFamilyConversion, rule: fixedFunctionType("DateTime"), class: wrapperTransparent, strategy: argsIndependent, domain: &dateFromValueArgumentDomain, gen: scalarCall("toDateTime", 1), resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	// toDateTime64 needs the scale (measured: toDateTime64(dt) gives
	// Code: 42, toDateTime64(dt, 3) is DateTime64(3)).
	// toDateTime64 reads its argument as a number of seconds, thus it
	// refuses an AggregateFunction state the same way the other numeric
	// conversions do (measured: toDateTime64(agg, 3) is Code: 43). It
	// keeps castArgumentDomain and NOT dateFromValueArgumentDomain,
	// because unlike toDate, toDate32 and toDateTime it still accepts a
	// Decimal (measured: toDateTime64(dec, 3) RUNS).
	"todatetime64": {family: semanticFamilyConversion, rule: fixedFunctionType("DateTime64"), class: wrapperTransparent, strategy: argsIndependent, domain: &castArgumentDomain, gen: &genSpec{spelling: "toDateTime64", minArity: 2, maxArity: 2, argSorts: []argSort{argSortValue, argSortConstInt}, place: placementScalar}, resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	// toStartOfInterval and toTimeZone are special-cased in
	// inferFunctionType, because their result depends on an argument
	// EXPRESSION (the INTERVAL unit, the timezone literal) and not only
	// on the argument types. Their specs carry no rule: the registry
	// no longer needs a placeholder rule to keep a rule table and a
	// class table paired, because the class lives in the spec itself.
	// TestSpecWithoutARuleIsRoutedElsewhere names them, so a spec that
	// loses its rule by accident still fails.
	// toStartOfInterval takes an INTERVAL expression, which is not an
	// ordinary argument list, thus its recipe carries a template. The
	// second verb takes the unit name.
	"tostartofinterval": {family: semanticFamilyContextDependent, class: wrapperTransparent, strategy: argsGeneric, gen: &genSpec{spelling: "toStartOfInterval", minArity: 2, maxArity: 2, argSorts: []argSort{argSortValue, argSortConstString}, place: placementScalar, template: "toStartOfInterval(%s, INTERVAL 1 %s)"}, resultMode: resultRuleSpecialRoute, domainMode: argumentDomainSpecialRoute, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"totimezone":        {family: semanticFamilyContextDependent, class: wrapperTransparent, strategy: argsGeneric, gen: &genSpec{spelling: "toTimeZone", minArity: 2, maxArity: 2, argSorts: []argSort{argSortValue, argSortConstString}, place: placementScalar}, resultMode: resultRuleSpecialRoute, domainMode: argumentDomainSpecialRoute, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"now":               {family: semanticFamilyFixedResult, rule: fixedFunctionType("DateTime"), class: wrapperOpaque, strategy: argsIndependent, gen: scalarCall("now", 0), resultMode: resultRuleGeneric, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"now64":             {family: semanticFamilyFixedResult, rule: fixedFunctionType("DateTime64"), class: wrapperOpaque, strategy: argsIndependent, gen: &genSpec{spelling: "now64", minArity: 0, maxArity: 1, argSorts: []argSort{argSortConstInt}, place: placementScalar}, resultMode: resultRuleGeneric, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"today":             {family: semanticFamilyConversion, rule: fixedFunctionType("Date"), class: wrapperOpaque, strategy: argsIndependent, gen: scalarCall("today", 0), resultMode: resultRuleGeneric, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},

	// These conversions read the argument as a number, thus each one
	// refuses an AggregateFunction state (measured: toUInt8(agg) is
	// Code: 43). castArgumentDomain names that fact once; see the
	// domain's comment in argument_domain.go for the full measured
	// grid. hex and toString are the accepting exception, because they
	// read raw bytes and never interpret them, so they keep
	// scalarArgumentDomain and no domain at all, respectively.
	"touint8":   {family: semanticFamilyConversion, rule: fixedFunctionType("UInt8"), class: wrapperTransparent, strategy: argsIndependent, domain: &castArgumentDomain, gen: scalarCall("toUInt8", 1), resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"touint16":  {family: semanticFamilyConversion, rule: fixedFunctionType("UInt16"), class: wrapperTransparent, strategy: argsIndependent, domain: &castArgumentDomain, gen: scalarCall("toUInt16", 1), resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"touint32":  {family: semanticFamilyConversion, rule: fixedFunctionType("UInt32"), class: wrapperTransparent, strategy: argsIndependent, domain: &castArgumentDomain, gen: scalarCall("toUInt32", 1), resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"touint64":  {family: semanticFamilyConversion, rule: fixedFunctionType("UInt64"), class: wrapperTransparent, strategy: argsIndependent, domain: &castArgumentDomain, gen: scalarCall("toUInt64", 1), resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"toint8":    {family: semanticFamilyConversion, rule: fixedFunctionType("Int8"), class: wrapperTransparent, strategy: argsIndependent, domain: &castArgumentDomain, gen: scalarCall("toInt8", 1), resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"toint16":   {family: semanticFamilyConversion, rule: fixedFunctionType("Int16"), class: wrapperTransparent, strategy: argsIndependent, domain: &castArgumentDomain, gen: scalarCall("toInt16", 1), resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"toint32":   {family: semanticFamilyConversion, rule: fixedFunctionType("Int32"), class: wrapperTransparent, strategy: argsIndependent, domain: &castArgumentDomain, gen: scalarCall("toInt32", 1), resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"toint64":   {family: semanticFamilyConversion, rule: fixedFunctionType("Int64"), class: wrapperTransparent, strategy: argsIndependent, domain: &castArgumentDomain, gen: scalarCall("toInt64", 1), resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"tofloat32": {family: semanticFamilyConversion, rule: fixedFunctionType("Float32"), class: wrapperTransparent, strategy: argsIndependent, domain: &castArgumentDomain, gen: scalarCall("toFloat32", 1), resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"tofloat64": {family: semanticFamilyConversion, rule: fixedFunctionType("Float64"), class: wrapperTransparent, strategy: argsIndependent, domain: &castArgumentDomain, gen: scalarCall("toFloat64", 1), resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},

	// argMax and argMin take the comparison key as their SECOND
	// argument. comparableValueArgumentDomain therefore pins domainArgs
	// to index 1 alone: the FIRST argument, the value to output, keeps
	// accepting an AggregateFunction state (measured: argMax(agg, i32)
	// is AggregateFunction(uniq, UInt64), RUNS). See
	// comparableValueArgumentDomain in argument_domain.go for the full
	// measured grid.
	"argmax": {family: semanticFamilyAggregateFirstArgument, rule: firstFunctionArgument, class: wrapperAggregate, strategy: argsFirstOnly, domain: &comparableValueArgumentDomain, domainArgs: []int{1}, gen: aggregateCall("argMax", 2), resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"argmin": {family: semanticFamilyAggregateFirstArgument, rule: firstFunctionArgument, class: wrapperAggregate, strategy: argsFirstOnly, domain: &comparableValueArgumentDomain, domainArgs: []int{1}, gen: aggregateCall("argMin", 2), resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	// max and min pick the largest or the smallest of their argument,
	// thus they need the same order comparison "<" and ">" need. An
	// AggregateFunction state has none (measured: max(agg) is Code: 43,
	// "the values of that data type are not comparable"). See
	// comparableValueArgumentDomain for the full measured grid.
	"max": {family: semanticFamilyAggregateFirstArgument, rule: firstFunctionArgument, class: wrapperAggregate, strategy: argsFirstOnly, domain: &comparableValueArgumentDomain, gen: aggregateCall("max", 1), resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"min": {family: semanticFamilyAggregateFirstArgument, rule: firstFunctionArgument, class: wrapperAggregate, strategy: argsFirstOnly, domain: &comparableValueArgumentDomain, gen: aggregateCall("min", 1), resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"sum": {family: semanticFamilyAggregateDedicated, rule: sumFunctionArgument, class: wrapperAggregate, strategy: argsFirstOnly, domain: &sumArgumentDomain, gen: aggregateCall("sum", 1), resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	// The bitwise group aggregates fold N values of a type into one value of
	// that same type, so unlike sum they never widen. Bool is the one
	// exception: it normalizes to its storage type UInt8. See
	// groupBitFunctionArgument for the measured table.
	"groupbitxor": {family: semanticFamilyAggregateDedicated, rule: groupBitFunctionArgument, class: wrapperAggregate, strategy: argsFirstOnly, domain: &groupBitArgumentDomain, gen: aggregateCall("groupBitXor", 1), resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"groupbitand": {family: semanticFamilyAggregateDedicated, rule: groupBitFunctionArgument, class: wrapperAggregate, strategy: argsFirstOnly, domain: &groupBitArgumentDomain, gen: aggregateCall("groupBitAnd", 1), resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"groupbitor":  {family: semanticFamilyAggregateDedicated, rule: groupBitFunctionArgument, class: wrapperAggregate, strategy: argsFirstOnly, domain: &groupBitArgumentDomain, gen: aggregateCall("groupBitOr", 1), resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"any":         {family: semanticFamilyAggregateFirstArgument, rule: firstFunctionArgument, class: wrapperAggregate, strategy: argsFirstOnly, gen: aggregateCall("any", 1), resultMode: resultRuleGeneric, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"anylast":     {family: semanticFamilyAggregateFirstArgument, rule: firstFunctionArgument, class: wrapperAggregate, strategy: argsFirstOnly, gen: aggregateCall("anyLast", 1), resultMode: resultRuleGeneric, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	// The -If condition argument does not change which values maxIf and
	// minIf compare (measured: maxIf(agg, u8=1) is Code: 43, the same
	// "not comparable" message as max(agg)). See
	// comparableValueArgumentDomain.
	"maxif": {family: semanticFamilyAggregateFirstArgument, rule: firstFunctionArgument, class: wrapperAggregate, strategy: argsFirstOnly, domain: &comparableValueArgumentDomain, gen: conditionalAggregateCall("maxIf", 1), resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"minif": {family: semanticFamilyAggregateFirstArgument, rule: firstFunctionArgument, class: wrapperAggregate, strategy: argsFirstOnly, domain: &comparableValueArgumentDomain, gen: conditionalAggregateCall("minIf", 1), resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"sumif": {family: semanticFamilyAggregateDedicated, rule: sumFunctionArgument, class: wrapperAggregate, strategy: argsFirstOnly, domain: &sumArgumentDomain, gen: conditionalAggregateCall("sumIf", 1), resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	// The comparison key stays at index 1 under the -If combinator too
	// (measured: argMaxIf(agg, i32, u8=1) RUNS, argMaxIf(i32, agg,
	// u8=1) is Code: 43).
	"argmaxif": {family: semanticFamilyAggregateFirstArgument, rule: firstFunctionArgument, class: wrapperAggregate, strategy: argsFirstOnly, domain: &comparableValueArgumentDomain, domainArgs: []int{1}, gen: conditionalAggregateCall("argMaxIf", 2), resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"argminif": {family: semanticFamilyAggregateFirstArgument, rule: firstFunctionArgument, class: wrapperAggregate, strategy: argsFirstOnly, domain: &comparableValueArgumentDomain, domainArgs: []int{1}, gen: conditionalAggregateCall("argMinIf", 2), resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	// sumWithOverflow gives its argument's type back UNCHANGED for every
	// integer, float and Decimal, unlike sum, which widens an integer to
	// 64 bits and a Decimal's precision to 38 (measured on ClickHouse
	// 25.8.29.51 over real columns: sumWithOverflow(d2) is
	// Decimal(18, 4), the exact input scale and precision, while
	// sum(d2) is Decimal(38, 4); sumWithOverflow(i32) is Int32,
	// sumWithOverflow(b) is UInt8). It DOES accept an Enum, the same as
	// sum (measured: sumWithOverflow(e8) is Int8, sumWithOverflow(e16)
	// is Int16; sum(e8) and sum(e16) are both Int64), so it shares
	// sum's accept-set, sumArgumentDomain, and not avg's narrower one.
	// See sumWithOverflowFunctionArgument in infer_function.go for the
	// Enum-to-underlying-width case. The regression.
	"sumwithoverflow": {family: semanticFamilyAggregateDedicated, rule: sumWithOverflowFunctionArgument, class: wrapperAggregate, strategy: argsFirstOnly, domain: &sumArgumentDomain, gen: aggregateCall("sumWithOverflow", 1), resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"avg":             {family: semanticFamilyAggregateFixedResult, rule: fixedFunctionType("Float64"), class: wrapperAggregate, strategy: argsIndependent, domain: &avgArgumentDomain, gen: aggregateCall("avg", 1), resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"avgif":           {family: semanticFamilyAggregateFixedResult, rule: fixedFunctionType("Float64"), class: wrapperAggregate, strategy: argsIndependent, domain: &avgArgumentDomain, gen: conditionalAggregateCall("avgIf", 1), resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"quantile":        {family: semanticFamilyAggregateDedicatedParametric, rule: quantileFunctionArgument, class: wrapperAggregate, strategy: argsFirstOnly, domain: &quantileArgumentDomain, gen: aggregateCall("quantile", 1), resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"quantileif":      {family: semanticFamilyAggregateDedicatedParametric, rule: quantileFunctionArgument, class: wrapperAggregate, strategy: argsFirstOnly, domain: &quantileArgumentDomain, gen: conditionalAggregateCall("quantileIf", 1), resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"quantilestate":   {family: semanticFamilyAggregateDedicatedParametric, rule: aggregateStateFunctionArgument, class: wrapperOpaque, strategy: argsFirstOnly, domain: &quantileArgumentDomain, gen: aggregateCall("quantileState", 1), resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"quantilestateif": {family: semanticFamilyAggregateDedicatedParametric, rule: aggregateStateFunctionArgument, class: wrapperOpaque, strategy: argsFirstOnly, domain: &quantileArgumentDomain, gen: conditionalAggregateCall("quantileStateIf", 1), resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"median":          {family: semanticFamilyAggregateDedicatedParametric, rule: quantileFunctionArgument, class: wrapperAggregate, strategy: argsFirstOnly, domain: &quantileArgumentDomain, gen: aggregateCall("median", 1), resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"medianif":        {family: semanticFamilyAggregateDedicatedParametric, rule: quantileFunctionArgument, class: wrapperAggregate, strategy: argsFirstOnly, domain: &quantileArgumentDomain, gen: conditionalAggregateCall("medianIf", 1), resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},

	"assumenotnull": {family: semanticFamilyDedicated, rule: withoutNullableFunctionArgument, class: wrapperOpaque, strategy: argsGeneric, gen: scalarCall("assumeNotNull", 1), resultMode: resultRuleGeneric, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	// nullIf(a, b) is "a = b ? NULL : a", thus it compares its two
	// arguments against each other, and comparesArgPair marks that
	// fact. See checkComparableOperandExprs's call site in
	// inferFunctionType for the measurement of the refusal.
	"nullif": {family: semanticFamilyDedicated, rule: nullIfFunctionResult, class: wrapperTransparent, strategy: argsGeneric, domain: &scalarArgumentDomain, gen: scalarCall("nullIf", 2), measuredFacts: measuredFacts(functionFactComparesArgPair), resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	// greatest and least are scalar, although their class is
	// wrapperAggregate (measured: greatest(i32) is Int32 with no GROUP
	// BY). They are variadic.
	//
	// The strategy is argsGeneric, because the result is the COMMON
	// type of EVERY argument. The strategy was argsFirstOnly, which
	// gave the rule the first argument only and made the answer depend
	// on the argument order. See greatestLeastFunctionType for the
	// measurement table.
	//
	// greatestLeastFunctionType is a FACTORY, not a functionTypeRule
	// itself: it needs its own registry key's name to reach
	// greatestLeastCommonCHTypes's signed/UInt64 exception (a
	// functionTypeRule value alone cannot see which name looked it up).
	// Each entry below calls the factory with its own name.
	"greatest": {family: semanticFamilyCommonSupertype, rule: greatestLeastFunctionType("greatest"), class: wrapperAggregate, strategy: argsGeneric, gen: &genSpec{spelling: "greatest", minArity: 1, maxArity: -1, argSorts: []argSort{argSortValue}, place: placementScalar}, parameterPolicy: parameterResultLattice, resultMode: resultRuleGeneric, domainMode: argumentDomainUnrestricted, evidence: registryMeasurementEvidence},
	"least":    {family: semanticFamilyCommonSupertype, rule: greatestLeastFunctionType("least"), class: wrapperAggregate, strategy: argsGeneric, gen: &genSpec{spelling: "least", minArity: 1, maxArity: -1, argSorts: []argSort{argSortValue}, place: placementScalar}, parameterPolicy: parameterResultLattice, resultMode: resultRuleGeneric, domainMode: argumentDomainUnrestricted, evidence: registryMeasurementEvidence},

	// and, or and xor take the FUNCTION spelling of the logic operators.
	// The rule field stays nil, exactly as tostartofinterval and
	// totimezone above: inferFunctionType routes all three names to
	// inferLogicOperatorFunctionType before it ever reaches the registry
	// lookup, because the result depends on the argument EXPRESSIONS and
	// not only on their types (a Bool argument makes the result Bool, and
	// the two rules that decide that differ between and/or and xor; see
	// inferLogicOperatorFunctionType).
	//
	// The entry still needs a strategy, because TestEverySpecDeclaresAStrategy
	// checks every entry regardless of its rule, and it still needs a gen
	// recipe and a domain, because gridFunctionProbes and the domain
	// lookup both read the registry directly and never call the rule.
	// Before this entry existed, "and", "or" and "xor" had no
	// functionRegistry key at all, so the function-call form refused
	// (TestLogicOperatorFunctionFormStillRefuses pinned that gap) and the
	// enumerator's function lane never considered any of the three names.
	//
	// logicOperatorArgumentDomain applies to EVERY argument here, not only
	// the first: inferLogicOperatorFunctionType checks each argument
	// itself, because the generic strategy's own domain check in
	// inferFunctionType only reaches argument zero.
	"and": {family: semanticFamilyContextDependent, class: wrapperTransparent, strategy: argsGeneric, domain: &logicOperatorArgumentDomain, gen: &genSpec{spelling: "and", minArity: 2, maxArity: -1, argSorts: []argSort{argSortValue}, place: placementScalar}, resultMode: resultRuleSpecialRoute, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"or":  {family: semanticFamilyContextDependent, class: wrapperTransparent, strategy: argsGeneric, domain: &logicOperatorArgumentDomain, gen: &genSpec{spelling: "or", minArity: 2, maxArity: -1, argSorts: []argSort{argSortValue}, place: placementScalar}, resultMode: resultRuleSpecialRoute, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"xor": {family: semanticFamilyContextDependent, class: wrapperTransparent, strategy: argsGeneric, domain: &xorArgumentDomain, gen: &genSpec{spelling: "xor", minArity: 2, maxArity: -1, argSorts: []argSort{argSortValue}, place: placementScalar}, measuredFacts: measuredFacts(functionFactDynamicForcesNullable), resultMode: resultRuleSpecialRoute, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},

	"array": {family: semanticFamilyParametric, rule: arrayFunctionResult, class: wrapperOpaque, strategy: argsGeneric, gen: &genSpec{spelling: "array", minArity: 1, maxArity: -1, argSorts: []argSort{argSortValue}, place: placementScalar}, parameterPolicy: parameterResultLattice, resultMode: resultRuleGeneric, domainMode: argumentDomainUnrestricted, evidence: registryMeasurementEvidence},
	"tuple": {family: semanticFamilyParametric, rule: tupleFunctionResult, class: wrapperOpaque, strategy: argsGeneric, gen: &genSpec{spelling: "tuple", minArity: 1, maxArity: -1, argSorts: []argSort{argSortValue}, place: placementScalar}, resultMode: resultRuleGeneric, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	// map takes the key and the value in turn, thus its arity is even.
	// The minimum is 2, because map() alone is Map(Nothing, Nothing) on
	// the server and Nothing has no Go type. See mapFunctionResult.
	"map": {family: semanticFamilyParametric, rule: mapFunctionResult, class: wrapperOpaque, strategy: argsGeneric, gen: &genSpec{spelling: "map", minArity: 2, maxArity: -1, argSorts: []argSort{argSortValue}, place: placementScalar}, parameterPolicy: parameterResultLattice, resultMode: resultRuleGeneric, domainMode: argumentDomainUnrestricted, evidence: registryMeasurementEvidence},
	// tupleElement carries no rule for the same reason: the element
	// selector is a constant that names the element, thus
	// inferFunctionType dispatches tupleElement to
	// inferTupleElementType before the registry lookup.
	// tupleElement selects an element by a constant. The selector is
	// not a value, thus it is argSortTypeName.
	"tupleelement":     {family: semanticFamilyContextDependent, class: wrapperOpaque, strategy: argsGeneric, gen: &genSpec{spelling: "tupleElement", minArity: 2, maxArity: 3, argSorts: []argSort{argSortValue, argSortTypeName, argSortValue}, place: placementScalar}, resultMode: resultRuleSpecialRoute, domainMode: argumentDomainSpecialRoute, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"grouparray":       {family: semanticFamilyAggregateDedicatedParametric, rule: groupArrayFunctionResult, class: wrapperOpaque, strategy: argsGeneric, gen: aggregateCall("groupArray", 1), resultMode: resultRuleGeneric, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"grouparrayif":     {family: semanticFamilyAggregateDedicatedParametric, rule: groupArrayFunctionResult, class: wrapperOpaque, strategy: argsGeneric, gen: conditionalAggregateCall("groupArrayIf", 1), resultMode: resultRuleGeneric, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"groupuniqarray":   {family: semanticFamilyAggregateDedicatedParametric, rule: groupUniqArrayFunctionResult, class: wrapperOpaque, strategy: argsGeneric, gen: aggregateCall("groupUniqArray", 1), resultMode: resultRuleGeneric, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"groupuniqarrayif": {family: semanticFamilyAggregateDedicatedParametric, rule: groupUniqArrayFunctionResult, class: wrapperOpaque, strategy: argsGeneric, gen: conditionalAggregateCall("groupUniqArrayIf", 1), resultMode: resultRuleGeneric, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	// The four array functions below give their argument type back, thus
	// a missing domain made chgen answer the argument type for a scalar
	// argument that the server refuses with Code: 43. The domain is the
	// measured "Array only" set; see arrayArgumentDomain.
	// arrayDistinct does NOT give its argument back unchanged. It
	// removes the top-level Nullable of the ELEMENT, because it removes
	// the duplicate elements and thereby removes the NULL from the data
	// (measured: arrayDistinct([1, NULL, 1]) over a real
	// Array(Nullable(Int32)) column is Array(Int32) and gives [1]). Its
	// three neighbours below keep every element, thus they keep the
	// Nullable and stay on firstFunctionArgument. See
	// arrayDistinctFunctionResult.
	//
	// The four are wrapperAggregate, NOT wrapperOpaque. An opaque class
	// gives the rule the WRAPPED type, and both rules here hand the
	// first argument back, thus a SimpleAggregateFunction marker on the
	// argument survived onto the result. The server reads THROUGH the
	// marker. Measured on ClickHouse 25.8.29.51 with a real column
	// saf = SimpleAggregateFunction(anyLast, Array(Int32)) that held
	// [3,1,1,2], reading the VALUE as well as the type:
	//
	//	arrayDistinct(saf)     Array(Int32)  [3,1,2]
	//	arraySort(saf)         Array(Int32)  [1,1,2,3]
	//	arraySlice(saf, 1, 2)  Array(Int32)  [3,1]
	//	arrayResize(saf, 2)    Array(Int32)  [3,1]
	//
	// The four names agree on the marker, although each changes the
	// value in a different way. Each name was measured on its own.
	//
	// wrapperAggregate is the correct class, and no new rule is needed.
	// Its conditional disposition keeps the marker only over a BARE
	// SCALAR inner type; see aggregateKeepsSimpleAggregateCondition.
	// These functions demand an Array argument, thus the inner type is
	// always an Array and never a bare scalar, so the condition always
	// drops the marker here. A bare-scalar inner cannot reach these
	// names at all: the server answers Code 43 for arrayDistinct(safs)
	// and for its three neighbours, where safs is
	// SimpleAggregateFunction(anyLast, Int32).
	//
	// The class also gives the measured LowCardinality behaviour. Its
	// stripNested field removes LowCardinality below the top level,
	// which is what the server does (measured: all four give
	// Array(String) for an Array(LowCardinality(String)) column).
	"arraydistinct":     {family: semanticFamilyDedicated, rule: arrayDistinctFunctionResult, class: wrapperAggregate, strategy: argsFirstOnly, domain: &arrayArgumentDomain, gen: scalarCall("arrayDistinct", 1), resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"arraysort":         {family: semanticFamilyLambdaFirstArgument, rule: firstFunctionArgument, class: wrapperAggregate, strategy: argsFirstOnly, domain: &arrayArgumentDomain, gen: scalarCall("arraySort", 1), resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"arrayslice":        {family: semanticFamilyFirstArgument, rule: firstFunctionArgument, class: wrapperAggregate, strategy: argsFirstOnly, domain: &arrayArgumentDomain, gen: &genSpec{spelling: "arraySlice", minArity: 2, maxArity: 3, argSorts: []argSort{argSortValue, argSortConstInt, argSortConstInt}, place: placementScalar}, resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"arrayresize":       {family: semanticFamilyParametric, rule: arrayResizeFunctionResult, class: wrapperAggregate, strategy: argsGeneric, domain: &arrayArgumentDomain, gen: &genSpec{spelling: "arrayResize", minArity: 2, maxArity: 3, argSorts: []argSort{argSortValue, argSortConstInt, argSortValue}, place: placementScalar}, resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"arrayelement":      {family: semanticFamilyDedicated, rule: arrayElementFunctionResult, class: wrapperOpaque, strategy: argsGeneric, gen: &genSpec{spelling: "arrayElement", minArity: 2, maxArity: 2, argSorts: []argSort{argSortValue, argSortConstInt}, place: placementScalar}, resultMode: resultRuleGeneric, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"arraystringconcat": {family: semanticFamilyFixedResult, rule: fixedFunctionType("String"), class: wrapperOpaque, strategy: argsIndependent, domain: &arrayArgumentDomain, gen: &genSpec{spelling: "arrayStringConcat", minArity: 1, maxArity: 2, argSorts: []argSort{argSortValue, argSortConstString}, place: placementScalar}, resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	// has takes a Map as well as an Array, thus it does NOT share
	// arrayArgumentDomain (measured: has(mp, 'a') is UInt8, while
	// arrayDistinct(mp) is Code: 43). It is a predicate: has(Array(Bool),
	// true) is UInt8, not Bool (measured on 25.8.29.51).
	"has": {family: semanticFamilyPredicate, rule: fixedFunctionType("UInt8"), class: wrapperOpaque, strategy: argsIndependent, domain: &hasArgumentDomain, gen: scalarCall("has", 2), resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	// length reads a raw byte count. It refuses IPv4, IPv6 and UUID,
	// unlike empty and notEmpty below, thus it carries its own domain
	// and not countableArgumentDomain (measured on 25.8.29.51: SELECT
	// length(ip4) FROM probe is Code: 43, "Cannot apply function length
	// to IPv4 argument").
	"length": {family: semanticFamilyFixedResult, rule: fixedFunctionType("UInt64"), class: wrapperTransparent, strategy: argsIndependent, domain: &byteLengthArgumentDomain, gen: scalarCall("length", 1), resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	// notEmpty is a predicate: notEmpty(Array(UInt8)) is UInt8, and
	// this holds for Array(Bool) input too (measured on 25.8.29.51).
	"notempty":  {family: semanticFamilyPredicate, rule: fixedFunctionType("UInt8"), class: wrapperTransparent, strategy: argsIndependent, domain: &countableArgumentDomain, gen: scalarCall("notEmpty", 1), resultMode: resultRuleGeneric, domainMode: argumentDomainRestricted, domainArgs: []int{0}, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"mapkeys":   {family: semanticFamilyParametric, rule: mapKeysFunctionResult, class: wrapperOpaque, strategy: argsGeneric, gen: scalarCall("mapKeys", 1), resultMode: resultRuleGeneric, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"mapvalues": {family: semanticFamilyParametric, rule: mapValuesFunctionResult, class: wrapperOpaque, strategy: argsGeneric, gen: scalarCall("mapValues", 1), resultMode: resultRuleGeneric, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},

	// Window-only functions. Measured on ClickHouse 25.8.29.51 over
	// "<f> OVER (ORDER BY i32)" with real columns.
	//
	// The rank family counts rows, so the result is UInt64 whatever the
	// partition and order expressions are:
	//   row_number() OVER (...)  -> UInt64
	//   rank() OVER (...)        -> UInt64
	//   dense_rank() OVER (...)  -> UInt64
	"row_number":   {family: semanticFamilyWindowFixedResult, rule: fixedFunctionType("UInt64"), class: wrapperOpaque, strategy: argsIndependent, gen: windowCall("row_number", 0), resultMode: resultRuleGeneric, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"rank":         {family: semanticFamilyWindowFixedResult, rule: fixedFunctionType("UInt64"), class: wrapperOpaque, strategy: argsIndependent, gen: windowCall("rank", 0), resultMode: resultRuleGeneric, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"dense_rank":   {family: semanticFamilyWindowFixedResult, rule: fixedFunctionType("UInt64"), class: wrapperOpaque, strategy: argsIndependent, gen: windowCall("dense_rank", 0), resultMode: resultRuleGeneric, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"denserank":    {family: semanticFamilyWindowFixedResult, rule: fixedFunctionType("UInt64"), class: wrapperOpaque, strategy: argsIndependent, gen: &genSpec{spelling: "denseRank", minArity: 0, maxArity: -1, argSorts: []argSort{argSortValue}, place: placementWindow}, resultMode: resultRuleGeneric, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"percent_rank": {family: semanticFamilyWindowFixedResult, rule: fixedFunctionType("Float64"), class: wrapperOpaque, strategy: argsIndependent, gen: &genSpec{spelling: "percent_rank", minArity: 0, maxArity: -1, argSorts: []argSort{argSortValue}, place: placementWindow}, resultMode: resultRuleGeneric, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"percentrank":  {family: semanticFamilyWindowFixedResult, rule: fixedFunctionType("Float64"), class: wrapperOpaque, strategy: argsIndependent, gen: &genSpec{spelling: "percentRank", minArity: 0, maxArity: -1, argSorts: []argSort{argSortValue}, place: placementWindow}, resultMode: resultRuleGeneric, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},

	// The value-frame family returns a value of the first argument type:
	//   first_value(i32)     -> Int32       first_value(ns)  -> Nullable(String)
	//   last_value(dt64)     -> DateTime64(3)
	//   lagInFrame(ni32, 1)  -> Nullable(Int32)
	//   leadInFrame(s, 1)    -> String
	// LowCardinality is REMOVED, exactly like an aggregate
	// (measured: first_value(lcdt) is DateTime, lagInFrame(lc, 1) is
	// String), hence the wrapperAggregate class below.
	//
	// The optional third argument of lagInFrame and leadInFrame is a
	// default value. It cannot widen the result: ClickHouse rejects any
	// default whose supertype with the first argument differs from the
	// first argument type (measured: lagInFrame(i32, 1, NULL) and
	// lagInFrame(i32, 1, 3000000000) are both BAD_ARGUMENTS). The first
	// argument is therefore the whole rule.
	"first_value": {family: semanticFamilyWindowFirstArgument, rule: firstFunctionArgument, class: wrapperAggregate, strategy: argsFirstOnly, gen: windowCall("first_value", 1), resultMode: resultRuleGeneric, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"last_value":  {family: semanticFamilyWindowFirstArgument, rule: firstFunctionArgument, class: wrapperAggregate, strategy: argsFirstOnly, gen: windowCall("last_value", 1), resultMode: resultRuleGeneric, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	// The second argument of lagInFrame is the offset, an integer
	// constant. The optional third argument is a default value.
	"laginframe":                {family: semanticFamilyWindowFirstArgument, rule: firstFunctionArgument, class: wrapperAggregate, strategy: argsFirstOnly, gen: &genSpec{spelling: "lagInFrame", minArity: 2, maxArity: 3, argSorts: []argSort{argSortValue, argSortConstInt, argSortValue}, place: placementWindow}, resultMode: resultRuleGeneric, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"leadinframe":               {family: semanticFamilyWindowFirstArgument, rule: firstFunctionArgument, class: wrapperAggregate, strategy: argsFirstOnly, gen: &genSpec{spelling: "leadInFrame", minArity: 2, maxArity: 3, argSorts: []argSort{argSortValue, argSortConstInt, argSortValue}, place: placementWindow}, resultMode: resultRuleGeneric, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"lag":                       {family: semanticFamilyWindowFirstArgument, rule: firstFunctionArgument, class: wrapperAggregate, strategy: argsFirstOnly, gen: &genSpec{spelling: "lag", minArity: 1, maxArity: 3, argSorts: []argSort{argSortValue, argSortIntegerOffset, argSortValue}, place: placementWindow}, resultMode: resultRuleGeneric, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"lead":                      {family: semanticFamilyWindowFirstArgument, rule: firstFunctionArgument, class: wrapperAggregate, strategy: argsFirstOnly, gen: &genSpec{spelling: "lead", minArity: 1, maxArity: 3, argSorts: []argSort{argSortValue, argSortIntegerOffset, argSortValue}, place: placementWindow}, resultMode: resultRuleGeneric, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"nth_value":                 {family: semanticFamilyWindowFirstArgument, rule: firstFunctionArgument, class: wrapperAggregate, strategy: argsFirstOnly, gen: &genSpec{spelling: "nth_value", minArity: 2, maxArity: 2, argSorts: []argSort{argSortValue, argSortIntegerOffset}, place: placementWindow}, resultMode: resultRuleGeneric, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"ntile":                     {family: semanticFamilyWindowFixedResult, rule: fixedFunctionType("UInt64"), class: wrapperOpaque, strategy: argsIndependent, gen: &genSpec{spelling: "ntile", minArity: 1, maxArity: 1, argSorts: []argSort{argSortConstInt}, place: placementWindow}, resultMode: resultRuleGeneric, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"first_value_respect_nulls": {family: semanticFamilyWindowFirstArgument, rule: firstFunctionArgument, class: wrapperAggregate, strategy: argsFirstOnly, gen: windowCall("first_value_respect_nulls", 1), resultMode: resultRuleGeneric, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"firstvaluerespectnulls":    {family: semanticFamilyWindowFirstArgument, rule: firstFunctionArgument, class: wrapperAggregate, strategy: argsFirstOnly, gen: windowCall("firstValueRespectNulls", 1), resultMode: resultRuleGeneric, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"last_value_respect_nulls":  {family: semanticFamilyWindowFirstArgument, rule: firstFunctionArgument, class: wrapperAggregate, strategy: argsFirstOnly, gen: windowCall("last_value_respect_nulls", 1), resultMode: resultRuleGeneric, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"lastvaluerespectnulls":     {family: semanticFamilyWindowFirstArgument, rule: firstFunctionArgument, class: wrapperAggregate, strategy: argsFirstOnly, gen: windowCall("lastValueRespectNulls", 1), resultMode: resultRuleGeneric, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},

	// isNull, isNotNull and the six comparison functions are
	// predicates. Each answers plain UInt8 even over a Bool operand
	// (measured on 25.8.29.51: isNull(Nullable(Bool)) is UInt8,
	// isNotNull(Nullable(Bool)) is UInt8, equals(Bool, Bool) is
	// UInt8, less(Bool, Bool) is UInt8). A wrapper still applies on
	// top: Nullable(Bool) = Nullable(Bool) is Nullable(UInt8).
	//
	// The six comparison functions below are the FUNCTION spelling of
	// the "=", "!=", "<", "<=", ">" and ">=" operators, and they
	// compare their two arguments against each other exactly as the
	// operators do. comparesArgPair marks all six, because a fixed
	// UInt8 result does not say that the pair is legal. Measured on
	// ClickHouse 25.8.29.51 with real columns and a value select
	// (never a bare literal, because the server folds constants):
	//
	//	equals(dec, s)           Code: 43   No operation equals
	//	                                    between Decimal(18, 4)
	//	                                    and String
	//	notEquals(dec, s)        Code: 43   (same shape, notEquals)
	//	less(dec, s)             Code: 43   (same shape, less)
	//	lessOrEquals(dec, s)     Code: 43   (same shape, lessOrEquals)
	//	greater(uid, ip4)        Code: 43   Illegal types of arguments
	//	                                    (UUID, IPv4) of function
	//	                                    greater
	//	greaterOrEquals(uid, ip4) Code: 43  (same shape, greaterOrEquals)
	//
	// Before this field existed, the function spelling of these six
	// names reached only the fixed UInt8 result and never the
	// comparability check, because the check lived behind the
	// operator-string switch in infer_expr.go and behind a single
	// "nullif" name literal in infer_function.go. A call such as
	// equals(dec, s) therefore answered UInt8 although the operator
	// spelling "dec = s" was already refused.
	"isnull":          {family: semanticFamilyPredicate, rule: fixedFunctionType("UInt8"), class: wrapperOpaque, strategy: argsIndependent, gen: scalarCall("isNull", 1), resultMode: resultRuleGeneric, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"isnotnull":       {family: semanticFamilyPredicate, rule: fixedFunctionType("UInt8"), class: wrapperOpaque, strategy: argsIndependent, gen: scalarCall("isNotNull", 1), resultMode: resultRuleGeneric, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"equals":          {family: semanticFamilyPredicate, rule: fixedFunctionType("UInt8"), class: wrapperTransparent, strategy: argsIndependent, gen: scalarCall("equals", 2), measuredFacts: measuredFacts(functionFactComparesArgPair | functionFactDynamicForcesNullable), resultMode: resultRuleGeneric, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"notequals":       {family: semanticFamilyPredicate, rule: fixedFunctionType("UInt8"), class: wrapperTransparent, strategy: argsIndependent, gen: scalarCall("notEquals", 2), measuredFacts: measuredFacts(functionFactComparesArgPair | functionFactDynamicForcesNullable), resultMode: resultRuleGeneric, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"less":            {family: semanticFamilyPredicate, rule: fixedFunctionType("UInt8"), class: wrapperTransparent, strategy: argsIndependent, gen: scalarCall("less", 2), measuredFacts: measuredFacts(functionFactComparesArgPair | functionFactDynamicForcesNullable), resultMode: resultRuleGeneric, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"lessorequals":    {family: semanticFamilyPredicate, rule: fixedFunctionType("UInt8"), class: wrapperTransparent, strategy: argsIndependent, gen: scalarCall("lessOrEquals", 2), measuredFacts: measuredFacts(functionFactComparesArgPair | functionFactDynamicForcesNullable), resultMode: resultRuleGeneric, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"greater":         {family: semanticFamilyPredicate, rule: fixedFunctionType("UInt8"), class: wrapperTransparent, strategy: argsIndependent, gen: scalarCall("greater", 2), measuredFacts: measuredFacts(functionFactComparesArgPair | functionFactDynamicForcesNullable), resultMode: resultRuleGeneric, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	"greaterorequals": {family: semanticFamilyPredicate, rule: fixedFunctionType("UInt8"), class: wrapperTransparent, strategy: argsIndependent, gen: scalarCall("greaterOrEquals", 2), measuredFacts: measuredFacts(functionFactComparesArgPair | functionFactDynamicForcesNullable), resultMode: resultRuleGeneric, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
}

// functionRegistry is the validated production view of the semantic source.
// Tests can replace entries in this map without changing the source.
var functionRegistry = mustBuildFunctionRegistry(addSizedConstructorSemanticSpecs(functionSemanticSpecs))

func mustBuildFunctionRegistry(source map[string]functionSpec) map[string]functionSpec {
	registry := make(map[string]functionSpec, len(source))
	for name, sourceSpec := range source {
		spec, err := validateAndBuildFunctionSpec(name, sourceSpec)
		if err != nil {
			panic(err)
		}
		registry[name] = spec
	}
	return registry
}

func validateAndBuildFunctionSpec(name string, spec functionSpec) (functionSpec, error) {
	if name == "" || name != strings.ToLower(name) {
		return functionSpec{}, fmt.Errorf("function semantic specification has invalid name %q", name)
	}
	if spec.derivedFamilyIdentity != semanticFamilyUnknown ||
		spec.derivedFamilyResult != resultPolicyUnknown || spec.derivedFamilyProbe != probePolicyUnknown {
		return functionSpec{}, fmt.Errorf("function %s semantic source sets derived family policy", name)
	}
	spec = applyFunctionSemanticFamily(spec)
	if spec.class == wrapperClassUnset {
		return functionSpec{}, fmt.Errorf("function %s has no wrapper class", name)
	}
	if spec.strategy == argsUnset {
		return functionSpec{}, fmt.Errorf("function %s has no argument strategy", name)
	}
	if spec.resultMode == resultRuleUnknown {
		return functionSpec{}, fmt.Errorf("function %s has no result rule mode", name)
	}
	if spec.resultMode == resultRuleGeneric && spec.rule == nil {
		return functionSpec{}, fmt.Errorf("function %s uses the generic result route but has no rule", name)
	}
	if spec.resultMode == resultRuleSpecialRoute && spec.rule != nil {
		return functionSpec{}, fmt.Errorf("function %s uses a special result route but also has a generic rule", name)
	}
	if spec.domainMode == argumentDomainUnknown {
		return functionSpec{}, fmt.Errorf("function %s has no argument domain mode", name)
	}
	if spec.domainMode == argumentDomainRestricted && spec.domain == nil {
		return functionSpec{}, fmt.Errorf("function %s has a restricted domain but no domain rule", name)
	}
	if spec.domainMode == argumentDomainRestricted && len(spec.domainArgs) == 0 {
		return functionSpec{}, fmt.Errorf("function %s has a restricted domain but no argument positions", name)
	}
	if spec.domainMode != argumentDomainRestricted && spec.domain != nil {
		return functionSpec{}, fmt.Errorf("function %s has a domain rule that its domain mode does not use", name)
	}
	if spec.domainMode != argumentDomainRestricted && len(spec.domainArgs) != 0 {
		return functionSpec{}, fmt.Errorf("function %s has domain positions that its domain mode does not use", name)
	}
	if spec.parameterPolicy == parameterResultUnknown {
		return functionSpec{}, fmt.Errorf("function %s has no parameter result policy", name)
	}
	if spec.evidence == "" {
		return functionSpec{}, fmt.Errorf("function %s has no measurement evidence", name)
	}
	if spec.gen == nil {
		return functionSpec{}, fmt.Errorf("function %s has no call shape", name)
	}
	if spec.gen.place == placementUnset {
		return functionSpec{}, fmt.Errorf("function %s has no function kind", name)
	}
	if spec.gen.minArity < 0 || (spec.gen.maxArity != -1 && spec.gen.maxArity < spec.gen.minArity) {
		return functionSpec{}, fmt.Errorf("function %s has an invalid arity", name)
	}
	if spec.signature == nil {
		signature := applyFunctionSignatureOverride(name, signatureFromGenSpec(*spec.gen, spec.evidence))
		spec.signature = &signature
	}
	if err := validateFunctionSemanticFamily(name, spec); err != nil {
		return functionSpec{}, err
	}
	if err := validateFunctionSignatureDefinition(name, *spec.signature); err != nil {
		return functionSpec{}, err
	}
	if spec.facts != 0 {
		return functionSpec{}, fmt.Errorf("function %s sets derived fact bits directly", name)
	}
	for _, measured := range spec.measuredFacts {
		if measured.fact == 0 || measured.evidence == "" {
			return functionSpec{}, fmt.Errorf("function %s has an incomplete measured fact", name)
		}
		if spec.facts&measured.fact != 0 {
			return functionSpec{}, fmt.Errorf("function %s repeats a measured fact", name)
		}
		spec.facts |= measured.fact
	}
	return spec, nil
}
