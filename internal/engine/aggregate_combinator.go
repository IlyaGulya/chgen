package engine

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	clickhouse "github.com/AfterShip/clickhouse-sql-parser/parser"
)

// This file types the ClickHouse aggregate function combinators. A
// combinator is a suffix on the name of a base aggregate that changes what
// the aggregate does and, with it, the result type.
//
// Every rule below was measured on ClickHouse 25.8.29.51 (image
// clickhouse/clickhouse-server:25.8, disposable container) with
// DESCRIBE (SELECT <expression> FROM <table>) over real table columns,
// never over literals only, because ClickHouse folds constants and a
// folded constant reports a different LowCardinality wrapper.
//
// The measured rules:
//
//	sumIf(i32, b)                    -> Int64          (same as sum(i32))
//	anyIf(lcn, b)                    -> Nullable(String)
//	sumArray(arr_i)                  -> Int64          (base over the element)
//	sumArray(arr_n)                  -> Nullable(Int64)
//	maxArray(arr_lc)                 -> String
//	sumOrNull(i32)                   -> Nullable(Int64)
//	sumOrDefault(i32)                -> Int64          (unchanged)
//	sumOrDefault(ni32)               -> Nullable(Int64)
//	sumResample(0, 10, 1)(i32, i8)   -> Array(Int64)
//	maxResample(0, 10, 1)(ni32, i8)  -> Array(Int32)   (Nullable DROPS)
//	sumState(i32)                    -> AggregateFunction(sum, Int32)
//	sumState(ni32)                   -> AggregateFunction(sum, Nullable(Int32))
//	uniqState(lc)                    -> AggregateFunction(uniq, String)
//	sumSimpleState(i8)               -> SimpleAggregateFunction(sum, Int64)
//	sumMerge(AggregateFunction(sum, Int32)) -> Int64
//	sumIfMerge(AggregateFunction(sumIf, Int32, UInt8)) -> Int64
//	sumIfMerge(AggregateFunction(sum, Int32))          -> Code 43 (refused)
//	sumMergeState(AggregateFunction(sum, Int32))       -> AggregateFunction(sum, Int32)
//	sumIfMergeState(AggregateFunction(sumIf, Int32, UInt8)) -> AggregateFunction(sumIf, Int32, UInt8)
//
// A combinator whose base aggregate has no type rule stays a refusal. The
// rules below never invent a base result; they call the existing base rule
// through inferAggregateCombinatorBase.
//
// -IfMerge and -IfMergeState are the CHAINED forms sum + If + Merge and
// sum + If + Merge + State. -Merge (and -MergeState) name-check against the
// FULL name that built the state, thus sumIfMerge must check for a state
// named "sumIf" and not "sum". Measured on ClickHouse 25.8.29.51 over a
// real AggregatingMergeTree table with s AggregateFunction(sum, Int32) and
// si AggregateFunction(sumIf, Int32, UInt8):
//
//	sumIfMerge(si)   Int64    (name matches: sumIf)
//	sumIfMerge(s)    Code 43  (name differs: sum, not sumIf)
//
// Without this check, sumIfMerge answered Int32 for BOTH rows: a silently
// wrong type in both cases, and the worst class (a typed answer where the
// server refuses) for the second.

// aggregateCombinator names one suffix and says how it changes the base
// aggregate call.
type aggregateCombinator struct {
	primitives []aggregatePrimitive
	plan       aggregateArgumentPlan
	// suffix is the lowercase combinator suffix, without a leading dash.
	suffix string
}

type aggregatePrimitive uint8

const (
	aggregatePrimitiveArgMax aggregatePrimitive = iota
	aggregatePrimitiveArgMin
	aggregatePrimitiveArray
	aggregatePrimitiveDistinct
	aggregatePrimitiveForEach
	aggregatePrimitiveIf
	aggregatePrimitiveMap
	aggregatePrimitiveMerge
	aggregatePrimitiveNull
	aggregatePrimitiveOrDefault
	aggregatePrimitiveOrNull
	aggregatePrimitiveResample
	aggregatePrimitiveSimpleState
	aggregatePrimitiveState
)

type aggregatePrimitiveSpec struct {
	primitive  aggregatePrimitive
	spelling   string
	isInternal bool
}

// aggregatePrimitiveCatalog is the complete primitive roster from
// system.aggregate_function_combinators on ClickHouse 25.8.29.51. The parser
// knows every row, but the measured chain list below enables only the current
// supported subset.
var aggregatePrimitiveCatalog = []aggregatePrimitiveSpec{
	{primitive: aggregatePrimitiveSimpleState, spelling: "SimpleState"},
	{primitive: aggregatePrimitiveOrDefault, spelling: "OrDefault"},
	{primitive: aggregatePrimitiveDistinct, spelling: "Distinct"},
	{primitive: aggregatePrimitiveResample, spelling: "Resample"},
	{primitive: aggregatePrimitiveForEach, spelling: "ForEach"},
	{primitive: aggregatePrimitiveArgMax, spelling: "ArgMax"},
	{primitive: aggregatePrimitiveArgMin, spelling: "ArgMin"},
	{primitive: aggregatePrimitiveOrNull, spelling: "OrNull"},
	{primitive: aggregatePrimitiveMerge, spelling: "Merge"},
	{primitive: aggregatePrimitiveState, spelling: "State"},
	{primitive: aggregatePrimitiveArray, spelling: "Array"},
	{primitive: aggregatePrimitiveNull, spelling: "Null", isInternal: true},
	{primitive: aggregatePrimitiveMap, spelling: "Map"},
	{primitive: aggregatePrimitiveIf, spelling: "If"},
}

type aggregateArgumentPlan struct {
	conditionArgument     bool
	arrayDataArguments    bool
	resampleKeyArgument   bool
	stateArgument         bool
	stateHasCondition     bool
	stateStoresCondition  bool
	dropsTopLevelNullable bool
	buildsAggregateState  bool
	returnsAggregateState bool
	simpleStateResult     bool
	orNullResult          bool
}

type aggregateChainParseStatus uint8

const (
	aggregateChainNotFound aggregateChainParseStatus = iota
	aggregateChainUnsupported
	aggregateChainSupported
)

// measuredAggregatePrimitiveChains is the fail-closed accept-list. Each
// suffix is built from server primitives. No fixed chain spelling is stored.
var measuredAggregatePrimitiveChains = [][]aggregatePrimitive{
	{aggregatePrimitiveSimpleState},
	// -StateIf must come before both -State and -If. It is a CHAIN, and
	// neither half can spell it: the -If entry would leave the base
	// `quantilestate`, which is not a combinable aggregate, and the
	// -State entry would leave `quantilestateif`, which is not one
	// either. Without this entry the name reaches no combinator rule at
	// all and falls back to a hand-written registry rule that keeps the
	// wrappers the server removes.
	{aggregatePrimitiveState, aggregatePrimitiveIf},
	// -IfState is the MIRROR chain of -StateIf, with the two suffix
	// letters in the OTHER order, and the two orders type differently.
	// quantileIfState(0.5)(f64, b) KEEPS the trailing condition inside
	// the state (AggregateFunction(quantileIf(0.5), Float64, Bool)),
	// while quantileStateIf(0.5)(f64, b) DROPS it
	// (AggregateFunction(quantile(0.5), Float64)). Measured on
	// ClickHouse 25.8.29.51 over real columns; see the "ifstate" case
	// below for the full measurement table.
	//
	// This entry must come before "state", for the same chain reason as
	// -StateIf above: "quantileif" is not itself a combinable base
	// aggregate, so a two-step split (first "state", then "if") never
	// reaches a rule.
	{aggregatePrimitiveIf, aggregatePrimitiveState},
	// -IfMergeState and -IfMerge are CHAINS, for the same reason as
	// -StateIf above: sumifmergestate and sumifmerge must be read whole,
	// because "sumif" is not itself a combinable base aggregate and a
	// two-step split (first "merge"/"mergestate", then "if") would land
	// on a base that isCombinableBaseAggregate refuses. Both entries
	// must come before "mergestate", "merge" and "if" for that reason.
	{aggregatePrimitiveIf, aggregatePrimitiveMerge, aggregatePrimitiveState},
	{aggregatePrimitiveIf, aggregatePrimitiveMerge},
	// -StateOrNull is the CHAIN State + OrNull. "sumstate" is not itself a
	// combinable base aggregate, so a two-step split (first "ornull", then
	// "state") never reaches a rule; this entry must come before "state"
	// for that reason and before "ornull" so the whole suffix is read as
	// one chain. Measured on ClickHouse 25.8.29.51, read from the DECLARED
	// column type of a materialized AggregatingMergeTree table (never
	// toTypeName, which only analyzes and can lie about execution-time
	// behaviour):
	//
	//	sumStateOrNull(i32)   AggregateFunction(sumOrNull, Int32)
	//	sumStateOrNull(ni32)  AggregateFunction(sumOrNull, Nullable(Int32))
	//
	// The state is tagged with the FULL combinator name "sumOrNull", not
	// with the base "sum". Round-trip check: sumOrNullMerge(state) answers
	// Nullable(Int64), and sumMerge(state) refuses with Code 43 ("state
	// ... corresponds to different aggregate function: sumOrNull instead
	// of sum"), which confirms the tag chgen must record.
	{aggregatePrimitiveState, aggregatePrimitiveOrNull},
	{aggregatePrimitiveMerge, aggregatePrimitiveState},
	{aggregatePrimitiveState},
	{aggregatePrimitiveMerge},
	{aggregatePrimitiveOrNull},
	{aggregatePrimitiveOrDefault},
	// -ResampleIf is the CHAIN Resample + If, with the CONDITION as the
	// LAST call argument, after the resample key. "sumresample" is not a
	// combinable base aggregate, so a two-step split reaches no rule; this
	// entry must come before "resample" and "if" for that reason. Measured
	// on ClickHouse 25.8.29.51 over real table columns:
	//
	//	sumResampleIf(0, 10, 1)(i32, i32, b)   Array(Int64)
	//	sumResampleIf(0, 10, 1)(ni32, i32, b)  Array(Int64)  (Nullable DROPS, same as plain -Resample)
	//
	// The MIRROR order -IfResample is genuinely illegal: the server reads
	// the resample key as the trailing -If condition and refuses with Code
	// 43 ("Illegal type Int32 of last argument for aggregate function with
	// If suffix"). That refusal is correct and this file adds no entry for
	// it; a name-shaped guard would be redundant with the fact that
	// "sumif" is not a combinable base aggregate under a two-step split.
	{aggregatePrimitiveResample, aggregatePrimitiveIf},
	{aggregatePrimitiveResample},
	// -ArrayIf is the CHAIN Array + If: the base aggregate reads the
	// ELEMENT type of the array argument, then the trailing condition
	// applies as usual. "sumarray" is not a combinable base aggregate, so
	// this entry must come before "array" and "if". Measured on ClickHouse
	// 25.8.29.51 over real table columns:
	//
	//	sumArrayIf(arr_i, b)  Int64
	//	sumArrayIf(arr_n, b)  Nullable(Int64)  (Array(Nullable(Int32)) element)
	//
	// The MIRROR order -IfArray is genuinely illegal: the server reads the
	// condition argument itself as the array and refuses with Code 43
	// ("Illegal type Bool of argument for aggregate function with Array
	// suffix. Must be array."). This file adds no entry for it, for the
	// same reason as -IfResample above.
	{aggregatePrimitiveArray, aggregatePrimitiveIf},
	{aggregatePrimitiveArray},
	// -IfOrNull and -IfOrDefault are the CHAIN If + OrNull / If + OrDefault.
	// "sumif" is not a combinable base aggregate, so both entries must come
	// before "ornull", "ordefault" and "if". Measured on ClickHouse
	// 25.8.29.51 over real table columns:
	//
	//	sumIfOrNull(i32, b)     Nullable(Int64)
	//	sumIfOrNull(ni32, b)    Nullable(Int64)
	//	sumIfOrDefault(i32, b)  Int64
	//	sumIfOrDefault(ni32, b) Nullable(Int64)  (matches sumOrDefault(ni32), unchanged by -If)
	//
	// -IfOrDefault gives the ordinary result of the base aggregate over the
	// data argument, exactly as the plain -If, -Array and -OrDefault forms
	// do, so it needs no dedicated switch case; the "default" branch of
	// inferAggregateCombinatorType already computes this. -IfOrNull is
	// DIFFERENT: it forces Nullable even when the data argument is not
	// nullable (i32, not just ni32), which is the OrNull wrapper rule and
	// not the plain -If rule, so it shares its switch case with "ornull".
	{aggregatePrimitiveIf, aggregatePrimitiveOrNull},
	{aggregatePrimitiveIf, aggregatePrimitiveOrDefault},
	{aggregatePrimitiveIf},
}

var aggregateCombinators = buildMeasuredAggregateCombinators()

func buildMeasuredAggregateCombinators() []aggregateCombinator {
	result := make([]aggregateCombinator, 0, len(measuredAggregatePrimitiveChains))
	for _, primitives := range measuredAggregatePrimitiveChains {
		plan := aggregatePlanForPrimitives(primitives)
		result = append(result, aggregateCombinator{
			primitives: primitives,
			plan:       plan,
			suffix:     aggregatePrimitiveChainSuffix(primitives),
		})
	}
	return result
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func aggregatePlanForPrimitives(primitives []aggregatePrimitive) aggregateArgumentPlan {
	plan := aggregateArgumentPlan{}
	mergeIndex := -1
	stateIndex := -1
	ifIndex := -1
	for index, primitive := range primitives {
		switch primitive {
		case aggregatePrimitiveArray:
			plan.arrayDataArguments = true
		case aggregatePrimitiveResample:
			plan.resampleKeyArgument = true
		case aggregatePrimitiveMerge:
			mergeIndex = index
			plan.stateArgument = true
		case aggregatePrimitiveState:
			stateIndex = index
			plan.returnsAggregateState = true
		case aggregatePrimitiveSimpleState:
			plan.simpleStateResult = true
		case aggregatePrimitiveOrNull:
			plan.orNullResult = true
		case aggregatePrimitiveIf:
			ifIndex = index
		}
	}
	plan.conditionArgument = ifIndex >= 0 && mergeIndex < 0
	plan.stateHasCondition = ifIndex >= 0 && mergeIndex >= 0 && ifIndex < mergeIndex
	plan.stateStoresCondition = ifIndex >= 0 && stateIndex >= 0 && mergeIndex < 0 && ifIndex < stateIndex
	plan.dropsTopLevelNullable = stateIndex >= 0 && ifIndex > stateIndex
	plan.buildsAggregateState = stateIndex >= 0 && mergeIndex < 0
	return plan
}

func validateAggregateArgumentPlan(combinator aggregateCombinator) error {
	want := aggregatePlanForPrimitives(combinator.primitives)
	if combinator.plan != want {
		return fmt.Errorf("aggregate combinator chain %s has argument plan %+v, want %+v", combinator.suffix, combinator.plan, want)
	}
	return nil
}

func aggregatePrimitiveChainSuffix(primitives []aggregatePrimitive) string {
	var builder strings.Builder
	for _, primitive := range primitives {
		builder.WriteString(strings.ToLower(aggregatePrimitiveSpelling(primitive)))
	}
	return builder.String()
}

func aggregatePrimitiveSpelling(primitive aggregatePrimitive) string {
	for _, spec := range aggregatePrimitiveCatalog {
		if spec.primitive == primitive {
			return spec.spelling
		}
	}
	return ""
}

func aggregatePrimitiveSpellings() []string {
	result := make([]string, 0, len(aggregatePrimitiveCatalog))
	for _, spec := range aggregatePrimitiveCatalog {
		result = append(result, spec.spelling)
	}
	sort.Strings(result)
	return result
}

func (combinator aggregateCombinator) primitiveSpellings() []string {
	result := make([]string, 0, len(combinator.primitives))
	for _, primitive := range combinator.primitives {
		result = append(result, aggregatePrimitiveSpelling(primitive))
	}
	return result
}

// splitAggregateCombinator splits a lowercase function name into the base
// aggregate name and the combinator. It reports ok only when the base name
// is a known aggregate with a type rule, so an ordinary function whose name
// merely ends in the same letters (for example `if` itself, or `array`) is
// never taken apart.
func splitAggregateCombinator(name string) (string, aggregateCombinator, bool) {
	base, combinator, status := parseAggregateCombinatorChain(name)
	return base, combinator, status == aggregateChainSupported
}

func parseAggregateCombinatorChain(name string) (string, aggregateCombinator, aggregateChainParseStatus) {
	name = strings.ToLower(name)
	bases := append([]string(nil), aggregateBaseNames...)
	sort.Slice(bases, func(i, j int) bool { return len(bases[i]) > len(bases[j]) })
	for _, base := range bases {
		if !strings.HasPrefix(name, base) || len(name) == len(base) {
			continue
		}
		primitives, ok := parseAggregatePrimitiveSuffix(name[len(base):])
		if !ok {
			continue
		}
		suffix := aggregatePrimitiveChainSuffix(primitives)
		for _, combinator := range aggregateCombinators {
			if combinator.suffix == suffix {
				return base, combinator, aggregateChainSupported
			}
		}
		return base, aggregateCombinator{primitives: primitives, suffix: suffix, plan: aggregatePlanForPrimitives(primitives)}, aggregateChainUnsupported
	}
	return "", aggregateCombinator{}, aggregateChainNotFound
}

func parseAggregatePrimitiveSuffix(suffix string) ([]aggregatePrimitive, bool) {
	var result []aggregatePrimitive
	for suffix != "" {
		matched := false
		for _, spec := range aggregatePrimitiveCatalog {
			spelling := strings.ToLower(spec.spelling)
			if !strings.HasPrefix(suffix, spelling) {
				continue
			}
			result = append(result, spec.primitive)
			suffix = suffix[len(spelling):]
			matched = true
			break
		}
		if !matched {
			return nil, false
		}
	}
	return result, len(result) > 0
}

// isCombinableBaseAggregate reports whether the name is a base aggregate
// that this file can build a combinator rule on. The list is exactly the
// aggregates that already have a measured type rule, so a combinator can
// never produce a type that the base aggregate itself could not produce.
var aggregateBaseNames = []string{
	"sum", "min", "max", "any", "anylast", "avg",
	"argmin", "argmax", "count", "uniq", "uniqexact", "uniqcombined",
	"quantile", "median", "grouparray", "groupuniqarray",
	"groupbitand", "groupbitor", "groupbitxor",
}

func isCombinableBaseAggregate(name string) bool {
	for _, base := range aggregateBaseNames {
		if name == base {
			return true
		}
	}
	return false
}

// baseAggregateResultType gives the ordinary result type of a base
// aggregate over the given data argument types, with the wrapper rules of
// the aggregate class applied: LowCardinality is always removed and
// Nullable is kept from the data arguments.
func baseAggregateResultType(base string, function *clickhouse.FunctionExpr, argTypes []CHType) (CHType, error) {
	rule, ok := functionRuleFor(base)
	if !ok {
		return CHType{}, fmt.Errorf("aggregate %s has no registered type rule", base)
	}
	// This path gives the ORDINARY result type of the base aggregate,
	// thus it READS each argument as a value. A
	// SimpleAggregateFunction(f, T) argument is therefore replaced by T
	// first, unless the marker survives the read. The marker survives a
	// BARE SCALAR inner type only. readSimpleAggregateValue holds that
	// measured rule, and this file adds no second copy of it.
	//
	// The read must happen BEFORE the wrapper split, because the
	// wrappers that decide the result live INSIDE the marker. A
	// LowCardinality or a Nullable on the inner type is invisible to
	// splitCHWrappers while the marker is still in place.
	//
	// Measured on ClickHouse 25.8.29.51 over real columns of a real
	// AggregatingMergeTree table with one row, never over literals,
	// because the server folds a constant:
	//
	//	anySimpleState(saf)      SimpleAggregateFunction(any, SimpleAggregateFunction(anyLast, Int32))
	//	anySimpleState(saflc)    SimpleAggregateFunction(any, Int32)
	//	anySimpleState(safn)     SimpleAggregateFunction(any, Nullable(Int32))
	//	anySimpleState(saf_arr)  SimpleAggregateFunction(any, Array(Int32))
	//	anySimpleState(saf_tup)  SimpleAggregateFunction(any, Tuple(Int32, String))
	//	anyIf(saflc, b)          Int32
	//	anyOrNull(safn)          Nullable(Int32)
	//
	// saf keeps the whole marker and saf_arr drops it, although neither
	// inner type carries a wrapper. Thus the rule is on the SHAPE of the
	// inner type and not on its wrappers, and it is not an
	// unconditional look-through.
	//
	// -State is a DIFFERENT class and does NOT come here. It stores the
	// argument type verbatim: anyState(safn) is
	// AggregateFunction(any, SimpleAggregateFunction(anyLast,
	// Nullable(Int32))), which KEEPS the marker that
	// anySimpleState(safn) drops.
	valueTypes := make([]CHType, 0, len(argTypes))
	for _, argType := range argTypes {
		valueTypes = append(valueTypes, readSimpleAggregateValue(argType))
	}
	stripped := make([]CHType, 0, len(valueTypes))
	for _, valueType := range valueTypes {
		bare, _, _ := splitCHWrappers(valueType)
		stripped = append(stripped, bare)
	}
	dataCount := aggregateDataArgCount(base, len(stripped))
	// Only the data arguments contribute a Nullable wrapper. For every
	// base aggregate in isCombinableBaseAggregate the data arguments are
	// the leading ones that aggregateDataArgCount reports.
	nullable := false
	for _, valueType := range valueTypes[:dataCount] {
		_, argNullable, _ := splitCHWrappers(valueType)
		nullable = nullable || argNullable
	}
	if functionStrategyFor(base) == argsIndependent {
		result, err := rule(nil)
		if err != nil {
			return CHType{}, err
		}
		if functionClassFor(base) == wrapperOpaque {
			return result, nil
		}
		return applyCHWrappers(result, nullable, false), nil
	}
	if functionClassFor(base) == wrapperOpaque {
		return rule(stripped)
	}
	result, err := rule(stripped)
	if err != nil {
		return CHType{}, err
	}
	// A combinator keeps the wrapper rules of its base aggregate, thus
	// the result loses every LowCardinality wrapper at every depth, not
	// only the one at the top level. Measured: argMinIf((i32, lc), ni32,
	// i32 > 0) is Tuple(Int32, String).
	// See stripNestedLowCardinality.
	return applyCHWrappers(stripNestedLowCardinality(result), nullable, false), nil
}

// inferAggregateCombinatorType types a call of a base aggregate with a
// combinator suffix. It returns ok false when the name is not such a call,
// so the caller can continue with the ordinary paths.
func inferAggregateCombinatorType(name string, function *clickhouse.FunctionExpr, args []clickhouse.Expr, scope queryScope) (CHType, bool, error) {
	base, combinator, status := parseAggregateCombinatorChain(name)
	if status == aggregateChainUnsupported {
		if _, registered := functionRuleFor(name); registered {
			return CHType{}, false, nil
		}
		return CHType{}, true, fmt.Errorf("function %s has no measured aggregate combinator chain; %s", function.Name.Name, pinTypeHint)
	}
	if status != aggregateChainSupported {
		return CHType{}, false, nil
	}
	// The repository already has hand-written rules for a few -If names.
	// Keep them in charge, so this file cannot change a measured answer
	// that the existing tests pin.
	//
	// The -State family is the ONE exception. `quantilestate` and
	// `quantilestateif` carry a hand-written rule that gives back the
	// argument type verbatim. It therefore keeps a LowCardinality
	// wrapper inside the AggregateFunction where the server has none,
	// and it keeps the top-level Nullable that the -If form removes.
	// Measured on ClickHouse 25.8.29.51 over real columns:
	//
	//	quantileState(lc_i32)        AggregateFunction(quantile, Int32)
	//	quantileState(lcn_i32)       AggregateFunction(quantile, Nullable(Int32))
	//	quantileStateIf(ni32, b)     AggregateFunction(quantile, Int32)
	//	quantileStateIf(lcn_i32, b)  AggregateFunction(quantile, Int32)
	//
	// The -State rule below applies the measured wrapper behaviour, thus
	// the combinator path must own these names. Every other name keeps
	// its hand-written rule.
	stateRuleOwnsName := combinator.plan.buildsAggregateState && !combinator.plan.stateStoresCondition && !combinator.plan.orNullResult
	if _, existing := functionRuleFor(name); existing && !stateRuleOwnsName {
		return CHType{}, false, nil
	}
	if err := validateAggregateCombinatorCall(base, combinator, function, args, scope); err != nil {
		return CHType{}, true, err
	}
	trailingRoleCount := boolInt(combinator.plan.conditionArgument) + boolInt(combinator.plan.resampleKeyArgument)
	if len(args) < trailingRoleCount {
		return CHType{}, true, fmt.Errorf("function %s needs at least %d trailing role arguments", function.Name.Name, trailingRoleCount)
	}
	dataArgs := args[:len(args)-trailingRoleCount]
	if combinator.plan.stateArgument {
		dataArgs = args
	}
	argTypes := make([]CHType, 0, len(dataArgs))
	for _, arg := range dataArgs {
		inferred, err := inferExprType(arg, scope)
		if err != nil {
			return CHType{}, true, fmt.Errorf("function %s argument: %w", function.Name.Name, err)
		}
		if combinator.plan.arrayDataArguments {
			bare, nullable, _ := splitCHWrappers(inferred)
			if !strings.EqualFold(bare.Name, "Array") || len(bare.Params) != 1 {
				return CHType{}, true, fmt.Errorf("function %s expects an Array argument, got %s", function.Name.Name, inferred.String())
			}
			inferred = bare.Params[0]
			if nullable {
				inferred = applyCHWrappers(inferred, true, false)
			}
		}
		argTypes = append(argTypes, inferred)
	}

	// A combinator does not widen the argument domain of its base
	// aggregate. Measured on ClickHouse 25.8.29.51 over real columns:
	// every form that this file types refuses the same argument that the
	// bare aggregate refuses, and with the same Code: 43. For example
	// sum(s), sumState(s), sumOrNull(s), sumOrDefault(s), sumArray(arr_s),
	// sumResample(0, 10, 1)(s, i32) and sumSimpleState(s) all fail, and
	// quantile(0.5)(e8) fails together with quantileState(0.5)(e8).
	// Without this check the combinator gives a type where the server
	// refuses, which is a silently wrong type.
	//
	// -Merge, -MergeState, -IfMerge and -IfMergeState all read a state
	// and not a data value, thus no data domain accepts their argument.
	// The state already carries an argument type that the base
	// aggregate accepted when the state was built. Each of these forms
	// has its OWN check instead, on the aggregate NAME stored in the
	// state; see the "merge" case below.
	if combinator.plan.stateArgument {
		// no domain check; the argument is a state, checked by name below.
	} else {
		if err := checkCombinatorArgumentDomain(base, function, argTypes); err != nil {
			return CHType{}, true, err
		}
	}

	switch {
	case combinator.plan.stateStoresCondition:
		// -IfState builds a state tagged "<base>If" and keeps EVERY
		// argument, including the trailing condition, verbatim, with
		// LowCardinality removed and Nullable kept, exactly as plain
		// -State does for the data arguments. This is the MIRROR of
		// -StateIf, and the two orders type differently: -StateIf
		// drops the condition and the top-level Nullable, -IfState
		// keeps both. Measured on ClickHouse 25.8.29.51 over real
		// columns:
		//
		//	quantileIfState(0.5)(f64, b)   AggregateFunction(quantileIf(0.5), Float64, Bool)
		//	quantileStateIf(0.5)(f64, b)   AggregateFunction(quantile(0.5), Float64)
		//	sumIfState(i32, b)             AggregateFunction(sumIf, Int32, Bool)
		//	sumIfState(ni32, b)            AggregateFunction(sumIf, Nullable(Int32), Bool)
		//	sumIfState(i32, u8)            AggregateFunction(sumIf, Int32, UInt8)
		//	anyIfState(lc, b)              AggregateFunction(anyIf, String, Bool)
		//	anyIfState(lcn, b)             AggregateFunction(anyIf, Nullable(String), Bool)
		//
		// The condition argument (the LAST one) keeps its own type
		// verbatim, with the same wrapper rule as a data argument: the
		// sumIfState(i32, u8) row shows UInt8 surviving unchanged, not
		// canonicalized to Bool.
		condition, err := inferExprType(args[len(args)-1], scope)
		if err != nil {
			return CHType{}, true, fmt.Errorf("function %s argument: %w", function.Name.Name, err)
		}
		allArgs := make([]CHType, 0, len(argTypes)+1)
		allArgs = append(allArgs, argTypes...)
		allArgs = append(allArgs, condition)
		params := make([]CHType, 0, len(allArgs)+1)
		params = append(params, CHType{Name: baseAggregateDisplayName(base) + "If", LiteralParams: aggregateParametricLiterals(function)})
		for _, argType := range allArgs {
			bare, nullable, _ := splitCHWrappers(argType)
			params = append(params, applyCHWrappers(stripNestedLowCardinality(bare), nullable, false))
		}
		return CHType{Name: "AggregateFunction", Params: params}, true, nil
	case combinator.plan.buildsAggregateState && !combinator.plan.orNullResult:
		// -State keeps the data argument types verbatim, with
		// LowCardinality removed and Nullable kept. The state itself
		// has no Go representation; goType refuses it.
		//
		// -StateIf applies the same LowCardinality rule and ALSO
		// removes the top-level Nullable. Measured on ClickHouse
		// 25.8.29.51 over real columns, for quantile, sum, max, any,
		// uniq and groupArray alike:
		//
		//	quantileState(ni32)          AggregateFunction(quantile, Nullable(Int32))
		//	quantileStateIf(ni32, b)     AggregateFunction(quantile, Int32)
		//	quantileStateIf(lcn_i32, b)  AggregateFunction(quantile, Int32)
		//
		// AggregateFunction does accept a Nullable value type: the
		// plain -State row above proves it. Only the -If form removes
		// the wrapper, thus the rule belongs to the combinator and not
		// to the container.
		params := make([]CHType, 0, len(argTypes)+1)
		params = append(params, CHType{Name: baseAggregateDisplayName(base), LiteralParams: aggregateParametricLiterals(function)})
		for _, argType := range argTypes {
			bare, nullable, _ := splitCHWrappers(argType)
			if combinator.plan.dropsTopLevelNullable {
				nullable = false
			}
			// The state drops LowCardinality at every depth, in the
			// same way as the ordinary result. Measured:
			// argMinState((i32, lc), ni32) is
			// AggregateFunction(argMin, Tuple(Int32, String),
			// Nullable(Int32)).
			params = append(params, applyCHWrappers(stripNestedLowCardinality(bare), nullable, false))
		}
		return CHType{Name: "AggregateFunction", Params: params}, true, nil
	case combinator.plan.simpleStateResult:
		// ClickHouse accepts -SimpleState only for the aggregates that
		// have a merge function expressible as a plain value. The
		// server names the supported list in the BAD_ARGUMENTS error
		// of an unsupported one, for example medianSimpleState.
		// Anything outside that list must refuse, not guess.
		if !simpleStateSupportedBases[base] {
			return CHType{}, true, fmt.Errorf("function %s is not a supported SimpleState aggregate; ClickHouse accepts -SimpleState only for any, anyLast, min, max, sum, sumWithOverflow, groupBitAnd, groupBitOr, groupBitXor, sumMap, minMap, maxMap and the *Array forms", function.Name.Name)
		}
		result, err := baseAggregateResultType(base, function, argTypes)
		if err != nil {
			return CHType{}, true, err
		}
		outerNullable := false
		// sumSimpleState places the NULL that a CASE without ELSE
		// introduces outside its marker. A Nullable that comes from a
		// typed value stays inside. The position is therefore not a rule
		// for every Nullable argument.
		//
		// Measured on ClickHouse 25.8.29.51 with real columns, with both
		// toTypeName and execution:
		//
		//	sumSimpleState(CASE WHEN i32 > 0 THEN i32 END)
		//	    Nullable(SimpleAggregateFunction(sum, Int64))
		//	sumSimpleState(CASE WHEN i32 > 0 THEN ni32 END)
		//	    Nullable(SimpleAggregateFunction(sum, Int64))
		//	sumSimpleState(ni32)
		//	    SimpleAggregateFunction(sum, Nullable(Int64))
		//	sumSimpleState(CASE WHEN i32 > 0 THEN ni32 ELSE i32 END)
		//	    SimpleAggregateFunction(sum, Nullable(Int64))
		if base == "sum" && len(dataArgs) == 1 {
			if caseExpr, ok := unwrapConstantOperand(dataArgs[0]).(*clickhouse.CaseExpr); ok && caseExpr.Else == nil {
				bare, nullable, _ := splitCHWrappers(result)
				if nullable && !simpleStateCaseKeepsInheritedNullable(caseExpr, scope) {
					result = bare
					outerNullable = true
				}
			}
		}
		marker := CHType{Name: "SimpleAggregateFunction", Params: []CHType{
			{Name: baseAggregateDisplayName(base), LiteralParams: aggregateParametricLiterals(function)},
			result,
		}}
		return applyCHWrappers(marker, outerNullable, false), true, nil
	case combinator.plan.stateArgument:
		// -Merge reads a state and gives the ordinary result type of the
		// base aggregate over the argument types of that state.
		// -MergeState reads the same state and gives it back verbatim,
		// once the name check passes: it is a re-tag, not a compute.
		// -IfMerge and -IfMergeState are the same two forms, but the
		// name that must match the state is "<base>If" and not "<base>",
		// because the state was built by the -If form of the base
		// aggregate. Measured on ClickHouse 25.8.29.51 over a real
		// AggregatingMergeTree table with s AggregateFunction(sum,
		// Int32) and si AggregateFunction(sumIf, Int32, UInt8):
		//
		//	sumMerge(s)            Int64
		//	sumIfMerge(si)         Int64    (name matches: sumIf)
		//	sumIfMerge(s)          Code 43  (name differs: sum, not sumIf)
		//	sumMergeState(s)       AggregateFunction(sum, Int32)
		//	sumIfMergeState(si)    AggregateFunction(sumIf, Int32, UInt8)
		//
		// Before this case existed, sumIfMerge fell through to a
		// generic "ends in merge" fallback that skipped the name check
		// entirely and always answered Int32: a silently wrong type
		// for the matching row, and a typed answer where the server
		// refuses (the worst class) for the mismatched row.
		wantsIf := combinator.plan.stateHasCondition
		isState := combinator.plan.returnsAggregateState
		if len(argTypes) == 0 {
			return CHType{}, true, fmt.Errorf("function %s has no arguments", function.Name.Name)
		}
		stateType := argTypes[0]
		if !strings.EqualFold(stateType.Name, "AggregateFunction") || len(stateType.Params) < 1 {
			return CHType{}, true, fmt.Errorf("function %s expects an AggregateFunction argument, got %s", function.Name.Name, stateType.String())
		}
		// The FIRST state parameter holds the aggregate NAME that built
		// the state, not a data type. -Merge (and its -MergeState and
		// -If variants) refuse a state built by a different aggregate,
		// with the same Code 43 for every family and regardless of any
		// PARAMETER on either side. Measured on ClickHouse 25.8.29.51
		// over a real AggregatingMergeTree table:
		//
		//	sumMerge(AggregateFunction(sum, Int32))              Int64
		//	sumMerge(AggregateFunction(avg, Int32))               Code 43
		//	avgMerge(AggregateFunction(sum, Int32))               Code 43
		//	uniqMerge(AggregateFunction(sum, Int32))              Code 43
		//	minMerge(AggregateFunction(max, Int32))               Code 43
		//	maxMerge(AggregateFunction(min, Int32))               Code 43
		//	countMerge(AggregateFunction(sum, Int32))             Code 43
		//	groupArrayMerge(AggregateFunction(sum, Int32))        Code 43
		//	quantileMerge(0.9)(AggregateFunction(quantile(0.5), Int32))  Float64  (name matches, parameter differs: LEGAL)
		//	quantileMerge(0.5)(AggregateFunction(sum, Int32))            Code 43  (name differs)
		//	avgMergeState(AggregateFunction(sum, Int32))                 Code 43  (name differs, MergeState form)
		//
		// The rule is on the NAME only, checked once as ONE family
		// rule, not as a name-shaped branch per aggregate. A silently
		// wrong type here is the worst defect class, so this file must
		// refuse whenever the server would refuse.
		stateName := stateType.Params[0].Name
		wantName := baseAggregateDisplayName(base)
		if wantsIf {
			wantName = wantName + "If"
		}
		if !strings.EqualFold(stateName, wantName) {
			return CHType{}, true, fmt.Errorf("function %s expects a state built by %s, got a state built by %s", function.Name.Name, wantName, stateName)
		}
		stateArgs := stateType.Params[1:]
		if wantsIf {
			if len(stateArgs) == 0 {
				return CHType{}, true, fmt.Errorf("function %s reads a state with no -If condition type", function.Name.Name)
			}
			condition := stateArgs[len(stateArgs)-1]
			conditionBase := domainBaseType(condition)
			if conditionBase.normalizedName() != "uint8" && conditionBase.normalizedName() != "bool" {
				return CHType{}, true, fmt.Errorf("function %s reads a state with an invalid -If condition type %s", function.Name.Name, condition.String())
			}
			stateArgs = stateArgs[:len(stateArgs)-1]
		}
		if err := validateBaseAggregateArity(base, function.Name.Name, len(stateArgs)); err != nil {
			return CHType{}, true, fmt.Errorf("function %s reads an invalid state shape: %w", function.Name.Name, err)
		}
		if isState {
			// -MergeState and -IfMergeState give the state back with
			// its VALUE types unchanged, but the aggregate name's own
			// parametric literal (if any) comes from THIS call, not
			// from the state that was read. Measured on ClickHouse
			// 25.8.29.51:
			//
			//	sumMergeState(AggregateFunction(sum, Int32))
			//	    -> AggregateFunction(sum, Int32)
			//	sumIfMergeState(AggregateFunction(sumIf, Int32, UInt8))
			//	    -> AggregateFunction(sumIf, Int32, UInt8)
			//	quantileMergeState(0.9)(AggregateFunction(quantile(0.5), Int32))
			//	    -> AggregateFunction(quantile(0.9), Int32)
			//
			// The last row RE-TAGS the parameter: the state was built
			// with 0.5 and the call passes 0.9, and the server answers
			// 0.9. A plain pass-through of stateType would keep 0.5
			// and mistype this row.
			params := make([]CHType, 0, len(stateType.Params))
			params = append(params, CHType{Name: stateType.Params[0].Name, LiteralParams: aggregateParametricLiterals(function)})
			params = append(params, stateType.Params[1:]...)
			return CHType{Name: "AggregateFunction", Params: params}, true, nil
		}
		result, err := baseAggregateResultType(base, function, stateType.Params[1:])
		if err != nil {
			return CHType{}, true, err
		}
		return result, true, nil
	case combinator.plan.buildsAggregateState && combinator.plan.orNullResult:
		// -StateOrNull builds a state tagged "<base>OrNull", with the data
		// argument types kept verbatim, in the same shape as plain -State.
		// Measured on ClickHouse 25.8.29.51, read from the DECLARED column
		// type of a materialized AggregatingMergeTree table:
		//
		//	sumStateOrNull(i32)   AggregateFunction(sumOrNull, Int32)
		//	sumStateOrNull(ni32)  AggregateFunction(sumOrNull, Nullable(Int32))
		//
		// ClickHouse accepts -StateOrNull only for the aggregates whose
		// state implementation supports the OrNull wrapper. This is NOT
		// the same accept-set as plain -OrNull (which checks the
		// ORDINARY result type with canBeInsideNullable): uniqOrNull(i32)
		// answers Nullable(UInt64) and IS accepted, while
		// uniqStateOrNull(i32) is refused with Code 43, "Nested type
		// AggregateFunction(uniq, Int32) cannot be inside Nullable type".
		// The refusal is thus on the STATE itself, which is a property of
		// the aggregate's C++ implementation and not something this file
		// can derive from any other measured rule; it must be its own
		// fixed accept-list, in the same way as simpleStateSupportedBases.
		// Measured on ClickHouse 25.8.29.51 over every base this file
		// combines:
		//
		//	sumStateOrNull, minStateOrNull, maxStateOrNull,
		//	anyStateOrNull, anyLastStateOrNull, avgStateOrNull,
		//	argMinStateOrNull, argMaxStateOrNull, uniqCombinedStateOrNull,
		//	quantileStateOrNull, medianStateOrNull, groupBitAndStateOrNull,
		//	groupBitOrStateOrNull, groupBitXorStateOrNull    all ACCEPTED
		//
		//	countStateOrNull, uniqStateOrNull, uniqExactStateOrNull,
		//	groupArrayStateOrNull, groupUniqArrayStateOrNull  all Code 43
		if !stateOrNullSupportedBases[base] {
			return CHType{}, true, fmt.Errorf("function %s is not a supported StateOrNull aggregate; ClickHouse refuses -StateOrNull for count, uniq, uniqExact, groupArray and groupUniqArray, because their state cannot go inside Nullable", function.Name.Name)
		}
		params := make([]CHType, 0, len(argTypes)+1)
		params = append(params, CHType{Name: baseAggregateDisplayName(base) + "OrNull", LiteralParams: aggregateParametricLiterals(function)})
		for _, argType := range argTypes {
			bare, nullable, _ := splitCHWrappers(argType)
			params = append(params, applyCHWrappers(stripNestedLowCardinality(bare), nullable, false))
		}
		return CHType{Name: "AggregateFunction", Params: params}, true, nil
	case combinator.plan.orNullResult:
		result, err := baseAggregateResultType(base, function, argTypes)
		if err != nil {
			return CHType{}, true, err
		}
		bare, _, _ := splitCHWrappers(result)
		// -OrNull puts the result inside Nullable, thus it REFUSES an
		// aggregate whose result cannot go inside Nullable. The rule is
		// on the SHAPE of the result, not on the name of the aggregate.
		// Measured on ClickHouse 25.8.29.51 over real columns:
		//
		//	groupArrayOrNull(i32)        Code 43
		//	groupUniqArrayOrNull(s)      Code 43
		//	topKOrNull(2)(s)             Code 43
		//	quantilesOrNull(0.5)(i32)    Code 43
		//	sumMapOrNull(m)              Code 43
		//	sumOrNull(i32)               Nullable(Int64)
		//	maxOrNull(s)                 Nullable(String)
		//	avgOrNull(i32)               Nullable(Float64)
		//	uniqOrNull(s)                Nullable(UInt64)
		//	countOrNull(i32)             Nullable(UInt64)
		//
		// Five refusals and five answers, and the split is exactly
		// canBeInsideNullable. wrapNullable alone cannot carry this
		// rule: it returns the BARE type when the guard fails, thus the
		// answer would be Array(String) where the server refuses, which
		// is a silently wrong type.
		//
		// -IfOrNull is the CHAIN If + OrNull, and it FORCES Nullable even
		// when the data argument itself is not nullable. Measured:
		// sumIfOrNull(i32, b) is Nullable(Int64), not Int64, so it shares
		// this rule and not the plain -If rule in "default" below.
		if !canBeInsideNullable(bare) {
			return CHType{}, true, fmt.Errorf("function %s cannot put %s inside Nullable", function.Name.Name, bare.String())
		}
		return applyCHWrappers(bare, true, false), true, nil
	case combinator.plan.resampleKeyArgument:
		// Measured: -Resample REMOVES the Nullable wrapper that the
		// plain aggregate keeps. max(ni32) is Nullable(Int32) while
		// maxResample(0, 10, 1)(ni32, i8) is Array(Int32).
		//
		// -ResampleIf is the CHAIN Resample + If and keeps the SAME rule:
		// sumResampleIf(0, 10, 1)(ni32, i32, b) is Array(Int64), with the
		// Nullable dropped exactly as the plain form drops it. The
		// trailing condition and key arguments already left argTypes
		// through their plan roles, so this case needs no extra logic.
		result, err := baseAggregateResultType(base, function, argTypes)
		if err != nil {
			return CHType{}, true, err
		}
		bare, _, _ := splitCHWrappers(result)
		return CHType{Name: "Array", Params: []CHType{bare}}, true, nil
	default:
		// -If, -Array and -OrDefault all give the ordinary result type
		// of the base aggregate over the data arguments.
		result, err := baseAggregateResultType(base, function, argTypes)
		if err != nil {
			return CHType{}, true, err
		}
		return result, true, nil
	}
}

// validateAggregateCombinatorCall validates the call shape that a measured
// suffix adds to its base aggregate. The ordinary function signature gate
// cannot validate a dynamic name such as sumArray or sumMerge because those
// names do not have separate registry entries.
func validateAggregateCombinatorCall(base string, combinator aggregateCombinator, function *clickhouse.FunctionExpr, args []clickhouse.Expr, scope queryScope) error {
	displayName := function.Name.Name
	if err := validateAggregateArgumentPlan(combinator); err != nil {
		return fmt.Errorf("function %s: %w", displayName, err)
	}
	baseArgCount := len(args)
	conditionCount := boolInt(combinator.plan.conditionArgument)
	resampleKeyCount := boolInt(combinator.plan.resampleKeyArgument)
	if combinator.plan.stateArgument {
		if len(args) != 1 {
			return fmt.Errorf("function %s accepts exactly one AggregateFunction state argument, but got %d; %s", displayName, len(args), pinTypeHint)
		}
		return validateAggregateCombinatorParameters(base, combinator, function)
	}
	baseArgCount -= conditionCount + resampleKeyCount
	if baseArgCount < 0 {
		return fmt.Errorf("function %s has too few arguments; %s", displayName, pinTypeHint)
	}
	if err := validateBaseAggregateArity(base, displayName, baseArgCount); err != nil {
		return err
	}
	if combinator.plan.arrayDataArguments && baseArgCount == 0 {
		return fmt.Errorf("function %s needs at least one Array data argument; %s", displayName, pinTypeHint)
	}
	if conditionCount == 1 {
		if len(args) == 0 {
			return fmt.Errorf("function %s has no -If condition argument", displayName)
		}
		if err := checkIfCombinatorCondition(displayName, args[len(args)-1], scope); err != nil {
			return err
		}
	}
	if combinator.plan.arrayDataArguments {
		for index, argument := range args[:baseArgCount] {
			argumentType, err := inferExprType(argument, scope)
			if err != nil {
				return fmt.Errorf("function %s argument %d: %w", displayName, index+1, err)
			}
			bare := domainBaseType(argumentType)
			if bare.normalizedName() != "array" || len(bare.Params) != 1 {
				return fmt.Errorf("function %s argument %d must be an Array, got %s; %s", displayName, index+1, argumentType.String(), pinTypeHint)
			}
		}
	}
	if resampleKeyCount == 1 {
		keyIndex := baseArgCount
		keyType, err := inferExprType(args[keyIndex], scope)
		if err != nil {
			return fmt.Errorf("function %s resample key: %w", displayName, err)
		}
		if !resampleKeyType(domainBaseType(keyType)) {
			return fmt.Errorf("function %s needs a fixed-width integer, Bool, Enum, Date, or DateTime resample key, got %s; %s", displayName, keyType.String(), pinTypeHint)
		}
	}
	return validateAggregateCombinatorParameters(base, combinator, function)
}

func validateBaseAggregateArity(base, displayName string, arity int) error {
	signature, found := functionSignatureFor(base)
	if !found {
		return fmt.Errorf("aggregate %s has no executable base signature; %s", displayName, pinTypeHint)
	}
	if _, accepted := signature.sortsForArity(arity); accepted {
		return nil
	}
	return fmt.Errorf("function %s base aggregate rejects %d data arguments: it %s; %s", displayName, arity, signatureArityText(signature), pinTypeHint)
}

func validateAggregateCombinatorParameters(base string, combinator aggregateCombinator, function *clickhouse.FunctionExpr) error {
	signature, found := functionSignatureFor(base)
	if !found {
		return fmt.Errorf("aggregate %s has no executable base signature; %s", function.Name.Name, pinTypeHint)
	}
	parameters := functionParameterArgs(function)
	baseParameters := parameters
	if combinator.plan.resampleKeyArgument {
		if len(parameters) < 3 {
			return fmt.Errorf("function %s needs three trailing Resample parameters; %s", function.Name.Name, pinTypeHint)
		}
		baseParameters = parameters[:len(parameters)-3]
		resampleParameters := parameters[len(parameters)-3:]
		values := make([]int64, 3)
		for index, parameter := range resampleParameters {
			value, ok := signedIntegerConstant(parameter)
			if !ok {
				return fmt.Errorf("function %s Resample parameter %d must be a signed 64-bit integer constant; %s", function.Name.Name, index+1, pinTypeHint)
			}
			values[index] = value
		}
		if values[2] <= 0 {
			return fmt.Errorf("function %s Resample step must be positive; %s", function.Name.Name, pinTypeHint)
		}
		if values[1] > values[0] {
			distance := uint64(values[1]) - uint64(values[0])
			step := uint64(values[2])
			buckets := distance / step
			if distance%step != 0 {
				buckets++
			}
			// ClickHouse 25.8.29.51 accepts 1,048,576 buckets and refuses
			// 1,048,577 with Code 69 on an executed real-column query.
			if buckets > 1<<20 {
				return fmt.Errorf("function %s Resample range has %d buckets, above the measured limit 1048576; %s", function.Name.Name, buckets, pinTypeHint)
			}
		}
	}
	matched := false
	for _, form := range signature.parameterForms {
		sorts, ok := form.sortsForArity(len(baseParameters))
		if !ok {
			continue
		}
		matched = true
		for index, sort := range sorts {
			if err := checkSignatureArgumentSort(function.Name.Name+" parameter", index, sort, baseParameters[index]); err != nil {
				return err
			}
		}
		break
	}
	if !matched {
		return fmt.Errorf("function %s base aggregate rejects %d parameters; %s", function.Name.Name, len(baseParameters), pinTypeHint)
	}
	if err := checkSignatureConstantRanges(function.Name.Name, signature, nil, baseParameters); err != nil {
		return err
	}
	return nil
}

func signedIntegerConstant(expression clickhouse.Expr) (int64, bool) {
	expression = unwrapColumnExpr(expression)
	negative := false
	if unary, ok := expression.(*clickhouse.UnaryExpr); ok {
		if strings.TrimSpace(string(unary.Kind)) != "-" {
			return 0, false
		}
		negative = true
		expression = unwrapColumnExpr(unary.Expr)
	}
	literal, ok := expression.(*clickhouse.NumberLiteral)
	if !ok || strings.ContainsAny(literal.Literal, ".eE") {
		return 0, false
	}
	text := strings.TrimSpace(literal.Literal)
	if negative {
		text = "-" + text
	}
	value, err := strconv.ParseInt(text, 10, 64)
	return value, err == nil
}

func resampleKeyType(value CHType) bool {
	switch value.normalizedName() {
	case "int8", "int16", "int32", "int64", "int",
		"uint8", "uint16", "uint32", "uint64", "bool", "boolean",
		"enum8", "enum16", "date", "datetime":
		return true
	default:
		return false
	}
}

// simpleStateCaseKeepsInheritedNullable reports the narrow case where a
// CASE with no ELSE selects a Nullable branch on every row. In that case,
// the Nullable belongs to the branch value and stays inside the marker.
func simpleStateCaseKeepsInheritedNullable(expression *clickhouse.CaseExpr, scope queryScope) bool {
	if expression.Expr != nil || expression.Else != nil || len(expression.Whens) != 1 {
		return false
	}
	branch := expression.Whens[0]
	if branch.When == nil || branch.Then == nil {
		return false
	}
	truth, constant := constantConditionTruth(branch.When, scope)
	if !constant || !truth {
		return false
	}
	branchType, err := inferExprType(branch.Then, scope)
	if err != nil {
		return false
	}
	_, nullable, _ := splitCHWrappers(branchType)
	return nullable
}

// checkCombinatorArgumentDomain applies the argument domain of the bare
// base aggregate to the data arguments of a combinator call.
//
// It reuses the domain that the registry spec of the base aggregate
// carries, so there is one measured accept-set for the bare form and for
// every combinator form of the same aggregate.
//
// The check runs on the base type, that is after the Nullable and the
// LowCardinality wrappers come off, for the same reason as in the bare
// path: the server decides on the inner type. It covers only the leading
// data arguments that aggregateDataArgCount reports, because a condition
// argument or an ordering key is not a data value.
//
// A base aggregate whose spec has no domain has no constraint here
// either. That keeps this function from inventing a rule that no
// measurement supports.
func checkCombinatorArgumentDomain(base string, function *clickhouse.FunctionExpr, argTypes []CHType) error {
	domain, constrained := argumentDomainFor(base)
	if !constrained || len(argTypes) == 0 {
		return nil
	}
	displayName := base
	if function != nil {
		displayName = function.Name.Name
	}
	for _, argType := range argTypes[:aggregateDataArgCount(base, len(argTypes))] {
		bare, _, _ := splitCHWrappers(argType)
		if err := checkArgumentDomain(displayName, domain, bare); err != nil {
			return err
		}
	}
	return nil
}

// baseAggregateDisplayName gives the name that ClickHouse prints inside an
// AggregateFunction type. ClickHouse prints the canonical camel-case name,
// not the spelling of the call.
func baseAggregateDisplayName(base string) string {
	switch base {
	// median is an alias of quantile. ClickHouse canonicalizes it in the
	// state name: medianState(i32) is AggregateFunction(quantile, Int32).
	case "median":
		return "quantile"
	case "anylast":
		return "anyLast"
	case "argmin":
		return "argMin"
	case "argmax":
		return "argMax"
	case "uniqexact":
		return "uniqExact"
	case "uniqcombined":
		return "uniqCombined"
	case "grouparray":
		return "groupArray"
	case "groupuniqarray":
		return "groupUniqArray"
	case "groupbitand":
		return "groupBitAnd"
	case "groupbitor":
		return "groupBitOr"
	case "groupbitxor":
		return "groupBitXor"
	}
	return base
}

// aggregateParametricLiterals gives the parameter literals of a parametric
// aggregate call such as quantileState(0.5)(x). For a plain call such as
// sumState(x) the parser puts the data arguments in the same list, thus the
// parameters are empty unless a separate column argument list is present.
func aggregateParametricLiterals(function *clickhouse.FunctionExpr) []string {
	if function == nil || function.Params == nil || function.Params.ColumnArgList == nil {
		return nil
	}
	return functionParamLiterals(function)
}

// simpleStateSupportedBases lists the base aggregates that ClickHouse
// accepts with the -SimpleState combinator. Measured on 25.8.29.51: an
// unsupported base gives BAD_ARGUMENTS and the message names this list.
// Only the bases that isCombinableBaseAggregate also allows appear here.
var simpleStateSupportedBases = map[string]bool{
	"any": true, "anylast": true, "min": true, "max": true, "sum": true,
	"groupbitand": true, "groupbitor": true, "groupbitxor": true,
}

// stateOrNullSupportedBases lists the base aggregates that ClickHouse
// accepts with the -StateOrNull combinator. Measured on 25.8.29.51: an
// unsupported base gives Code 43, "Nested type AggregateFunction(<base>,
// ...) cannot be inside Nullable type". This is a DIFFERENT accept-set
// from plain -OrNull (canBeInsideNullable, checked on the ORDINARY
// result): uniqOrNull is accepted, uniqStateOrNull is not, because the
// refusal is on whether the aggregate's STATE implementation supports
// the wrapper, not on the shape of its ordinary result. Only the bases
// that isCombinableBaseAggregate also allows appear here.
var stateOrNullSupportedBases = map[string]bool{
	"sum": true, "min": true, "max": true, "any": true, "anylast": true,
	"avg": true, "argmin": true, "argmax": true, "uniqcombined": true,
	"quantile": true, "median": true, "groupbitand": true,
	"groupbitor": true, "groupbitxor": true,
}

// checkIfCombinatorCondition refuses a trailing -If condition argument
// whose base type is not UInt8.
//
// The check belongs here, at the point where the combinator DISCARDS the
// trailing arguments, because before this fix the condition never reached
// any type rule at all. Every combinator entry that carries a condition
// has trailingArgs above zero, thus one check covers every -If form:
// -If, -StateIf, -IfState, -IfOrNull, -IfOrDefault, -ResampleIf and
// -ArrayIf. The -IfMerge and -IfMergeState entries carry no trailing
// argument, thus they never reach this code. A check written for one
// aggregate name would leave the same hole in every other form.
//
// Measured on ClickHouse 25.8.29.51 over the REAL COLUMNS of a table,
// both witnesses in agreement:
//
//	sumIf(u64, u8)   UInt64     sumIf(u64, i32)  Code 43
//	sumIf(u64, b)    UInt64     sumIf(u64, u64)  Code 43
//	sumIf(u64, nu8)  UInt64     sumIf(u64, s)    Code 43
//
// The server message is "Illegal type <T> of last argument for aggregate
// function with If suffix".
//
// The rule is the BASE type and never an implicit conversion: Int32 is
// refused although Int32 converts to UInt8 in other contexts. Bool is an
// alias of UInt8 and passes. The Nullable and LowCardinality wrappers are
// transparent, measured with allow_suspicious_low_cardinality_types:
// LowCardinality(UInt8), LowCardinality(Nullable(UInt8)) and
// Nullable(Bool) all pass.
//
// A placeholder has no result type yet, so it does not make the query
// impossible. Every other inference error is a real refusal and must move to
// the parent. If it is discarded here, an invalid expression inside the
// condition can get a type from the aggregate data argument.
func checkIfCombinatorCondition(functionName string, condition clickhouse.Expr, scope queryScope) error {
	inferred, err := inferExprType(condition, scope)
	if err != nil {
		if errors.Is(err, errPlaceholderResultType) {
			return nil
		}
		return fmt.Errorf("function %s condition: %w", functionName, err)
	}
	base, _, _ := splitCHWrappers(inferred)
	if strings.EqualFold(base.Name, "UInt8") || strings.EqualFold(base.Name, "Bool") {
		return nil
	}
	return fmt.Errorf(
		"function %s takes the last argument as the -If condition, and the server needs a UInt8 there, but the argument is %s; %s",
		functionName, inferred.String(), pinTypeHint,
	)
}

// ifConditionExemptNames lists the names that END in "if" but take no -If
// condition, thus the suffix alone must not select them.
//
//	nullIf(a, b)  is a scalar function and not a combinator at all.
//	*IfMerge and *IfMergeState read a STATE argument that already holds
//	the condition, thus they have no trailing condition of their own.
//	This matches their trailingArgs: 0 entries in aggregateCombinators.
var ifConditionExemptNames = map[string]bool{
	"nullif": true,
}

// checkIfCombinatorConditionArg refuses a call whose name carries the -If
// combinator and whose LAST argument cannot be a condition.
//
// It keys off the NAME and not off a registry entry, because the hole this
// closes is spread over both paths that type an -If call: the 13
// hand-written registry entries use strategy argsFirstOnly and read
// argument zero only, and the combinator path in this file discards the
// trailing arguments. Neither ever looked at the condition. A fix in one
// place would leave the other blind, and a fix for one aggregate name
// would leave the other twelve blind.
//
// A name that ends in "ifmerge" or "ifmergestate" is exempt: the state
// argument already holds the condition and there is no trailing condition
// argument to check.
func checkIfCombinatorConditionArg(name, spelledName string, args []clickhouse.Expr, scope queryScope) error {
	if ifConditionExemptNames[name] {
		return nil
	}
	// countIf has no data argument. Its only argument is the condition, so
	// the general trailing-condition rule must not require two arguments.
	// Measured on ClickHouse 25.8.29.51 over real columns: countIf(UInt8)
	// and countIf(Bool) run and answer UInt64, while countIf(String) and
	// countIf(Int32) refuse with Code 43.
	if name == "countif" {
		if len(args) != 1 {
			return nil
		}
		return checkIfCombinatorCondition(spelledName, args[0], scope)
	}
	if len(args) < 2 {
		return nil
	}
	if !strings.HasSuffix(name, "if") &&
		!strings.HasSuffix(name, "ifstate") &&
		!strings.HasSuffix(name, "ifornull") &&
		!strings.HasSuffix(name, "ifordefault") {
		return nil
	}
	if strings.HasSuffix(name, "ifmerge") || strings.HasSuffix(name, "ifmergestate") {
		return nil
	}
	return checkIfCombinatorCondition(spelledName, args[len(args)-1], scope)
}
