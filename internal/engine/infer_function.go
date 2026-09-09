package engine

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	clickhouse "github.com/AfterShip/clickhouse-sql-parser/parser"
)

func inferFunctionType(function *clickhouse.FunctionExpr, scope queryScope) (CHType, error) {
	return inferFunctionTypeAt(function, scope, false)
}

func inferFunctionTypeAt(function *clickhouse.FunctionExpr, scope queryScope, window bool) (CHType, error) {
	name := strings.ToLower(function.Name.Name)
	args := functionArgs(function)
	if higherOrder, known := higherOrderArrayFunctions[name]; known && function.Name.Name != higherOrder.spelling {
		return CHType{}, fmt.Errorf("function %s does not match the measured case-sensitive spelling %s; %s", function.Name.Name, higherOrder.spelling, pinTypeHint)
	}
	if name != "if" && name != "multiif" && name != "coalesce" && name != "ifnull" {
		if err := checkIfCombinatorConditionArg(name, function.Name.Name, args, scope); err != nil {
			return CHType{}, err
		}
	}
	if err := checkNestedAggregateArgs(name, function.Name.Name, args); err != nil {
		return CHType{}, err
	}
	if err := validateFunctionCallSignature(name, function.Name.Name, function, scope, window); err != nil {
		return CHType{}, err
	}
	switch name {
	case "if", "multiif", "coalesce", "ifnull":
		return inferConditionalFamilyType(name, function.Name.Name, args, scope)
	case "tupleelement":
		return inferTupleElementType(function.Name.Name, args, scope)
	case "tostartofinterval":
		return inferToStartOfIntervalType(function.Name.Name, args, scope)
	case "totimezone":
		return inferToTimeZoneType(function.Name.Name, args, scope)
	case "todatetime", "todatetime64", "tostartofday", "tostartofhour", "tostartofminute", "now", "now64":
		return inferTimezoneCarryingType(name, function, args, scope)
	case "and", "or", "xor":
		return inferLogicOperatorFunctionType(name, function.Name.Name, args, scope)
	}
	if _, isShift := temporalShiftFunctions[name]; isShift {
		return inferTemporalShiftType(name, function.Name.Name, args, scope)
	}
	// A higher-order function whose first argument is a lambda needs the
	// lambda parameter in scope before any argument is typed. The generic
	// paths below would refuse, because the parameter is not a column.
	if higherOrder, known := higherOrderArrayFunctions[name]; known && len(args) > 0 {
		if isLambdaExpr(args[0]) {
			return inferHigherOrderArrayType(name, function.Name.Name, args, scope)
		}
		if higherOrder.allowNoLambda {
			return inferHigherOrderArrayWithoutLambda(name, function.Name.Name, args, scope)
		}
	}
	// The wide and the sized constructors need their argument
	// expressions, not only the argument types, because the size comes
	// from a constant argument. They are handled before the generic
	// rule lookup for that reason.
	if spec, sized := sizedConstructors[name]; sized {
		return inferSizedConstructorType(spec, name, function.Name.Name, args, scope)
	}
	if spec, registered := functionRegistry[name]; registered {
		if spec.resultMode == resultRuleUnknown {
			return CHType{}, fmt.Errorf("function %s has an unknown result rule mode; %s", function.Name.Name, pinTypeHint)
		}
		if spec.domainMode == argumentDomainUnknown {
			return CHType{}, fmt.Errorf("function %s has an unknown argument domain; %s", function.Name.Name, pinTypeHint)
		}
	}

	// An aggregate function with a combinator suffix (-If, -Array,
	// -State, -Merge, -OrNull, -OrDefault, -Resample, -SimpleState) is
	// typed from the rule of its base aggregate. A combinator over a base
	// with no rule stays a refusal.
	// The -If combinator takes its condition as the LAST argument, and
	// NEITHER path that types such a call ever reads that position: the
	// 13 hand-written registry entries use strategy argsFirstOnly and
	// read argument zero only, and inferAggregateCombinatorType discards
	// the trailing arguments. The check therefore sits before both of
	// them, where every function passes, and it keys off the name and
	// never off one registry entry.
	if err := checkIfCombinatorConditionArg(name, function.Name.Name, args, scope); err != nil {
		return CHType{}, err
	}
	// An aggregate in the argument subtree of another aggregate is always
	// Code: 184 on the server. The check sits here, before every path
	// that could give the call a type, because chgen must refuse at
	// generation time and not let the query fail at run time. See
	// nested_aggregate.go for the measured boundary.
	if err := checkNestedAggregateArgs(name, function.Name.Name, args); err != nil {
		return CHType{}, err
	}
	if result, handled, err := inferAggregateCombinatorType(name, function, args, scope); handled {
		return result, err
	}
	if rule, ok := functionRuleFor(name); ok {
		class := functionClassFor(name)
		if functionStrategyFor(name) == argsIndependent {
			result, err := rule(nil)
			if err != nil {
				return result, err
			}
			// A fixed result type does not make the call legal. The
			// server still refuses an argument outside the measured
			// domain, thus check the argument before the result goes
			// back. Without this check avg(s) and length(e8) get a
			// type although ClickHouse answers Code: 43.
			// A SimpleAggregateFunction(f, T) argument is judged on T
			// here too. The result type is fixed, thus the unwrap
			// changes the CHECK only (measured: avg(sagg) is Float64,
			// as avg(i64) is; avg(saggs) stays refused, as avg(s) is).
			// The domain applies to the argument positions that the
			// spec names, which is argument zero unless the spec says
			// otherwise. dateDiff names arguments one and two, so the
			// String unit at argument zero and the String timezone at
			// argument three never meet the date set.
			if domain, constrained := argumentDomainFor(name); constrained && len(args) > 0 {
				domainErr := checkArgumentDomainAt(
					function.Name.Name, domain, domainArgumentIndexes(name), len(args),
					func(index int) (CHType, bool) {
						argumentType, argErr := inferExprType(args[index], scope)
						if argErr != nil {
							return CHType{}, false
						}
						base, _, _ := splitCHWrappers(argumentType)
						if inner, ok := simpleAggregateWrapperInner(base); ok {
							base, _, _ = splitCHWrappers(inner)
						}
						return base, true
					},
				)
				if domainErr != nil {
					return CHType{}, domainErr
				}
			}
			// Every argument must get a type, whatever the
			// wrapper class is. A fixed result type says what
			// the call gives back; it does not say that the
			// argument is legal. An argument that inference
			// cannot type is a refusal, because the server also
			// refuses it: uniq(bogusfn(s)) is Code: 46 on
			// ClickHouse 25.8.29.51, and a UInt64 answer here
			// would hide that. A wrapperOpaque function keeps
			// the bare result, thus it uses the argument types
			// for the check only.
			//
			// The independent rules exist so that an untypeable
			// argument (for example a bare placeholder) does not
			// block a fixed result. When an argument cannot be
			// typed, keep the bare result as before.
			argTypes := make([]CHType, 0, len(args))
			for _, arg := range args {
				if isStarArgument(arg) {
					continue
				}
				inferred, argErr := inferExprType(arg, scope)
				if argErr != nil {
					// A placeholder argument keeps the bare
					// fixed result, so countIf(a = ?) works.
					// Any other untypeable argument is a
					// refusal: a bare guess here can drop a
					// Nullable wrapper that ClickHouse keeps.
					if errors.Is(argErr, errPlaceholderResultType) {
						return result, nil
					}
					return CHType{}, fmt.Errorf("function %s argument: %w", function.Name.Name, argErr)
				}
				argTypes = append(argTypes, inferred)
			}
			if err := checkHasElementPair(function.Name.Name, name, args, argTypes, scope); err != nil {
				return CHType{}, err
			}
			// A function marked comparesArgPair in the registry
			// compares its first two arguments against each other,
			// the same way the "=" family of OPERATORS does. equals,
			// notEquals, less, lessOrEquals, greater and
			// greaterOrEquals are the FUNCTION spelling of those
			// operators, and this is their only call site: they take
			// the argsIndependent strategy, thus their fixed UInt8
			// result never reaches nullIf's argsGeneric call site
			// above. Without this check chgen answers UInt8 for a
			// call that the server refuses, for example
			// equals(dec, s): Code: 43, "No operation equals between
			// Decimal(18, 4) and String". See the registry comment
			// on the six names for the measurement.
			if functionComparesArgPairFor(name) && len(args) == 2 && len(argTypes) == 2 {
				if err := checkComparableOperandExprs(
					"function "+function.Name.Name,
					args[0], args[1], argTypes[0], argTypes[1], scope,
				); err != nil {
					return CHType{}, err
				}
			}
			if class == wrapperOpaque {
				return result, nil
			}
			return applyFunctionWrappers(result, class, name, args, argTypes, scope), nil
		}
		if functionStrategyFor(name) == argsFirstOnly {
			if len(args) == 0 {
				return CHType{}, fmt.Errorf("function %s has no arguments", function.Name.Name)
			}
			first, err := inferExprType(args[0], scope)
			if err != nil {
				return CHType{}, fmt.Errorf("function %s first argument: %w", function.Name.Name, err)
			}
			// The server decides on the base type, thus check the
			// measured domain after the wrappers are removed:
			// sum(ns) and sum(s) both fail for the String, not for
			// the Nullable.
			base, nullable, lowCardinality := splitCHWrappers(first)
			// A SimpleAggregateFunction(f, T) argument of a COMPUTING
			// aggregate acts as T alone, both for the domain check and
			// for the result. An aggregate that gives a data value back
			// KEEPS the wrapper, thus the unwrap must not be global.
			//
			// The registry already separates the two groups, thus this
			// needs no new list. A function with a measured domain
			// computes from the value (sum, avg, groupBit*, quantile),
			// and a function without one gives an argument back (max,
			// min, any, anyLast, argMax). The class wrapperOpaque
			// returns above this point, which is what keeps
			// quantileExact correct.
			//
			// Measured on ClickHouse 25.8.29.51 with real table columns,
			// never over literals, because the server folds constants.
			// Each answer in the first group equals the answer for the
			// inner type alone:
			//
			//	sum(sagg)                  Int64      sum(i64)   Int64
			//	sum(saggu)                 UInt64     sum(u8)    UInt64
			//	sum(saggn)                 Nullable(Int64)
			//	avg(sagg)                  Float64    avg(i64)   Float64
			//	sumIf(sagg, c)             Int64
			//	avgIf(sagg, c)             Float64
			//	groupBitAnd(saggu)         UInt8
			//	quantile(0.5)(sagg)        Float64
			//
			// The second group keeps the wrapper, and chgen answered
			// these correctly before this rule:
			//
			//	max(sagg)                  SimpleAggregateFunction(sum, Int64)
			//	min(sagg)                  SimpleAggregateFunction(sum, Int64)
			//	any(sagg)                  SimpleAggregateFunction(sum, Int64)
			//	anyLast(sagg)              SimpleAggregateFunction(sum, Int64)
			//	argMax(sagg, i64)          SimpleAggregateFunction(sum, Int64)
			//	maxIf(sagg, c)             SimpleAggregateFunction(sum, Int64)
			//	quantileExact(0.5)(sagg)   SimpleAggregateFunction(sum, Int64)
			//
			// The unwrap does not widen the refusal boundary. A
			// non-numeric inner type stays refused, and the server
			// refuses it too: sum(saggs) answers Code: 43 ("Illegal type
			// SimpleAggregateFunction(min, String) of argument for
			// aggregate function sum").
			domain, constrained := argumentDomainFor(name)
			if constrained {
				// The inner type can itself be Nullable, thus split the
				// wrappers again: the domain works on the base type, and
				// the Nullable must join the result instead (measured:
				// sum(saggn) is Nullable(Int64), as sum(ni64) is).
				//
				// This unwrap is what makes a COMPUTING aggregate drop
				// the marker: sum(sagg) is Int64. A function whose
				// transport KEEPS the marker must NOT lose it here,
				// because the stack would then never learn the wrapper
				// had been there and the applier could not put it back.
				// Such a function checks the domain against the inner
				// type and leaves base wrapped for splitWrapperStack.
				// See caseFoldingTransport.
				domainArg := base
				// "Has a measured domain" is the proxy for "computes
				// from the value", and lower and upper are the
				// measured exception: they have a string domain AND
				// keep the marker. max, min, argMax, argMin and their
				// -If variants are a SECOND exception of the same
				// shape, added together with comparableValueArgumentDomain:
				// each one now has a domain AND keeps the marker
				// (measured: max(sagg) is still
				// SimpleAggregateFunction(sum, Int64), unchanged by
				// this rule). sum, avg, the bitwise group aggregates
				// and quantile share the SAME class, wrapperAggregate,
				// as max and min, yet they DO compute a new value and
				// must unwrap, thus the class alone cannot answer this
				// question; valuePreservingDomainFunctions names the
				// exception explicitly, the same way
				// wrapperTransportOverrides names the lower/upper
				// exception for the transport question.
				_, keepsMarker := valuePreservingDomainFunctions[name]
				if !keepsMarker {
					_, keepsMarker = wrapperTransportOverrides[name]
				}
				if inner, ok := simpleAggregateWrapperInner(base); ok {
					if keepsMarker {
						domainArg, _, _ = splitCHWrappers(inner)
					} else {
						var innerNullable, innerLowCardinality bool
						base, innerNullable, innerLowCardinality = splitCHWrappers(inner)
						nullable = nullable || innerNullable
						lowCardinality = lowCardinality || innerLowCardinality
						domainArg = base
					}
				}
				// The domain applies to argument zero unless the spec
				// pins it elsewhere. argMax and argMin pin
				// comparableValueArgumentDomain to index 1, the
				// comparison key, and leave index 0, the value to
				// output, unconstrained (measured: argMax(agg, i32)
				// RUNS, agg at index 0). This FIRST-argument check
				// must therefore run only when the spec names index 0
				// as one of its domain positions.
				appliesAtIndexZero := false
				for _, index := range domainArgumentIndexes(name) {
					if index == 0 {
						appliesAtIndexZero = true
						break
					}
				}
				if appliesAtIndexZero {
					if domainErr := checkArgumentDomain(function.Name.Name, domain, domainArg); domainErr != nil {
						return CHType{}, domainErr
					}
				}
			}
			if class == wrapperOpaque {
				opaque, opaqueErr := rule([]CHType{first})
				if opaqueErr != nil {
					return CHType{}, opaqueErr
				}
				return applyParameterVerdict(name, opaque)
			}
			// The rule must see a BARE base type. Split off any
			// SimpleAggregateFunction wrapper that survived the domain
			// step above and record it in the stack, so the ONE applier
			// decides whether the result carries it back. Before this,
			// the wrapper reached the rule and six separate sites
			// removed it by hand.
			//
			// A value-preserving aggregate (max, min, any, anyLast,
			// argMax) keeps the wrapper, and the wrapperAggregate
			// transport says so. greatest and least drop it under the
			// measured condition in their own transport. Neither
			// decision lives at this site any more.
			var argumentStack wrapperStack
			base, argumentStack = splitWrapperStack(base)
			argumentStack.lowCardinality = argumentStack.lowCardinality || lowCardinality
			if nullable {
				// This Nullable was found outside the
				// SimpleAggregateFunction wrapper, thus it goes back
				// outside it.
				argumentStack.nullable = true
				argumentStack.outerNullable = true
			}
			result, err := rule([]CHType{base})
			if err != nil {
				return CHType{}, err
			}
			// The parameter verdict gate, the same one as on the
			// generic path below. max, min, any, anyLast, argMax and
			// their neighbours reach their rule HERE, thus the gate
			// must stand on this path as well: a rule that gives its
			// argument back is exactly the rule that can copy a
			// parameter through without an opinion.
			// See parameter_verdict.go.
			result, err = applyParameterVerdict(name, result)
			if err != nil {
				return CHType{}, err
			}
			// A later data argument also contributes its Nullable
			// wrapper (for example the ordering argument of
			// argMax). An argument that inference cannot type must
			// refuse: a skip here keeps the bare result and can
			// drop a Nullable that ClickHouse keeps. Only a bare
			// positional placeholder is absorbed, because it has no
			// result type by construction.
			// A domain pinned to one of these LATER positions (for
			// example argMax and argMin pin comparableValueArgumentDomain
			// to index 1, the comparison key) must be checked here too,
			// because this argsFirstOnly path never reaches the
			// argsIndependent domain check above: the regression measured
			// argMax(i32, agg) as Code: 43, "the values of that data
			// type are not comparable", and the FIRST argument alone
			// cannot see that, since agg sits at index 1.
			extraDomainIndexes := map[int]bool{}
			if constrained {
				for _, index := range domainArgumentIndexes(name) {
					if index > 0 {
						extraDomainIndexes[index] = true
					}
				}
			}
			for offset, extra := range args[1:aggregateDataArgCount(name, len(args))] {
				index := offset + 1
				extraType, extraErr := inferExprType(extra, scope)
				if extraErr != nil {
					if errors.Is(extraErr, errPlaceholderResultType) {
						continue
					}
					return CHType{}, fmt.Errorf("function %s argument: %w", function.Name.Name, extraErr)
				}
				if extraDomainIndexes[index] {
					extraBase, _, _ := splitCHWrappers(extraType)
					if inner, ok := simpleAggregateWrapperInner(extraBase); ok {
						extraBase, _, _ = splitCHWrappers(inner)
					}
					if domainErr := checkArgumentDomain(function.Name.Name, domain, extraBase); domainErr != nil {
						return CHType{}, domainErr
					}
				}
				_, extraNullable, _ := splitCHWrappers(extraType)
				if extraNullable {
					// This Nullable comes from a LATER argument, not
					// from the inner type of a SimpleAggregateFunction,
					// thus it belongs OUTSIDE any such wrapper.
					argumentStack.nullable = true
					argumentStack.outerNullable = true
				}
			}
			// One applier decides which wrappers the result carries.
			// The transport of the function holds the measured rules,
			// including the asymmetric greatest and least grid. This
			// site holds no name-based special case and no hand removal
			// of a wrapper. See wrapper_transport.go.
			//
			// The argsFirstOnly path reads the first argument only,
			// thus a transparent function has no other argument that
			// could break the "all others constant" requirement of the
			// LowCardinality rule. greatest and least keep their own
			// arity condition and are not resolved here.
			transport := transportForFunction(name, class)
			if _, special := wrapperTransportOverrides[name]; !special {
				transport = transport.withResolvedLowCardinality(argumentStack.lowCardinality)
			}
			return applyWrapperTransport(
				result,
				transport,
				wrapperCall{
					base:     base,
					stacks:   []wrapperStack{argumentStack},
					argCount: len(args),
				},
			), nil
		}
	}
	argTypes := make([]CHType, 0, len(args))
	for _, arg := range args {
		inferred, err := inferExprType(arg, scope)
		if err != nil {
			return CHType{}, fmt.Errorf("function %s argument: %w", function.Name.Name, err)
		}
		argTypes = append(argTypes, inferred)
	}
	if rule, ok := functionRuleFor(name); ok {
		class := functionClassFor(name)
		// A rule must see BARE base types, thus remove the transport
		// stack. The ONE applier below puts back the wrappers that the
		// transport of this function keeps.
		//
		// The SimpleAggregateFunction wrapper is the exception on THIS
		// path, and the exception is measured, not historical. A rule
		// here can be VALUE-PRESERVING: it gives its argument back
		// rather than computing a new value, and the server then keeps
		// the wrapper INSIDE the Nullable that the rule itself adds.
		// Measured on ClickHouse 25.8.29.51 with real columns:
		//
		//	nullIf(sagg, i64)    Nullable(SimpleAggregateFunction(sum, Int64))
		//	nullIf(saggs, s)     Nullable(SimpleAggregateFunction(min, String))
		//
		// The applier cannot rebuild that nesting, because it can only
		// put a wrapper AROUND the finished result, and the wrapper
		// belongs under the Nullable. Such a rule therefore keeps the
		// wrapper on its argument and owns it, exactly as a
		// wrapperOpaque rule does. wrapperValuePreservingRules is the
		// closed list, and the guard test pins it.
		stripSimpleAggregate := !wrapperValuePreservingRules[name]
		strippedArgs := make([]CHType, 0, len(argTypes))
		for _, argType := range argTypes {
			base, stack := splitWrapperStack(argType)
			if !stripSimpleAggregate && stack.simpleAggregate != nil {
				// Keep the SimpleAggregateFunction wrapper, and remove
				// the outer LowCardinality and Nullable only.
				base, _, _ = splitCHWrappers(argType)
			}
			strippedArgs = append(strippedArgs, base)
		}
		// Refuse an argument that the server refuses, before the rule
		// gives the call a type.
		//
		// A SimpleAggregateFunction(f, T) argument is judged on T, as in
		// the argsFirstOnly path above: a computing aggregate reads the
		// value, thus the wrapper does not reach the domain (measured:
		// avg(sagg) is Float64, as avg(i64) is, and avgIf(sagg, c) is
		// Float64 too). The unwrap is only for the CHECK here. The rule
		// keeps the stripped argument that it had before, thus a
		// function that gives an argument back is not affected.
		if domain, constrained := argumentDomainFor(name); constrained && len(strippedArgs) > 0 {
			domainArg := strippedArgs[0]
			if inner, ok := simpleAggregateWrapperInner(domainArg); ok {
				domainArg, _, _ = splitCHWrappers(inner)
			}
			if domainErr := checkArgumentDomain(function.Name.Name, domain, domainArg); domainErr != nil {
				return CHType{}, domainErr
			}
		}
		// A function marked comparesArgPair in the registry compares
		// its first two arguments against each other, the same way
		// the "=" family of OPERATORS does. nullIf(a, b) is
		// "a = b ? NULL : a", and its result type comes from the
		// FIRST argument alone, thus without this check chgen answers
		// a type for a call that the server refuses, for example
		// nullIf(dec, s): Code: 43, "No operation equals between
		// Decimal(18, 4) and String".
		//
		// The check lives here, and not in nullIfFunctionResult,
		// because it needs the argument EXPRESSIONS: a constant
		// operand folds into the type of the other side and stays
		// legal. This is the only entry on this path today
		// (comparesArgPair is set on nullif alone here; the other six
		// comparing functions take the argsIndependent strategy and
		// are checked at their own call site below).
		if functionComparesArgPairFor(name) && len(args) == 2 && len(argTypes) == 2 {
			if err := checkComparableOperandExprs(
				"function "+function.Name.Name,
				args[0], args[1], argTypes[0], argTypes[1], scope,
			); err != nil {
				return CHType{}, err
			}
		}
		if class == wrapperOpaque {
			opaque, opaqueErr := rule(argTypes)
			if opaqueErr != nil {
				return CHType{}, opaqueErr
			}
			return applyParameterVerdict(name, opaque)
		}
		result, err := rule(strippedArgs)
		if err != nil {
			return CHType{}, err
		}
		// The parameter verdict gate. A rule that gives a parametric
		// type back must hold a MEASURED verdict for that parameter
		// family, otherwise it refuses. Without this gate a rule with
		// no opinion about a parameter copied the parameter through
		// and answered a type whose base kind was right and whose
		// parameter was wrong. See parameter_verdict.go.
		//
		// The gate runs on the rule result and BEFORE the wrapper
		// applier, because the applier only puts Nullable and
		// LowCardinality around a finished result and never touches a
		// parameter, thus the parameter question is already settled
		// here.
		result, err = applyParameterVerdict(name, result)
		if err != nil {
			return CHType{}, err
		}
		return applyFunctionWrappers(result, class, name, args, argTypes, scope), nil
	}
	if strings.HasSuffix(name, "merge") {
		if len(argTypes) == 0 {
			return CHType{}, fmt.Errorf("merge function %s has no arguments", function.Name.Name)
		}
		argumentType := argTypes[0]
		// This path is reached only when inferAggregateCombinatorType did
		// NOT handle the name, which happens when the part of the name
		// before "merge" is not a base aggregate that this project has a
		// measured rule for (isCombinableBaseAggregate), and not one of
		// the measured CHAINS in aggregate_combinator.go (-IfMerge,
		// -MergeState, -IfMergeState).
		//
		// Answering argumentType.Params[1] here used to guess the base
		// aggregate's result type without checking the aggregate NAME
		// stored in the state. Measured on ClickHouse 25.8.29.51: the
		// server refuses a -Merge call whenever the state's stored name
		// does not match the aggregate that -Merge names (Code 43), and
		// this path cannot run that check, because it does not know
		// which base aggregate name to require. Guessing Params[1] gave
		// a silently wrong type for a call the server accepts (for
		// example sumIfMerge(si), before -IfMerge was measured and
		// added) and, worse, a typed answer for a call the server
		// refuses (for example sumOrNullMerge over a plain sum state:
		// Code 43, "corresponds to different aggregate function: sum
		// instead of sumOrNull").
		//
		// This path must therefore always refuse: an explicit refusal
		// costs the user one annotation, and every legitimate -Merge
		// chain measured so far is covered by aggregate_combinator.go,
		// not by this fallback.
		return CHType{}, fmt.Errorf(
			"function %s ends in merge but its base aggregate has no measured type rule for this argument (%s); %s",
			function.Name.Name, argumentType.String(), pinTypeHint,
		)
	}
	return CHType{}, &unregisteredFunctionError{name: function.Name.Name}
}

// isStarArgument reports whether an argument is the bare star of
// count(*). The parser gives the star as an ordinary identifier named
// "*", thus a column lookup for it always fails. The star names no
// expression and has no type, so the argument walk must step over it.
// ClickHouse accepts count(*) and answers UInt64 (measured on
// ClickHouse 25.8.29.51), thus a refusal here would break a working
// query.
func isStarArgument(arg clickhouse.Expr) bool {
	switch typed := arg.(type) {
	case *clickhouse.Ident:
		return typed.Name == "*"
	case *clickhouse.ColumnExpr:
		return isStarArgument(typed.Expr)
	}
	return false
}

func functionArgs(function *clickhouse.FunctionExpr) []clickhouse.Expr {
	if function.Params == nil || function.Params.Items == nil {
		return nil
	}
	items := function.Params.Items.Items
	// Parametric aggregate functions are represented as
	// quantileMerge(level)(state): the actual data arguments live in the
	// ColumnArgList, not in the parameter list.
	if function.Params.ColumnArgList != nil {
		items = function.Params.ColumnArgList.Items
	}
	args := make([]clickhouse.Expr, 0, len(items))
	for _, item := range items {
		if columnExpr, ok := item.(*clickhouse.ColumnExpr); ok {
			args = append(args, columnExpr.Expr)
		} else {
			args = append(args, item)
		}
	}
	return args
}

// caseFoldingFunctionType types upper and lower. The case-folded result
// has the same length as the input, so a FixedString(N) argument keeps
// its width, while every other string-like argument gives String
// (measured on ClickHouse 25.8.29.51: upper(fs) is FixedString(8),
// upper(s) is String).
func caseFoldingFunctionType(args []CHType) (CHType, error) {
	first, err := firstFunctionArgument(args)
	if err != nil {
		return CHType{}, err
	}
	if strings.EqualFold(first.Name, "FixedString") {
		return first, nil
	}
	return CHType{Name: "String"}, nil
}

// reverseFunctionType types reverse, which reads a text OR a container
// and gives a DIFFERENT shape for each. upper and lower share
// caseFoldingFunctionType, but reverse cannot, because that rule answers
// String for every argument that is not a FixedString, and String for an
// Array or a Tuple argument is a SILENTLY WRONG TYPE.
//
// Measured on ClickHouse 25.8.29.51 over real columns of a real table,
// each cell confirmed by execution with ignore():
//
//	TEXT, the caseFolding shape:
//	  reverse(s)      String          reverse(fs8)   FixedString(8)
//
//	ARRAY, the element type is KEPT, but an inner LowCardinality is
//	STRIPPED:
//	  reverse(arr_i)  Array(Int32)    reverse(arr_n) Array(Nullable(Int32))
//	  reverse(aa)     Array(Array(Int32))
//	  reverse(lca)    Array(String)   <- Array(LowCardinality(String))
//
//	TUPLE, the ELEMENT ORDER is reversed, and the names of a named
//	Tuple travel with their elements:
//	  reverse(tup)    Tuple(Int32, String)   -> Tuple(String, Int32)
//	  reverse(t3)     Tuple(Int32, String, Date)
//	                                         -> Tuple(Date, String, Int32)
//	  reverse(ntup)   Tuple(x Int32, y String)
//	                                         -> Tuple(y String, x Int32)
//
// An identity rule for Tuple would answer the argument type unchanged,
// which is wrong as soon as the elements differ, and an identity rule
// for Array would keep a LowCardinality the server drops.
func reverseFunctionType(args []CHType) (CHType, error) {
	first, err := firstFunctionArgument(args)
	if err != nil {
		return CHType{}, err
	}
	switch first.normalizedName() {
	case "array":
		if len(first.Params) != 1 {
			return CHType{}, fmt.Errorf("function reverse: an Array argument needs one element type; %s", pinTypeHint)
		}
		return CHType{Name: "Array", Params: []CHType{stripLowCardinalityDeep(first.Params[0])}}, nil
	case "tuple":
		reversed := CHType{Name: "Tuple", Params: make([]CHType, 0, len(first.Params))}
		for i := len(first.Params) - 1; i >= 0; i-- {
			reversed.Params = append(reversed.Params, first.Params[i])
		}
		// The element names, when the Tuple has them, travel with their
		// own elements and are reversed with them.
		if len(first.ParamNames) == len(first.Params) {
			reversed.ParamNames = make([]string, 0, len(first.ParamNames))
			for i := len(first.ParamNames) - 1; i >= 0; i-- {
				reversed.ParamNames = append(reversed.ParamNames, first.ParamNames[i])
			}
		}
		return reversed, nil
	}
	return caseFoldingFunctionType(args)
}

// concatFunctionType types concat and the "||" operator, which share
// this one rule.
//
// concat does TWO different things, and the argument types select which.
// Over containers of the SAME kind it JOINS the containers and keeps the
// container type. Over anything else it makes the string form of each
// argument and gives String. A constant String rule answers String for
// the join case as well, which is a SILENTLY WRONG TYPE.
//
// Measured on ClickHouse 25.8.29.51 over real columns of a real table
// with one row, never over literals, because the server folds a constant.
// Every cell was confirmed with a VALUE select as well as toTypeName,
// because toTypeName is analysis and is blind to a run-time refusal.
//
// THE JOIN CASE. Two or more arguments, and EVERY argument is an Array,
// or EVERY argument is a Map. The result is the common supertype of the
// arguments:
//
//	concat(arr_i32, arr_i32)          Array(Int32)     [3,4,3,4]
//	arr_i32 || arr_i32                Array(Int32)     [3,4,3,4]
//	concat(arr_s, arr_s)              Array(String)    ['c','d','c','d']
//	concat(arr_i32, arr_i32, arr_i32) Array(Int32)     [3,4,3,4,3,4]
//	concat(a8, a32)                   Array(Int32)     (the supertype widens)
//	concat(arr_lc, arr_lc)            Array(String)    (element LowCardinality comes off)
//	concat(map, map)                  Map(String, Int64)
//	concat(m1, m2)                    Map(String, Int64)
//
// The supertype is not a guess: the server REFUSES a pair that has no
// supertype, and commonCHTypes refuses the same pair.
//
//	concat(arr_i32, arr_u64)   Code: 386 (NO_COMMON_TYPE)
//	concat(arr_i32, arr_s)     Code: 386
//	concat(arr_lc, a32)        Code: 386
//	concat(m1, m3)             Code: 386
//
// THE STRING CASE. Everything else, including a MIXED call. A mixed call
// is NOT a refusal: the server makes the string form of the container.
//
//	concat(arr_i32, s)   String   [3,4]s
//	concat(s, arr_i32)   String   s[3,4]
//	arr_i32 || s         String   [3,4]s
//	concat(map, s)       String   {'m':1}s
//	concat(arr_i32, i32) String   [3,4]5
//	concat(s, s)         String   ss
//	concat(fs, fs)       String   abcdabcd
//
// ARITY ONE IS THE STRING CASE, for every argument shape. This is why
// the rule counts the arguments first. An arity-one measurement alone
// would have given the wrong rule:
//
//	concat(arr_i32)   String   [3,4]
//	concat(arr_s)     String   ['c','d']
//	concat(map)       String   {'m':1}
//	concat(tup)       String   ('t',2)
//
// TUPLE IS DELIBERATELY NOT A JOIN CASE. The server joins two tuples by
// FLATTENING the element lists, thus the result is not the supertype of
// the arguments and it is not the argument type either:
//
//	concat(tup, tup)   Tuple(String, Int32, String, Int32)   ('t',2,'t',2)
//
// A supertype cannot express that shape. chgen therefore REFUSES a
// Tuple join rather than answer a type it cannot compute. A refusal is
// mild; a wrong Tuple shape would be the worst defect class.
//
// THE MARKER IS DROPPED. concat is not a value-preserving rule, thus the
// caller replaces a SimpleAggregateFunction(f, T) argument by T before
// the rule runs. The measured result carries no marker, which agrees:
//
//	concat(c_arr_i32, c_arr_i32)   Array(Int32)   (NOT SimpleAggregateFunction)
//	c_arr_i32 || c_arr_i32         Array(Int32)
//	concat(c_arr_i32, p_arr_i32)   Array(Int32)   (marker and plain mix freely)
//	concat(c_s, c_s)               String
//
// Thus this rule needs no marker walk of its own, and it adds none.
//
// The rule READS its arguments, thus the registry entry must use the
// argsGeneric strategy. With argsIndependent the caller passes nil and
// no rule of any body could see the Array.
func concatFunctionType(args []CHType) (CHType, error) {
	// Arity one is always the string form. Measured above.
	if len(args) < 2 {
		return CHType{Name: "String"}, nil
	}
	// A join needs EVERY argument to be the same container kind. One
	// argument of another shape makes the whole call the string form.
	if allCHTypesNamed(args, "Array") || allCHTypesNamed(args, "Map") {
		return commonCHTypes(args)
	}
	// A Tuple join has a shape that a supertype cannot express. Refuse
	// it rather than answer a wrong shape.
	if allCHTypesNamed(args, "Tuple") {
		return CHType{}, fmt.Errorf(
			"concat over Tuple arguments joins the element lists, and chgen cannot express that result shape; %s",
			pinTypeHint,
		)
	}
	return CHType{Name: "String"}, nil
}

// allCHTypesNamed reports whether every type carries the given outer type
// name. It is the membership test of the concat join case.
func allCHTypesNamed(types []CHType, name string) bool {
	for _, value := range types {
		if !strings.EqualFold(value.Name, name) {
			return false
		}
	}
	return true
}

func fixedFunctionType(name string) functionTypeRule {
	return func(_ []CHType) (CHType, error) {
		return CHType{Name: name}, nil
	}
}

// fixedResultFunctionType reports whether the rule of a function gives
// the SAME result for every argument type. Such a rule COMPUTES a new
// value and never gives its argument back, thus the result cannot carry
// the SimpleAggregateFunction marker of the argument.
//
// It is measured, not assumed: uniqCombined has the class
// wrapperAggregate, whose transport keeps the marker by default, and it
// has no argument domain that would mark it as value-computing. Only the
// fixed rule separates it (measured on ClickHouse 25.8.29.51 with real
// columns: uniqCombined(saf) is UInt64, while greatest(saf) and max(saf)
// are both SimpleAggregateFunction(anyLast, UInt64)).
//
// The probe uses two unrelated argument types. A rule that answers the
// same type for both ignores its argument by construction.
func fixedResultFunctionType(rule functionTypeRule) bool {
	if rule == nil {
		return false
	}
	first, firstErr := rule([]CHType{{Name: "Int32"}})
	second, secondErr := rule([]CHType{{Name: "String"}})
	if firstErr != nil || secondErr != nil {
		return false
	}
	return first.String() == second.String()
}

func firstFunctionArgument(args []CHType) (CHType, error) {
	if len(args) == 0 {
		return CHType{}, fmt.Errorf("function has no arguments")
	}
	return args[0], nil
}

// arrayDistinctFunctionResult types arrayDistinct. The function gives
// its argument back with the TOP-LEVEL Nullable of the ELEMENT removed.
//
// arrayDistinct removes the duplicate elements. A NULL is a duplicate of
// a NULL, thus the result holds at most one NULL; in fact the server
// removes the NULL from the DATA, so the result can hold none and its
// element type is not Nullable. Measured on ClickHouse 25.8.29.51 with a
// real column an = Array(Nullable(Int32)) that held [1, NULL, 1], and
// reading the VALUE as well as the type:
//
//	arrayDistinct(an)     Array(Int32)            [1]
//	arraySort(an)         Array(Nullable(Int32))  [1,1,NULL]
//	arraySlice(an,1,2)    Array(Nullable(Int32))  [1,NULL]
//	arrayResize(an,2)     Array(Nullable(Int32))  [1,NULL]
//
// The three neighbours keep every element, thus they keep the null and
// the Nullable. They stay on firstFunctionArgument.
//
// The removal reaches the top level of the element only. Measured:
//
//	Array(Array(Nullable(Int32)))          unchanged
//	Array(Tuple(Nullable(Int32), String))  unchanged
//	Array(Map(String, Nullable(Int32)))    unchanged
//
// arrayDistinct compares whole elements and does not look inside a
// container, thus a Nullable at a nested depth stays.
//
// The LowCardinality of an element is NOT removed here, although
// arrayDistinct(Array(LowCardinality(String))) is Array(String). That
// removal is shared with the neighbours (measured: arraySort and
// arraySlice give Array(String) for the same column, and both remove it
// at a nested depth too), thus it is not this function's rule and
// another part of the stack owns it.
func arrayDistinctFunctionResult(args []CHType) (CHType, error) {
	argument, err := firstFunctionArgument(args)
	if err != nil {
		return CHType{}, err
	}
	if !strings.EqualFold(argument.Name, "Array") || len(argument.Params) != 1 {
		return argument, nil
	}
	element := argument.Params[0]
	if !strings.EqualFold(element.Name, "Nullable") || len(element.Params) != 1 {
		return argument, nil
	}
	return CHType{Name: argument.Name, Params: []CHType{element.Params[0]}}, nil
}

// arrayResizeFunctionResult keeps the input array element for two arguments.
// The third argument joins with that element and can widen the result.
func arrayResizeFunctionResult(args []CHType) (CHType, error) {
	if len(args) < 2 {
		return CHType{}, fmt.Errorf("arrayResize needs an array and a size")
	}
	arrayType := domainBaseType(args[0])
	if arrayType.normalizedName() != "array" || len(arrayType.Params) != 1 {
		return CHType{}, fmt.Errorf("arrayResize needs an Array as its first argument")
	}
	if len(args) == 2 {
		return arrayType, nil
	}
	element, err := commonContainerMemberCHType([]CHType{arrayType.Params[0], args[2]})
	if err != nil {
		return CHType{}, fmt.Errorf("arrayResize element: %w", err)
	}
	return CHType{Name: "Array", Params: []CHType{element}}, nil
}

// greatestLeastFunctionType builds the type rule for greatest or least.
// name must be "greatest" or "least"; the registry entry of each name
// calls this factory with its own name, because a functionTypeRule
// (func([]CHType) (CHType, error)) has no way to see which registry key
// invoked it.
//
// These functions fold the COMMON type over EVERY argument, exactly as
// the branch family (if, multiIf, ifNull, coalesce) does. They are
// members of that family on the server, thus this rule reuses
// commonCHTypes for every case EXCEPT the one pair that
// greatestLeastCommonCHTypes (supertype.go) carries as its own
// narrow, function-aware exception: a signed integer mixed with UInt64
// at arity two. See that function for the full measurement and the
// three reasons the exception cannot live inside commonCHTypes itself.
//
// Before this rule the two names used firstFunctionArgument, which read
// the FIRST argument only. That made the answer depend on the argument
// ORDER, and it was correct only by accident when the widest argument
// came first. Measured on ClickHouse 25.8.29.51 with real columns, the
// answer is the same in both orders:
//
//	greatest(i32, u64)   Int128           greatest(u64, i32)   Int128
//	greatest(i32, f64)   Float64          greatest(f64, i32)   Float64
//	greatest(i32, dec)   Decimal(18, 4)   greatest(dec, i32)   Decimal(18, 4)
//	greatest(d, dt)      DateTime         greatest(dt, d)      DateTime
//	greatest(fs, s)      String           greatest(s, fs)      String
//
// The fold also gives the correct REFUSAL. A mix with no supertype is
// refused in both orders, where reading one argument answered a type
// (measured: greatest(i32, u64, f64) is Code 386, NO_COMMON_TYPE).
//
// commonCHTypes strips LowCardinality at every depth, on the fold seed
// as well as on each folded pair, thus a NESTED wrapper goes away at
// every arity, including arity one (measured: greatest(alc) is
// Array(String), greatest(tlc) is Tuple(String), greatest(mlc) is
// Map(String, String)). The TOP-LEVEL scalar wrapper is not this rule's
// business: the caller removes it before the rule runs and the wrapper
// applier puts it back at arity one only, which is the one cell where
// the server keeps it (measured: greatest(lc) is LowCardinality(String),
// greatest(lc, lc) is String). See greatestLeastKeepsLowCardinality.
func greatestLeastFunctionType(name string) functionTypeRule {
	return func(args []CHType) (CHType, error) {
		if len(args) == 0 {
			return CHType{}, fmt.Errorf("function has no arguments")
		}
		return greatestLeastCommonCHTypes(name, args)
	}
}

func sumFunctionArgument(args []CHType) (CHType, error) {
	first, err := firstFunctionArgument(args)
	if err != nil {
		return CHType{}, err
	}
	nullable := strings.EqualFold(first.Name, "Nullable")
	if nullable {
		if len(first.Params) != 1 {
			return CHType{}, fmt.Errorf("Nullable expects one type argument")
		}
		first = first.Params[0]
	}
	var result CHType
	switch strings.ToLower(first.Name) {
	case "uint8", "uint16", "uint32", "uint64", "bool", "boolean":
		// Measured on ClickHouse 25.8.29.51 with real columns:
		// sum(UInt8) and sum(Bool) are both UInt64.
		result = CHType{Name: "UInt64"}
	case "int8", "int16", "int32", "int64", "int":
		result = CHType{Name: "Int64"}
	// The wide integers keep their own width, they do not widen to
	// Int64 (measured: sum(Int128) is Int128, sum(UInt256) is UInt256).
	case "int128", "uint128", "int256", "uint256":
		result = CHType{Name: first.Name}
	// An Enum sums as its underlying signed integer (measured:
	// sum(Enum8) and sum(Enum16) are both Int64).
	case "enum", "enum8", "enum16":
		result = CHType{Name: "Int64"}
	case "float32", "float64":
		result = CHType{Name: "Float64"}
	case "decimal", "decimal32", "decimal64", "decimal128", "decimal256":
		scale := decimalScale(first)
		if scale == "" {
			return CHType{}, fmt.Errorf("sum(%s) requires a decimal scale", first.String())
		}
		result = CHType{Name: "Decimal", LiteralParams: []string{decimalSumPrecision(first), scale}}
	default:
		result = first
	}
	if nullable {
		return CHType{Name: "Nullable", Params: []CHType{result}}, nil
	}
	return result, nil
}

// groupBitFunctionArgument answers the type of groupBitAnd, groupBitOr and
// groupBitXor. The three share one rule because they share one domain
// (groupBitArgumentDomain) and one server behaviour: the result is the
// argument's base type verbatim, except Bool normalizes to its storage
// type UInt8. Signed widths stay signed; unsigned widths stay unsigned;
// no width ever changes. This differs from sum, which widens.
//
// Measured on ClickHouse 25.8.29.51 with real columns in a real table,
// confirmed by execution (FORMAT TSVWithNamesAndTypes), for all three
// function names alike:
//
//	groupBitAnd(bool)    UInt8     groupBitAnd(i8)    Int8
//	groupBitAnd(u8)      UInt8     groupBitAnd(i16)   Int16
//	groupBitAnd(u16)     UInt16    groupBitAnd(i32)   Int32
//	groupBitAnd(u32)     UInt32    groupBitAnd(i64)   Int64
//	groupBitAnd(u64)     UInt64    groupBitAnd(i128)  Int128
//	groupBitAnd(u128)    UInt128   groupBitAnd(i256)  Int256
//	groupBitAnd(u256)    UInt256
//
// Nullable is kept and Bool still normalizes inside it (measured:
// groupBitAnd(Nullable(Bool)) is Nullable(UInt8), groupBitAnd(Nullable(Int32))
// is Nullable(Int32)). LowCardinality is removed, same as every other
// aggregate (measured: groupBitAnd(LowCardinality(Bool)) is UInt8,
// groupBitAnd(LowCardinality(Int32)) is Int32).
//
// groupBitArgumentDomain already refuses every non-integer base type
// (Float64, Decimal, String, Date, UUID, Array, ...), so this rule only
// runs on a base type it knows.
func groupBitFunctionArgument(args []CHType) (CHType, error) {
	first, err := firstFunctionArgument(args)
	if err != nil {
		return CHType{}, err
	}
	base, nullable, lowCardinality := splitCHWrappers(first)
	var result CHType
	switch strings.ToLower(base.Name) {
	case "bool", "boolean":
		result = CHType{Name: "UInt8"}
	case "int8", "int16", "int32", "int64", "int128", "int256",
		"uint8", "uint16", "uint32", "uint64", "uint128", "uint256":
		result = base
	default:
		return CHType{}, fmt.Errorf("groupBitAnd/groupBitOr/groupBitXor: cannot derive a type for argument of type %s", first.String())
	}
	return applyCHWrappers(result, nullable, lowCardinality), nil
}

// sumWithOverflowFunctionArgument types sumWithOverflow. It gives its
// argument's type back UNCHANGED for every integer, float and Decimal
// (measured: sumWithOverflow(i32) is Int32, sumWithOverflow(d2) is
// Decimal(18, 4), the exact input scale and precision, while sum widens
// both). An Enum argument converts to its underlying signed integer width.
// Bool converts to its storage type UInt8. These are the two exceptions.
// They are measured over real columns: sumWithOverflow(e8) is Int8,
// sumWithOverflow(e16) is Int16 and sumWithOverflow(b) is UInt8. The sum
// function gives Int64 for e8 and UInt64 for b, so this is not the sum rule.
// ClickHouse 25.8.29.51.
func sumWithOverflowFunctionArgument(args []CHType) (CHType, error) {
	first, err := firstFunctionArgument(args)
	if err != nil {
		return CHType{}, err
	}
	base, nullable, lowCardinality := splitCHWrappers(first)
	var result CHType
	switch base.normalizedName() {
	case "bool", "boolean":
		result = CHType{Name: "UInt8"}
	case "enum", "enum8":
		result = CHType{Name: "Int8"}
	case "enum16":
		result = CHType{Name: "Int16"}
	default:
		result = base
	}
	return applyCHWrappers(result, nullable, lowCardinality), nil
}

func quantileFunctionArgument(args []CHType) (CHType, error) {
	first, err := firstFunctionArgument(args)
	if err != nil {
		return CHType{}, err
	}
	return quantileFunctionResultType(first), nil
}

func aggregateStateFunctionArgument(args []CHType) (CHType, error) {
	first, err := firstFunctionArgument(args)
	if err != nil {
		return CHType{}, err
	}
	return CHType{Name: "AggregateFunction", Params: []CHType{
		{Name: "quantile"},
		first,
	}}, nil
}

func quantileFunctionResultType(argument CHType) CHType {
	if strings.EqualFold(argument.Name, "Nullable") {
		if len(argument.Params) != 1 {
			return argument
		}
		return CHType{Name: "Nullable", Params: []CHType{quantileFunctionResultType(argument.Params[0])}}
	}
	if arithmeticDecimalType(argument) {
		// median and quantile over a Decimal column return the input
		// Decimal type unchanged. Measured on ClickHouse 25.8.29.51
		// with real columns: median(Decimal(10, 2)) is Decimal(10, 2),
		// quantile(0.5)(Decimal64(10)) is Decimal(18, 10),
		// quantile(0.9)(Decimal128(20)) is Decimal(38, 20) and
		// median(Decimal256(40)) is Decimal(76, 40): the server spells
		// a DecimalN(S) result in the canonical Decimal(P, S) form.
		return canonicalDecimalType(argument)
	}
	// Every integer and float argument gives Float64. The wide integers
	// and Bool belong to this group too, although numericCHType does not
	// name them (measured on ClickHouse 25.8.29.51 with real columns:
	// quantile(0.5)(Int128), quantile(0.5)(UInt256) and
	// quantile(0.5)(Bool) are all Float64).
	if numericCHType(argument) || integerBaseType(argument) || floatBaseType(argument) {
		return CHType{Name: "Float64"}
	}
	// Quantile over Date/Date32/DateTime/DateTime64 preserves the input type
	// in ClickHouse. Keeping the complete type also preserves DateTime64's
	// precision and timezone metadata.
	return argument
}

// canonicalDecimalType spells a DecimalN(S) alias in the canonical
// Decimal(P, S) form that the server reports: Decimal32 holds 9 digits,
// Decimal64 18, Decimal128 38 and Decimal256 76. A type that is already
// Decimal(P, S) or has no readable scale stays unchanged.
func canonicalDecimalType(value CHType) CHType {
	var precision string
	switch strings.ToLower(value.Name) {
	case "decimal32":
		precision = "9"
	case "decimal64":
		precision = "18"
	case "decimal128":
		precision = "38"
	case "decimal256":
		precision = "76"
	default:
		return value
	}
	if len(value.LiteralParams) != 1 {
		return value
	}
	return CHType{Name: "Decimal", LiteralParams: []string{precision, value.LiteralParams[0]}}
}

func decimalScale(value CHType) string {
	switch strings.ToLower(value.Name) {
	case "decimal32", "decimal64", "decimal128", "decimal256":
		if len(value.LiteralParams) == 1 {
			return value.LiteralParams[0]
		}
	case "decimal":
		if len(value.LiteralParams) >= 2 {
			return value.LiteralParams[1]
		}
	}
	return ""
}

func decimalSumPrecision(value CHType) string {
	if strings.EqualFold(value.Name, "Decimal256") {
		return "76"
	}
	if strings.EqualFold(value.Name, "Decimal") && len(value.LiteralParams) > 0 {
		precision, err := strconv.Atoi(value.LiteralParams[0])
		if err == nil && precision > 38 {
			return "76"
		}
	}
	return "38"
}

// aggregateStateFunctionType gives the result type of quantileState and
// quantileStateIf. The AggregateFunction parameter list holds the
// PARAMETERS of the aggregate, never the data argument.
//
// A parametric aggregate has two argument lists: in quantileState(0.5)(x)
// the level 0.5 is a parameter and x is the data argument. The parser puts
// the parameter list in function.Params, but for the plain spelling
// quantileState(x) it puts the DATA argument there instead, with no
// separate ColumnArgList. Without the ColumnArgList guard below, the text
// of the data argument became the parameter list, thus chgen gave
// AggregateFunction(quantile(f64), Float64) where the server gives
// AggregateFunction(quantile, Float64). That spelling is not a valid type
// at all: the server answers Code: 134,
// PARAMETERS_TO_AGGREGATE_FUNCTIONS_MUST_BE_LITERALS.
//
// Measured on ClickHouse 25.8.29.51 over real table columns:
//
//	quantileState(f64)           AggregateFunction(quantile, Float64)
//	quantileState(0.5)(f64)      AggregateFunction(quantile(0.5), Float64)
//	quantileStateIf(f64, b)      AggregateFunction(quantile, Float64)
//	quantileStateIf(0.5)(f64, b) AggregateFunction(quantile(0.5), Float64)
func aggregateStateFunctionType(function *clickhouse.FunctionExpr, argument CHType) CHType {
	return CHType{Name: "AggregateFunction", Params: []CHType{
		{Name: "quantile", LiteralParams: aggregateParametricLiterals(function)},
		argument,
	}}
}

func functionParamLiterals(function *clickhouse.FunctionExpr) []string {
	if function == nil || function.Params == nil || function.Params.Items == nil {
		return nil
	}
	params := make([]string, 0, len(function.Params.Items.Items))
	for _, item := range function.Params.Items.Items {
		params = append(params, clickhouse.Format(item))
	}
	return params
}

// withoutNullableFunctionArgument is the rule for assumeNotNull. It
// removes the Nullable from the argument and gives the rest back.
//
// The Nullable is not always at the top level, thus this rule has to
// reach it in two ways. Both were measured on ClickHouse 25.8.29.51 with
// real columns of a real table, never over literals.
//
// FIRST, a SimpleAggregateFunction marker. assumeNotNull READS its
// argument as a value, thus a marker that does not survive that read is
// replaced by its inner type before the Nullable is looked for. The
// marker survives a bare scalar inner type only:
//
//	assumeNotNull(saf)      SimpleAggregateFunction(anyLast, Int32)
//	assumeNotNull(safn)     Int32
//	assumeNotNull(safarrb)  Array(Int32)
//
// saf and safarrb both have a NON-Nullable inner type and answer
// differently, thus the test is not nullability and it is not an
// unconditional look-through. See simpleAggregateMarkerSurvives.
//
// SECOND, a LowCardinality wrapper. The server removes the Nullable from
// INSIDE the LowCardinality and keeps the LowCardinality itself:
//
//	assumeNotNull(lcn)      LowCardinality(String)
//	assumeNotNull(lcn_i32)  LowCardinality(Int32)
//	assumeNotNull(lc)       LowCardinality(String)
//
// The two rules meet on one cell. An inner LowCardinality(Nullable(T))
// of a marker needs the marker read first and then the entry into the
// LowCardinality:
//
//	assumeNotNull(saflcn)   LowCardinality(String)
//
// THIRD, an element LowCardinality inside a container argument. The
// server holds one general rule for element LowCardinality: it is gone
// from the read value at ANY depth, inside Array, Map and Tuple alike,
// while a Nullable element survives. assumeNotNull reads its whole
// argument as a value (see the marker rule above), so this rule applies
// too. Measured on ClickHouse 25.8.29.51 with real columns, never
// literals (lca Array(LowCardinality(String)), lcna
// Array(LowCardinality(Nullable(String)))):
//
//	assumeNotNull(lca)   Array(String)           ['a','b']
//	assumeNotNull(lcna)  Array(Nullable(String)) ['a',NULL]
//
// stripNestedLowCardinality is the shared function for this rule; see
// its doc comment for the Tuple and Map depth measurements. It is a
// no-op on a type that carries no LowCardinality, so it is safe to
// apply unconditionally after the top-level LowCardinality and
// Nullable checks below.
func withoutNullableFunctionArgument(args []CHType) (CHType, error) {
	result, err := firstFunctionArgument(args)
	if err != nil {
		return CHType{}, err
	}
	result = readSimpleAggregateValue(result)
	if strings.EqualFold(result.Name, "LowCardinality") && len(result.Params) == 1 {
		inner := result.Params[0]
		if strings.EqualFold(inner.Name, "Nullable") && len(inner.Params) == 1 {
			return wrapLowCardinality(inner.Params[0]), nil
		}
		return result, nil
	}
	if strings.EqualFold(result.Name, "Nullable") && len(result.Params) == 1 {
		return result.Params[0], nil
	}
	return stripNestedLowCardinality(result), nil
}

// inferConditionalFamilyType types if, multiIf, coalesce and ifNull.
// ClickHouse gives these the common supertype of the value arguments,
// not the type of one branch (measured on ClickHouse 25.8.29.51:
// coalesce(nullIf(nf64, 255), 255) is Float64, multiIf(b, i16, b, b,
// ni32) is Nullable(Int32)). The condition arguments of if and multiIf
// do not reach the result (if(ni32 = 1, u8, u16) is UInt16).
//
// The Nullable rule differs by function:
//   - if and multiIf: the result is Nullable when a value argument is.
//   - coalesce: the result is Nullable only when every argument is.
//   - ifNull: the result is Nullable only when the second argument is.
//

func inferConditionalFamilyType(name, displayName string, args []clickhouse.Expr, scope queryScope) (CHType, error) {
	var valueIndexes []int
	switch name {
	case "if":
		if len(args) != 3 {
			return CHType{}, fmt.Errorf("function %s expects a condition and two values", displayName)
		}
		valueIndexes = []int{1, 2}
	case "multiif":
		if len(args) < 3 || len(args)%2 == 0 {
			return CHType{}, fmt.Errorf("function %s expects condition/value pairs and a final value", displayName)
		}
		for index := 1; index < len(args)-1; index += 2 {
			valueIndexes = append(valueIndexes, index)
		}
		valueIndexes = append(valueIndexes, len(args)-1)
	case "ifnull":
		if len(args) != 2 {
			return CHType{}, fmt.Errorf("function %s expects two arguments", displayName)
		}
		valueIndexes = []int{0, 1}
	default: // coalesce
		if len(args) == 0 {
			return CHType{}, fmt.Errorf("function %s has no arguments", displayName)
		}
		for index := range args {
			valueIndexes = append(valueIndexes, index)
		}
	}
	// An `if` whose condition folds to a constant loses its dead branch
	// before that branch gets a type. Thus a dead branch with no type
	// does not stop the expression. Measured on ClickHouse 25.8.29.51
	// with real columns: if(false, if(b, i64, f64), u8) is UInt8, while
	// if(b, if(b, i64, f64), u8) is Code: 386. See
	// constantConditionTruth for the whole rule and its edges.
	//
	// The live branch keeps its own errors, and a dead branch that DOES
	// have a type still joins the result: if(false, nf64, u8) is
	// Nullable(Float64), not UInt8, and if(false, f64, i64) is still a
	// refusal. Thus this can only remove a branch that had no type at
	// all, and it can never make a join accept a pair that the server
	// refuses.
	if name == "if" {
		if truth, constant := constantConditionTruth(args[0], scope); constant {
			liveIndex, deadIndex := 2, 1
			if truth {
				liveIndex, deadIndex = 1, 2
			}
			if _, err := inferExprType(args[deadIndex], scope); err != nil {
				liveType, liveErr := inferExprType(args[liveIndex], scope)
				if liveErr != nil {
					return CHType{}, fmt.Errorf("function %s argument: %w", displayName, liveErr)
				}
				return liveType, nil
			}
		}
	}
	// The condition arguments do not reach the result type, but they must
	// still get a type. The server refuses a condition that it cannot
	// type: if(bogusfn(s), i32, i32) and multiIf(bogusfn(s), i32, i32)
	// are both Code: 46 on ClickHouse 25.8.29.51. Without this check the
	// condition slot gives a result type for a query that never runs.
	// A bare positional placeholder is absorbed, because a pinned
	// parameter in the condition is normal and has no result type.
	// coalesce and ifNull have no condition arguments, thus this loop is
	// empty for them.
	valueSlot := make(map[int]bool, len(valueIndexes))
	for _, index := range valueIndexes {
		valueSlot[index] = true
	}
	for index := range args {
		if valueSlot[index] {
			continue
		}
		if _, err := inferExprType(args[index], scope); err != nil && !errors.Is(err, errPlaceholderResultType) {
			return CHType{}, fmt.Errorf("function %s condition: %w", displayName, err)
		}
	}
	valueTypes := make([]CHType, 0, len(valueIndexes))
	for _, index := range valueIndexes {
		valueType, err := inferExprType(args[index], scope)
		if err != nil {
			return CHType{}, fmt.Errorf("function %s argument: %w", displayName, err)
		}
		valueTypes = append(valueTypes, valueType)
	}
	valueExprs := make([]clickhouse.Expr, 0, len(valueIndexes))
	for _, index := range valueIndexes {
		valueExprs = append(valueExprs, args[index])
	}
	// coalesce returns the first argument that is not NULL. A
	// non-Nullable argument can never be NULL, thus every argument
	// after it is unreachable and the server ignores it. Measured on
	// ClickHouse 25.8.29.51 with real columns:
	//
	//   coalesce(u64, i32)              UInt64
	//   coalesce(u64, f64, i64, s)      UInt64
	//   coalesce(ni64, ni32, i64)       Int64
	//   coalesce(ni64, ni32, i64, f64)  Int64
	//   coalesce(nu64, i32)             refused, code 386
	//
	// UInt64 with Int32 and Int64 with Float64 have no common type at
	// all, and coalesce still answers. This is therefore not a
	// supertype rule over every argument: the list is truncated after
	// the first non-Nullable argument and the ordinary common type is
	// taken over that prefix. The truncated search still refuses the
	// same pairs that if() refuses, which is why coalesce(nu64, i32)
	// stays a refusal: a Nullable first argument does not short
	// circuit, thus the terminator joins the search.
	if name == "coalesce" {
		for index, valueType := range valueTypes {
			if !branchArgumentNullable(valueType) {
				valueTypes = valueTypes[:index+1]
				valueExprs = valueExprs[:index+1]
				break
			}
		}
	}
	var result CHType
	var err error
	if name == "coalesce" || name == "ifnull" {
		result, err = commonNullEliminatingCHType(valueTypes, valueExprs)
	} else {
		result, err = commonBranchCHType(valueTypes, valueExprs)
	}
	if err != nil {
		return CHType{}, err
	}
	// coalesce and ifNull RESOLVE the nullability, thus a
	// SimpleAggregateFunction(f, Nullable(T)) result loses its wrapper:
	// the wrapper cannot hold the type after the null is taken away. The
	// branch function if() keeps it, because it resolves nothing.
	//
	// Measured on ClickHouse 25.8.29.51 with real table columns:
	//
	//	ifNull(saggn, ni64)      Nullable(Int64)
	//	coalesce(saggn, ni64)    Nullable(Int64)
	//	if(c, saggn, ni64)       SimpleAggregateFunction(sum, Nullable(Int64))
	//
	// A NON-Nullable inner type keeps the wrapper here as well:
	//
	//	ifNull(sagg, ni64)       SimpleAggregateFunction(sum, Int64)
	//	coalesce(sagg, ni64)     SimpleAggregateFunction(sum, Int64)
	if name == "coalesce" || name == "ifnull" {
		if inner, wrapped := simpleAggregateWrapperInner(result); wrapped {
			if _, innerNullable, _ := splitCHWrappers(inner); innerNullable {
				result = inner
			}
		}
	}
	base, resultNullable, _ := splitCHWrappers(result)
	resultLowCardinality := false
	switch name {
	case "coalesce":
		resultNullable = true
		for _, valueType := range valueTypes {
			resultNullable = resultNullable && branchArgumentNullable(valueType)
		}
	case "ifnull":
		resultNullable = branchArgumentNullable(valueTypes[1])
		// ifNull keeps one LowCardinality source when every other
		// argument is constant. A constant fallback can widen the base
		// type without removing the encoding. A column fallback removes
		// it. This is the same measured transport rule that transparent
		// functions use, so call the shared resolver instead of copying
		// the condition here.
		//
		// Measured on ClickHouse 25.8.29.51 with real columns and both
		// witnesses:
		//
		//	ifNull(length(lc), toUInt64(1)) LowCardinality(UInt64)
		//	ifNull(length(lc), u64)         UInt64
		//	ifNull(lcn, 'x')                LowCardinality(String)
		//	ifNull(lcn, lc)                 String
		//
		// The rule uses the original expressions because a type alone
		// cannot distinguish the constant fallback from the column.
		_, resultLowCardinality = transparentWrapperFlags(name, valueExprs, valueTypes, scope)
	}
	return applyCHWrappers(base, resultNullable, resultLowCardinality), nil
}

// branchArgumentNullable reports whether coalesce and ifNull see an
// argument as Nullable. It differs from splitCHWrappers in one point: a
// SimpleAggregateFunction(f, Nullable(T)) argument IS Nullable here,
// because the null lives in the inner type and splitCHWrappers does not
// look inside the wrapper.
//
// Measured on ClickHouse 25.8.29.51 through the HTTP interface, against
// real columns of a real table, never over literals, because the server
// folds constants (saggn is SimpleAggregateFunction(sum, Nullable(Int64)),
// sagg is SimpleAggregateFunction(sum, Int64), i64 is Int64, ni64 is
// Nullable(Int64)):
//
//	coalesce(saggn, i64)          Int64
//	coalesce(saggn, ni64)         Nullable(Int64)
//	coalesce(saggn, saggn, i64)   Int64
//	ifNull(saggn, i64)            Int64
//	ifNull(saggn, ni64)           Nullable(Int64)
//
// A non-Nullable inner type terminates the search as before, thus the
// wrapper is kept:
//
//	coalesce(sagg, i64)   SimpleAggregateFunction(sum, Int64)
//	ifNull(sagg, i64)     SimpleAggregateFunction(sum, Int64)
func branchArgumentNullable(value CHType) bool {
	if inner, ok := simpleAggregateWrapperInner(value); ok {
		value = inner
	}
	_, nullable, _ := splitCHWrappers(value)
	return nullable
}

// branchArgumentLowCardinality reports whether an argument is
// LowCardinality for the rule that decides the result wrapper. It is the
// LowCardinality twin of branchArgumentNullable, and it differs from
// splitCHWrappers in the same one point: a
// SimpleAggregateFunction(f, LowCardinality(T)) argument IS
// LowCardinality here, because the wrapper lives in the inner type and
// splitCHWrappers does not look inside the marker.
//
// The reason is the measured server behaviour. A function that COMPUTES
// a new value drops the marker and then answers about the value inside
// it. That value is LowCardinality, thus the result carries the wrapper.
// The marker hides it from a flag that reads the type only, and the
// result then loses a wrapper that the server keeps.
//
// Measured on ClickHouse 25.8.29.51 through the HTTP interface, against
// real columns of a real AggregatingMergeTree table with one row, never
// over literals, because the server folds constants (saflc is
// SimpleAggregateFunction(anyLast, LowCardinality(Int32)), saflcs is
// SimpleAggregateFunction(anyLast, LowCardinality(String)), saf is
// SimpleAggregateFunction(anyLast, Int32)):
//
//	toString(saflc)     LowCardinality(String)
//	toUInt64(saflc)     LowCardinality(UInt64)
//	hex(saflc)          LowCardinality(String)
//	cityHash64(saflc)   LowCardinality(UInt64)
//	length(saflcs)      LowCardinality(UInt64)
//	empty(saflcs)       LowCardinality(UInt8)
//	trim(saflcs)        LowCardinality(String)
//	saflcs LIKE 'a'     LowCardinality(UInt8)
//
// A PLAIN LowCardinality column answers the same type for every row
// above, thus the marker must not change the answer.
//
// A BARE inner type terminates the search, thus the result stays bare
// and the rule is not an unconditional wrap:
//
//	toString(saf)   String
//
// The flag says only that the argument OFFERS the wrapper. The transport
// of the function still decides whether the result keeps it, and an
// aggregate drops it (measured: max(saflcs) is a bare String).
func branchArgumentLowCardinality(value CHType) bool {
	if _, _, lowCardinality := splitCHWrappers(value); lowCardinality {
		return true
	}
	if inner, ok := simpleAggregateWrapperInner(value); ok {
		_, _, lowCardinality := splitCHWrappers(inner)
		return lowCardinality
	}
	return false
}

// inferLogicOperatorFunctionType types the FUNCTION spelling of and, or
// and xor: and(a, b), or(a, b), xor(a, b, ...).
//
// chgen already typed the infix spelling of AND and OR through
// inferBinaryOperationType. This function gives the function spelling the
// SAME measured behaviour, so that "a AND b" and "and(a, b)" answer
// alike, and it is the first rule for xor, which has no infix spelling at
// all (measured: "1 xor 0" is Code: 62, a parse error at the SQL level,
// not a type refusal).
//
// The argument domain is the narrow integers, the two floats and Bool
// (logicOperatorArgumentDomain), and it applies to EVERY argument, not
// only the first: and, or and xor are variadic, and the server refuses
// each operand on its own (measured: and(u8, s) is Code: 43 naming
// argument 2, not argument 1). The generic argsGeneric path in
// inferFunctionType only checks argument zero, which is too narrow for
// this family, thus the check runs here instead.
//
// The result base is UInt8 by default, and it is Bool when a Bool value
// is present among the arguments. AND and OR use the SAME rule that the
// infix path already measured: a Bool counts only when it sits OUTSIDE a
// Nullable wrapper (andOrOperandCarriesBareBool). xor differs on that one
// point: a Bool counts even under a Nullable wrapper
// (xorOperandCarriesBool). See both helpers for the measured tables; the
// difference is real and not an oversight, for example xor(nb, nb) is
// Nullable(Bool) while and(nb, nb) is Nullable(UInt8), for two distinct
// Nullable(Bool) columns nb.
//
// The Nullable and LowCardinality wrapper dispositions are the ONE
// logicOperatorTransport table, which the infix path also uses, keyed by
// the function's lowercase name. There is no per-call special case here:
// this function only assembles the wrapperCall facts that the transport
// needs.
func inferLogicOperatorFunctionType(name, displayName string, args []clickhouse.Expr, scope queryScope) (CHType, error) {
	if len(args) < 2 {
		return CHType{}, fmt.Errorf(
			"function %s needs at least 2 arguments, the call has %d; %s",
			displayName, len(args), pinTypeHint,
		)
	}
	nullable := false
	lowCardinalityCount := 0
	othersConstant := true
	hasBool := false
	hasDynamic := false
	domain, constrained := argumentDomainFor(name)
	if !constrained {
		return CHType{}, fmt.Errorf(
			"function %s has no measured argument domain; %s",
			displayName, pinTypeHint,
		)
	}
	for _, arg := range args {
		argType, err := inferExprType(arg, scope)
		if err != nil {
			if errors.Is(err, errPlaceholderResultType) {
				othersConstant = false
				continue
			}
			return CHType{}, fmt.Errorf("function %s argument: %w", displayName, err)
		}
		argBase, _, _ := splitCHWrappers(argType)
		logicBase := argBase
		if inner, ok := simpleAggregateWrapperInner(logicBase); ok {
			logicBase, _, _ = splitCHWrappers(inner)
		}
		if !domain.accepts(logicBase) {
			return CHType{}, fmt.Errorf(
				"function %s does not accept an argument of type %s; ClickHouse needs %s here; %s",
				displayName, argType.String(), domain.expected, pinTypeHint,
			)
		}
		if isDynamicCHType(logicBase) {
			hasDynamic = true
			// Dynamic carries its missing-value state without a Nullable
			// wrapper. A measured function fact makes this state visible
			// on the result only for functions that have this mark.
			if functionDynamicForcesNullableFor(name) {
				nullable = true
			}
		}
		nullable = nullable || branchArgumentNullable(argType)
		if branchArgumentLowCardinality(argType) {
			lowCardinalityCount++
		} else if !isConstLiteralExpr(arg, scope) {
			othersConstant = false
		}
		var operandHasBool bool
		var boolErr error
		if name == "xor" {
			operandHasBool, boolErr = xorOperandCarriesBool(arg, scope)
		} else {
			operandHasBool, boolErr = andOrOperandCarriesBareBool(arg, strings.ToUpper(name), scope)
		}
		if boolErr != nil {
			return CHType{}, boolErr
		}
		hasBool = hasBool || operandHasBool
	}
	lowCardinality := lowCardinalityCount == 1 && othersConstant
	base := CHType{Name: "UInt8"}
	// A Bool operand normally makes the xor result Bool. A Dynamic
	// operand keeps the base UInt8, including xor(dyn, b). Both type
	// analysis and execution give Nullable(UInt8) for that measured cell.
	if hasBool && !hasDynamic {
		base = CHType{Name: "Bool"}
	}
	transport := transportForFunction(name, wrapperTransparent).withResolvedLowCardinality(lowCardinality)
	return applyWrapperTransport(
		base,
		transport,
		wrapperCall{
			base:     base,
			stacks:   []wrapperStack{{lowCardinality: lowCardinality, nullable: nullable, outerNullable: nullable}},
			argCount: len(args),
		},
	), nil
}

// higherOrderArrayResult tells what a higher-order array function makes
// from the lambda body type and the data array types.
type higherOrderArrayResult int

const (
	// hofArrayOfBody gives Array(<lambda body type>): arrayMap.
	hofArrayOfBody higherOrderArrayResult = iota
	// hofFirstArray gives the first data array back, with
	// LowCardinality removed: arrayFilter and arraySort.
	hofFirstArray
	// hofSumOfBody gives the promoted sum of the lambda body type:
	// arraySum.
	hofSumOfBody
	// hofBody gives the lambda body type itself: arrayMin and arrayMax.
	hofBody
	// hofCount gives UInt32: arrayCount.
	hofCount
	// hofPredicate gives the predicate result: arrayExists and arrayAll.
	hofPredicate
	// hofFirstElement gives the first Array element type.
	hofFirstElement
	// hofNullableFirstElement gives Nullable(first Array element type).
	hofNullableFirstElement
	// hofFloat64 gives a numeric reduction result.
	hofFloat64
	// hofArrayOfSum gives Array(promoted lambda body type).
	hofArrayOfSum
	// hofArrayOfFirstArray gives Array(first data Array type).
	hofArrayOfFirstArray
	// hofAccumulator gives the accumulator type after an exact body match.
	hofAccumulator
)

type higherOrderLambdaBodyDomain uint8

const (
	hofBodyDomainAny higherOrderLambdaBodyDomain = iota
	hofBodyDomainPredicate
	hofBodyDomainSummable
	hofBodyDomainNumericReduction
)

type higherOrderLengthPolicy uint8

const (
	hofLengthUnknown higherOrderLengthPolicy = iota
	hofLengthEqualAtExecution
)

// higherOrderLambdaAccumulator describes the optional state argument of
// a higher-order function. The first supported group has no accumulator.
// arrayFold uses this field in a later measured slice.
type higherOrderLambdaAccumulator struct {
	argumentPosition  int
	parameterPosition int
}

type higherOrderScalarArgument struct {
	role                string
	positionAfterLambda int
}

// higherOrderLambdaSignature is the executable relation between a lambda
// and its data arrays. Each lambda parameter links to one Array argument.
// The lambda runs in a child scope and can read names from the parent scope.
type higherOrderLambdaSignature struct {
	spelling          string
	result            higherOrderArrayResult
	bodyDomain        higherOrderLambdaBodyDomain
	minimumArrays     int
	maximumArrays     int
	lengthPolicy      higherOrderLengthPolicy
	allowOuterCapture bool
	allowNoLambda     bool
	accumulator       *higherOrderLambdaAccumulator
	scalarArgument    *higherOrderScalarArgument
}

// higherOrderArrayFunctions lists the array functions that take a lambda
// as the first argument, with the full linked signature. Every entry was
// measured on ClickHouse 25.8.29.51 with real array columns, because
// ClickHouse folds constants and a literal array gives a different
// LowCardinality answer.
var higherOrderArrayFunctions = map[string]higherOrderLambdaSignature{
	"arraymap":         {spelling: "arrayMap", result: hofArrayOfBody, bodyDomain: hofBodyDomainAny, minimumArrays: 1, maximumArrays: -1, lengthPolicy: hofLengthEqualAtExecution, allowOuterCapture: true},
	"arrayfilter":      {spelling: "arrayFilter", result: hofFirstArray, bodyDomain: hofBodyDomainPredicate, minimumArrays: 1, maximumArrays: -1, lengthPolicy: hofLengthEqualAtExecution, allowOuterCapture: true},
	"arraysort":        {spelling: "arraySort", result: hofFirstArray, bodyDomain: hofBodyDomainAny, minimumArrays: 1, maximumArrays: -1, lengthPolicy: hofLengthEqualAtExecution, allowOuterCapture: true, allowNoLambda: true},
	"arrayreversesort": {spelling: "arrayReverseSort", result: hofFirstArray, bodyDomain: hofBodyDomainAny, minimumArrays: 1, maximumArrays: -1, lengthPolicy: hofLengthEqualAtExecution, allowOuterCapture: true, allowNoLambda: true},
	"arraypartialsort": {spelling: "arrayPartialSort", result: hofFirstArray, bodyDomain: hofBodyDomainAny, minimumArrays: 1, maximumArrays: -1, lengthPolicy: hofLengthEqualAtExecution, allowOuterCapture: true, allowNoLambda: true,
		scalarArgument: &higherOrderScalarArgument{role: "limit", positionAfterLambda: 0}},
	"arraypartialreversesort": {spelling: "arrayPartialReverseSort", result: hofFirstArray, bodyDomain: hofBodyDomainAny, minimumArrays: 1, maximumArrays: -1, lengthPolicy: hofLengthEqualAtExecution, allowOuterCapture: true, allowNoLambda: true,
		scalarArgument: &higherOrderScalarArgument{role: "limit", positionAfterLambda: 0}},
	"arraysum":               {spelling: "arraySum", result: hofSumOfBody, bodyDomain: hofBodyDomainSummable, minimumArrays: 1, maximumArrays: -1, lengthPolicy: hofLengthEqualAtExecution, allowOuterCapture: true},
	"arraymin":               {spelling: "arrayMin", result: hofBody, bodyDomain: hofBodyDomainAny, minimumArrays: 1, maximumArrays: -1, lengthPolicy: hofLengthEqualAtExecution, allowOuterCapture: true},
	"arraymax":               {spelling: "arrayMax", result: hofBody, bodyDomain: hofBodyDomainAny, minimumArrays: 1, maximumArrays: -1, lengthPolicy: hofLengthEqualAtExecution, allowOuterCapture: true},
	"arraycount":             {spelling: "arrayCount", result: hofCount, bodyDomain: hofBodyDomainPredicate, minimumArrays: 1, maximumArrays: -1, lengthPolicy: hofLengthEqualAtExecution, allowOuterCapture: true},
	"arrayexists":            {spelling: "arrayExists", result: hofPredicate, bodyDomain: hofBodyDomainPredicate, minimumArrays: 1, maximumArrays: -1, lengthPolicy: hofLengthEqualAtExecution, allowOuterCapture: true},
	"arrayall":               {spelling: "arrayAll", result: hofPredicate, bodyDomain: hofBodyDomainPredicate, minimumArrays: 1, maximumArrays: -1, lengthPolicy: hofLengthEqualAtExecution, allowOuterCapture: true},
	"arrayfirst":             {spelling: "arrayFirst", result: hofFirstElement, bodyDomain: hofBodyDomainPredicate, minimumArrays: 1, maximumArrays: -1, lengthPolicy: hofLengthEqualAtExecution, allowOuterCapture: true},
	"arrayfirstornull":       {spelling: "arrayFirstOrNull", result: hofNullableFirstElement, bodyDomain: hofBodyDomainPredicate, minimumArrays: 1, maximumArrays: -1, lengthPolicy: hofLengthEqualAtExecution, allowOuterCapture: true},
	"arraylast":              {spelling: "arrayLast", result: hofFirstElement, bodyDomain: hofBodyDomainPredicate, minimumArrays: 1, maximumArrays: -1, lengthPolicy: hofLengthEqualAtExecution, allowOuterCapture: true},
	"arraylastornull":        {spelling: "arrayLastOrNull", result: hofNullableFirstElement, bodyDomain: hofBodyDomainPredicate, minimumArrays: 1, maximumArrays: -1, lengthPolicy: hofLengthEqualAtExecution, allowOuterCapture: true},
	"arrayfirstindex":        {spelling: "arrayFirstIndex", result: hofCount, bodyDomain: hofBodyDomainPredicate, minimumArrays: 1, maximumArrays: -1, lengthPolicy: hofLengthEqualAtExecution, allowOuterCapture: true},
	"arraylastindex":         {spelling: "arrayLastIndex", result: hofCount, bodyDomain: hofBodyDomainPredicate, minimumArrays: 1, maximumArrays: -1, lengthPolicy: hofLengthEqualAtExecution, allowOuterCapture: true},
	"arrayavg":               {spelling: "arrayAvg", result: hofFloat64, bodyDomain: hofBodyDomainNumericReduction, minimumArrays: 1, maximumArrays: -1, lengthPolicy: hofLengthEqualAtExecution, allowOuterCapture: true, allowNoLambda: true},
	"arrayproduct":           {spelling: "arrayProduct", result: hofFloat64, bodyDomain: hofBodyDomainNumericReduction, minimumArrays: 1, maximumArrays: -1, lengthPolicy: hofLengthEqualAtExecution, allowOuterCapture: true, allowNoLambda: true},
	"arraycumsum":            {spelling: "arrayCumSum", result: hofArrayOfSum, bodyDomain: hofBodyDomainSummable, minimumArrays: 1, maximumArrays: -1, lengthPolicy: hofLengthEqualAtExecution, allowOuterCapture: true, allowNoLambda: true},
	"arraycumsumnonnegative": {spelling: "arrayCumSumNonNegative", result: hofArrayOfSum, bodyDomain: hofBodyDomainSummable, minimumArrays: 1, maximumArrays: -1, lengthPolicy: hofLengthEqualAtExecution, allowOuterCapture: true, allowNoLambda: true},
	"arrayfill":              {spelling: "arrayFill", result: hofFirstArray, bodyDomain: hofBodyDomainPredicate, minimumArrays: 1, maximumArrays: -1, lengthPolicy: hofLengthEqualAtExecution, allowOuterCapture: true},
	"arrayreversefill":       {spelling: "arrayReverseFill", result: hofFirstArray, bodyDomain: hofBodyDomainPredicate, minimumArrays: 1, maximumArrays: -1, lengthPolicy: hofLengthEqualAtExecution, allowOuterCapture: true},
	"arraysplit":             {spelling: "arraySplit", result: hofArrayOfFirstArray, bodyDomain: hofBodyDomainPredicate, minimumArrays: 1, maximumArrays: -1, lengthPolicy: hofLengthEqualAtExecution, allowOuterCapture: true},
	"arrayreversesplit":      {spelling: "arrayReverseSplit", result: hofArrayOfFirstArray, bodyDomain: hofBodyDomainPredicate, minimumArrays: 1, maximumArrays: -1, lengthPolicy: hofLengthEqualAtExecution, allowOuterCapture: true},
	"arrayfold": {spelling: "arrayFold", result: hofAccumulator, bodyDomain: hofBodyDomainAny, minimumArrays: 1, maximumArrays: -1, lengthPolicy: hofLengthEqualAtExecution, allowOuterCapture: true,
		accumulator: &higherOrderLambdaAccumulator{argumentPosition: -1, parameterPosition: 0}},
}

func validateHigherOrderLambdaSignature(name string, signature higherOrderLambdaSignature) error {
	if signature.spelling == "" || strings.ToLower(signature.spelling) != name {
		return fmt.Errorf("function %s has no exact higher-order spelling", name)
	}
	if signature.result < hofArrayOfBody || signature.result > hofAccumulator {
		return fmt.Errorf("function %s has an unknown higher-order result rule", name)
	}
	if signature.bodyDomain < hofBodyDomainAny || signature.bodyDomain > hofBodyDomainNumericReduction {
		return fmt.Errorf("function %s has an unknown lambda body domain", name)
	}
	if signature.minimumArrays < 1 ||
		(signature.maximumArrays != -1 && signature.maximumArrays < signature.minimumArrays) {
		return fmt.Errorf("function %s has an invalid linked Array range", name)
	}
	if signature.lengthPolicy != hofLengthEqualAtExecution {
		return fmt.Errorf("function %s has no measured array length policy", name)
	}
	if signature.accumulator != nil &&
		(signature.accumulator.argumentPosition < -1 || signature.accumulator.parameterPosition < 0) {
		return fmt.Errorf("function %s has an invalid accumulator link", name)
	}
	if signature.scalarArgument != nil &&
		(signature.scalarArgument.role != "limit" || signature.scalarArgument.positionAfterLambda < 0) {
		return fmt.Errorf("function %s has an invalid scalar argument link", name)
	}
	return nil
}

// isLambdaExpr reports the lambda form "param -> body" or
// "(p1, p2) -> body". The parser gives a lambda as a BinaryOperation
// with the "->" operator.
func isLambdaExpr(expression clickhouse.Expr) bool {
	_, _, ok := lambdaParts(expression)
	return ok
}

// lambdaParts splits a lambda into its parameter names and its body.
func lambdaParts(expression clickhouse.Expr) (params []string, body clickhouse.Expr, ok bool) {
	if columnExpr, isColumn := expression.(*clickhouse.ColumnExpr); isColumn {
		return lambdaParts(columnExpr.Expr)
	}
	operation, isBinary := expression.(*clickhouse.BinaryOperation)
	if !isBinary || string(operation.Operation) != "->" {
		return nil, nil, false
	}
	name := func(item clickhouse.Expr) (string, bool) {
		if columnExpr, isColumn := item.(*clickhouse.ColumnExpr); isColumn {
			item = columnExpr.Expr
		}
		identifier, isIdent := item.(*clickhouse.Ident)
		if !isIdent {
			return "", false
		}
		return identifier.Name, true
	}
	switch left := operation.LeftExpr.(type) {
	case *clickhouse.Ident:
		params = []string{left.Name}
	case *clickhouse.ParamExprList:
		if left.Items == nil || len(left.Items.Items) == 0 {
			return nil, nil, false
		}
		for _, item := range left.Items.Items {
			paramName, isName := name(item)
			if !isName {
				return nil, nil, false
			}
			params = append(params, paramName)
		}
	default:
		return nil, nil, false
	}
	return params, operation.RightExpr, true
}

// inferHigherOrderArrayType types the array functions that take a lambda.
//
// The lambda parameter binds to the element type of the matching data
// array, with LowCardinality removed and Nullable kept. Measured on
// ClickHouse 25.8.29.51 with real array columns:
// arrayMap(x -> toTypeName(x), arr_lc) gives ['String'] and
// arrayMap(x -> toTypeName(x), arr_n) gives ['Nullable(Int32)'].
//
// The parameter lives in a child scope, so it does not leak out of the
// lambda body.
func inferHigherOrderArrayType(name, displayName string, args []clickhouse.Expr, scope queryScope) (CHType, error) {
	signature, known := higherOrderArrayFunctions[name]
	if !known {
		return CHType{}, fmt.Errorf("function %s has no higher-order rule", displayName)
	}
	params, body, isLambda := lambdaParts(args[0])
	if !isLambda {
		return CHType{}, fmt.Errorf("function %s expects a lambda as the first argument", displayName)
	}
	seenParams := make(map[string]struct{}, len(params))
	for _, param := range params {
		if _, duplicate := seenParams[param]; duplicate {
			return CHType{}, fmt.Errorf("function %s lambda has more than one parameter named %s; %s", displayName, param, pinTypeHint)
		}
		seenParams[param] = struct{}{}
	}
	dataArgs := args[1:]
	if signature.scalarArgument != nil {
		position := signature.scalarArgument.positionAfterLambda
		if position < 0 || position >= len(dataArgs) {
			return CHType{}, fmt.Errorf("function %s needs its %s before the Array arguments", displayName, signature.scalarArgument.role)
		}
		if err := checkHigherOrderLimit(displayName, dataArgs[position], scope); err != nil {
			return CHType{}, err
		}
		dataArgs = append(dataArgs[:position:position], dataArgs[position+1:]...)
	}
	var accumulatorType CHType
	if signature.accumulator != nil {
		if len(dataArgs) < 2 {
			return CHType{}, fmt.Errorf("function %s needs an Array and an accumulator", displayName)
		}
		accumulatorIndex := signature.accumulator.argumentPosition
		if accumulatorIndex == -1 {
			accumulatorIndex = len(dataArgs) - 1
		}
		if accumulatorIndex < 0 || accumulatorIndex >= len(dataArgs) {
			return CHType{}, fmt.Errorf("function %s has an invalid accumulator argument position", displayName)
		}
		accumulatorArg := dataArgs[accumulatorIndex]
		var accumulatorErr error
		accumulatorType, accumulatorErr = inferExprType(accumulatorArg, scope)
		if accumulatorErr != nil {
			return CHType{}, fmt.Errorf("function %s accumulator: %w", displayName, accumulatorErr)
		}
		dataArgs = append(dataArgs[:accumulatorIndex:accumulatorIndex], dataArgs[accumulatorIndex+1:]...)
	}
	if len(dataArgs) < signature.minimumArrays ||
		(signature.maximumArrays >= 0 && len(dataArgs) > signature.maximumArrays) {
		return CHType{}, fmt.Errorf("function %s has %d array arguments outside its measured range", displayName, len(dataArgs))
	}
	wantParams := len(dataArgs)
	if signature.accumulator != nil {
		wantParams++
	}
	if len(params) != wantParams {
		return CHType{}, fmt.Errorf("function %s has %d lambda parameters and %d array arguments", displayName, len(params), len(dataArgs))
	}
	elementTypes := make([]CHType, 0, len(dataArgs))
	for _, dataArg := range dataArgs {
		arrayType, err := inferExprType(dataArg, scope)
		if err != nil {
			return CHType{}, fmt.Errorf("function %s array argument: %w", displayName, err)
		}
		// LowCardinality(Array(...)) does not occur, but an array may
		// be wrapped, so unwrap before the Array check.
		bare := unwrapLowCardinality(arrayType)
		if inner, ok := simpleAggregateWrapperInner(bare); ok {
			bare = unwrapLowCardinality(inner)
		}
		if !strings.EqualFold(bare.Name, "Array") || len(bare.Params) != 1 {
			return CHType{}, fmt.Errorf("function %s argument %s is %s, not an Array", displayName, clickhouse.Format(dataArg), arrayType.String())
		}
		elementTypes = append(elementTypes, unwrapLowCardinality(bare.Params[0]))
	}
	// ClickHouse lambda parameter names are case-sensitive. Keep exact
	// lookup in this child scope. A miss continues to the parent scope,
	// where captured scalar aliases keep their existing lookup rules.
	lambdaScope := queryScope{scalars: make(map[string]CHType, len(params)), exactScalarNames: true}
	if signature.allowOuterCapture {
		lambdaScope.parent = &scope
	}
	arrayParameterOffset := 0
	if signature.accumulator != nil {
		lambdaScope.scalars[params[signature.accumulator.parameterPosition]] = accumulatorType
		arrayParameterOffset = 1
	}
	for index := range elementTypes {
		lambdaScope.scalars[params[index+arrayParameterOffset]] = elementTypes[index]
	}
	bodyType, err := inferExprType(body, lambdaScope)
	if err != nil {
		return CHType{}, fmt.Errorf("function %s lambda body: %w", displayName, err)
	}
	// A predicate function refuses a body type outside the measured
	// predicate domain. Without this check chgen gave arrayCount the
	// type UInt32 for every body, and the server then answered Code: 43
	// at run time.
	if signature.bodyDomain == hofBodyDomainPredicate {
		if bodyErr := checkLambdaPredicateBody(displayName, bodyType); bodyErr != nil {
			return CHType{}, bodyErr
		}
	}
	if signature.bodyDomain == hofBodyDomainSummable {
		if _, bodyErr := arraySumResultType(displayName, bodyType); bodyErr != nil {
			return CHType{}, bodyErr
		}
	}
	if signature.bodyDomain == hofBodyDomainNumericReduction {
		if bodyErr := checkArrayNumericReductionBody(displayName, bodyType); bodyErr != nil {
			return CHType{}, bodyErr
		}
	}
	if signature.accumulator != nil && !foldAccumulatorTypesEqual(bodyType, accumulatorType) {
		return CHType{}, fmt.Errorf("function %s lambda body type %s does not match accumulator type %s", displayName, bodyType.String(), accumulatorType.String())
	}
	switch signature.result {
	case hofArrayOfBody:
		return CHType{Name: "Array", Params: []CHType{bodyType}}, nil
	case hofFirstArray:
		// The element type keeps Nullable and loses LowCardinality
		// (arraySort(x -> x, arr_lc) is Array(String),
		// arraySort(x -> x, arr_n) is Array(Nullable(Int32))).
		return CHType{Name: "Array", Params: []CHType{elementTypes[0]}}, nil
	case hofSumOfBody:
		return arraySumResultType(displayName, bodyType)
	case hofBody:
		// arrayMin and arrayMax give the element type back, Nullable
		// included (arrayMin(x -> x, arr_n) is Nullable(Int32)).
		return bodyType, nil
	case hofCount:
		return CHType{Name: "UInt32"}, nil
	case hofPredicate:
		// arrayExists and arrayAll give a plain predicate result, with
		// no Nullable even over a Nullable element type, and the
		// result is plain UInt8 even over Array(Bool)
		// (arrayExists(x -> x > 1, arr_n) is UInt8; arrayExists(x ->
		// x, arr_bool) is UInt8; arrayAll(x -> x, arr_bool) is
		// UInt8). This is the same predicate family as the comparison
		// operators, and it takes the same UInt8 base.
		return CHType{Name: "UInt8"}, nil
	case hofFirstElement:
		return elementTypes[0], nil
	case hofNullableFirstElement:
		if !canBeInsideNullable(elementTypes[0]) && elementTypes[0].normalizedName() != "nullable" {
			return CHType{}, fmt.Errorf("function %s cannot put the element type %s inside Nullable", displayName, elementTypes[0].String())
		}
		return wrapNullable(elementTypes[0]), nil
	case hofFloat64:
		return CHType{Name: "Float64"}, nil
	case hofArrayOfSum:
		sumType, sumErr := arraySumResultType(displayName, bodyType)
		if sumErr != nil {
			return CHType{}, sumErr
		}
		return CHType{Name: "Array", Params: []CHType{sumType}}, nil
	case hofArrayOfFirstArray:
		return CHType{Name: "Array", Params: []CHType{{Name: "Array", Params: []CHType{elementTypes[0]}}}}, nil
	case hofAccumulator:
		return accumulatorType, nil
	default:
		return CHType{}, fmt.Errorf("function %s has an unknown higher-order result rule", displayName)
	}
}

func inferHigherOrderArrayWithoutLambda(name, displayName string, args []clickhouse.Expr, scope queryScope) (CHType, error) {
	signature, known := higherOrderArrayFunctions[name]
	if !known || !signature.allowNoLambda {
		return CHType{}, fmt.Errorf("function %s has no measured no-lambda form", displayName)
	}
	dataArgs := args
	if signature.scalarArgument != nil {
		position := signature.scalarArgument.positionAfterLambda
		if position < 0 || position >= len(dataArgs) {
			return CHType{}, fmt.Errorf("function %s needs its %s before the Array argument", displayName, signature.scalarArgument.role)
		}
		if err := checkHigherOrderLimit(displayName, dataArgs[position], scope); err != nil {
			return CHType{}, err
		}
		dataArgs = append(dataArgs[:position:position], dataArgs[position+1:]...)
	}
	if len(dataArgs) != 1 {
		return CHType{}, fmt.Errorf("function %s without a lambda needs exactly one Array argument", displayName)
	}
	arrayType, err := inferExprType(dataArgs[0], scope)
	if err != nil {
		return CHType{}, fmt.Errorf("function %s array argument: %w", displayName, err)
	}
	bare := unwrapLowCardinality(arrayType)
	if inner, ok := simpleAggregateWrapperInner(bare); ok {
		bare = unwrapLowCardinality(inner)
	}
	if !strings.EqualFold(bare.Name, "Array") || len(bare.Params) != 1 {
		return CHType{}, fmt.Errorf("function %s argument %s is %s, not an Array", displayName, clickhouse.Format(dataArgs[0]), arrayType.String())
	}
	bodyType := unwrapLowCardinality(bare.Params[0])
	if signature.bodyDomain == hofBodyDomainNumericReduction {
		if bodyErr := checkArrayNumericReductionBody(displayName, bodyType); bodyErr != nil {
			return CHType{}, bodyErr
		}
	} else if signature.bodyDomain == hofBodyDomainSummable {
		if _, bodyErr := arraySumResultType(displayName, bodyType); bodyErr != nil {
			return CHType{}, bodyErr
		}
	}
	switch signature.result {
	case hofFloat64:
		return CHType{Name: "Float64"}, nil
	case hofArrayOfSum:
		sumType, sumErr := arraySumResultType(displayName, bodyType)
		if sumErr != nil {
			return CHType{}, sumErr
		}
		return CHType{Name: "Array", Params: []CHType{sumType}}, nil
	case hofFirstArray:
		return CHType{Name: "Array", Params: []CHType{bodyType}}, nil
	default:
		return CHType{}, fmt.Errorf("function %s has an invalid no-lambda result rule", displayName)
	}
}

func checkHigherOrderLimit(displayName string, expression clickhouse.Expr, scope queryScope) error {
	limitType, err := inferExprType(expression, scope)
	if err != nil {
		return fmt.Errorf("function %s limit: %w", displayName, err)
	}
	base, nullable, _ := splitCHWrappers(limitType)
	if nullable || !indexBaseType(base) {
		return fmt.Errorf("function %s needs a non-Nullable integer or Bool limit, not %s", displayName, limitType.String())
	}
	return nil
}

func foldAccumulatorTypesEqual(bodyType, accumulatorType CHType) bool {
	if bodyType.String() == accumulatorType.String() {
		return true
	}
	if len(bodyType.Params) != 0 || len(accumulatorType.Params) != 0 ||
		len(bodyType.LiteralParams) != 0 || len(accumulatorType.LiteralParams) != 0 {
		return false
	}
	bodyName := bodyType.normalizedName()
	accumulatorName := accumulatorType.normalizedName()
	return (bodyName == "bool" || bodyName == "boolean" || bodyName == "uint8") &&
		(accumulatorName == "bool" || accumulatorName == "boolean" || accumulatorName == "uint8")
}

func checkArrayNumericReductionBody(displayName string, bodyType CHType) error {
	base, nullable, _ := splitCHWrappers(bodyType)
	if nullable {
		return fmt.Errorf("function %s cannot aggregate the Nullable type %s", displayName, bodyType.String())
	}
	lower := base.normalizedName()
	if numberBaseType(base) || lower == "bool" || lower == "boolean" ||
		lower == "enum8" || lower == "enum16" {
		return nil
	}
	return fmt.Errorf("function %s cannot aggregate the type %s", displayName, bodyType.String())
}

// arraySumResultType promotes the lambda body type the way the array
// aggregation of ClickHouse does. Measured on ClickHouse 25.8.29.51:
// arraySum(x -> x, arr_u8) is UInt64, arraySum(x -> x * 2, arr_i) is
// Int64, arraySum(x -> x, arr_f) is Float64 and arraySum(x -> x, arr_d)
// is Decimal(38, 4). A wide integer keeps its width: UInt128 gives UInt128
// and Int256 gives Int256. Decimal256 keeps its 76-digit storage width.
// A Nullable element type is an error on ClickHouse itself
// (ILLEGAL_TYPE_OF_ARGUMENT), so it stays a refusal here.
func arraySumResultType(displayName string, bodyType CHType) (CHType, error) {
	base, nullable, _ := splitCHWrappers(bodyType)
	if nullable {
		return CHType{}, fmt.Errorf("function %s cannot aggregate the Nullable type %s", displayName, bodyType.String())
	}
	if precision, scale, isDecimal := decimalPrecisionScale(base); isDecimal {
		resultPrecision := 38
		if precision > resultPrecision {
			resultPrecision = precision
		}
		return CHType{Name: "Decimal", LiteralParams: []string{strconv.Itoa(resultPrecision), strconv.Itoa(scale)}}, nil
	}
	lower := strings.ToLower(base.Name)
	switch {
	case strings.HasPrefix(lower, "float"):
		return CHType{Name: "Float64"}, nil
	case lower == "uint128" || lower == "uint256" || lower == "int128" || lower == "int256":
		return base, nil
	case lower == "bool" || lower == "boolean", strings.HasPrefix(lower, "uint"):
		return CHType{Name: "UInt64"}, nil
	case strings.HasPrefix(lower, "int"):
		return CHType{Name: "Int64"}, nil
	}
	return CHType{}, fmt.Errorf("function %s cannot aggregate the type %s", displayName, bodyType.String())
}
