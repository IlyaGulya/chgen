package engine

import (
	"errors"
	"fmt"
	"slices"
)

// callEvaluationPlan describes argument roles, not another type algorithm.
// Signature validation and special expression routes share the same CallContext.
type callEvaluationPlan struct {
	ruleArguments       int
	validationArguments int
	domainBefore        []int
	domainAfter         []int
	firstValue          bool
	allowPlaceholder    bool
	skipStars           bool
	checkParameters     bool
	compareBefore       bool
	compareAfter        bool
}

func functionCallPlan(name string, spec functionSpec, arity int) callEvaluationPlan {
	plan := callEvaluationPlan{
		ruleArguments: arity, validationArguments: arity,
		checkParameters: true, compareBefore: true,
	}
	switch spec.strategy {
	case argsIndependent:
		plan.ruleArguments = 0
		plan.domainBefore = spec.domainArgs
		plan.allowPlaceholder, plan.skipStars = true, true
		plan.checkParameters, plan.compareBefore, plan.compareAfter = false, false, true
	case argsFirstOnly:
		plan.ruleArguments = 1
		plan.validationArguments = aggregateDataArgCount(name, arity)
		if spec.class == wrapperOpaque {
			plan.validationArguments = 1
		}
		plan.firstValue, plan.allowPlaceholder = true, true
		plan.compareBefore = false
		if slices.Contains(spec.domainArgs, 0) {
			plan.domainBefore = []int{0}
		}
		plan.domainAfter = spec.domainArgs
	case argsGeneric:
		// Generic rules historically validate their first value before the
		// rule. Other argument roles are checked by the measured signature.
		if arity > 0 {
			plan.domainBefore = []int{0}
		}
	}
	return plan
}

func evaluateFunctionCall(name, displayName string, spec functionSpec, call *CallContext) (CHType, error) {
	plan := functionCallPlan(name, spec, len(call.args))
	if plan.ruleArguments > len(call.args) {
		return CHType{}, fmt.Errorf("function %s has no arguments", displayName)
	}
	argTypes, _, err := call.evaluationArguments(displayName, 0, plan.ruleArguments, false, plan)
	if err != nil {
		return CHType{}, err
	}
	if err := call.validateEvaluationDomain(displayName, spec, plan.domainBefore, 0); err != nil {
		return CHType{}, err
	}
	if plan.compareBefore {
		if err := call.validateEvaluationPairs(name, displayName, spec, argTypes); err != nil {
			return CHType{}, err
		}
	}
	var ruleTypes []CHType
	if len(argTypes) != 0 {
		ruleTypes = make([]CHType, len(argTypes))
	}
	var firstStack wrapperStack
	for index, value := range argTypes {
		ruleTypes[index], firstStack = evaluationRuleArgument(name, spec, plan, value)
	}
	// One result rule and one parameter gate serve every ordinary call.
	result, err := spec.rule(ruleTypes)
	if err != nil {
		return CHType{}, err
	}
	if plan.checkParameters {
		result, err = applyParameterVerdict(name, result)
		if err != nil {
			return CHType{}, err
		}
	}
	extraTypes, placeholder, err := call.evaluationArguments(displayName, plan.ruleArguments, plan.validationArguments, plan.allowPlaceholder, plan)
	if err != nil {
		return CHType{}, err
	}
	argTypes = append(argTypes, extraTypes...)
	if err := call.validateEvaluationDomain(displayName, spec, plan.domainAfter, plan.ruleArguments); err != nil {
		return CHType{}, err
	}
	if plan.compareAfter && !placeholder {
		if err := call.validateEvaluationPairs(name, displayName, spec, argTypes); err != nil {
			return CHType{}, err
		}
	}
	if spec.class == wrapperOpaque || (placeholder && !plan.firstValue) {
		return result, nil
	}
	var transport wrapperTransport
	var wrappers wrapperCall
	if plan.firstValue {
		// Only later DATA arguments contribute outer Nullable; an If
		// condition and a marker's inner Nullable have different roles.
		for _, value := range extraTypes {
			_, nullable, _ := splitCHWrappers(value)
			firstStack.nullable = firstStack.nullable || nullable
			firstStack.outerNullable = firstStack.outerNullable || nullable
		}
		transport = transportForFunction(name, spec.class)
		if !hasFunctionTransportOverride(name) {
			transport = transport.withResolvedLowCardinality(firstStack.lowCardinality)
		}
		wrappers = wrapperCall{base: ruleTypes[0], stacks: []wrapperStack{firstStack}, argCount: len(call.args)}
	} else {
		transport, wrappers = functionWrapperTransport(spec.class, name, call.args, argTypes, call.scope)
	}
	return applyWrapperTransport(result, transport, wrappers), nil
}

// evaluationArguments uses the cache populated by signature validation. A
// placeholder is not a failed column lookup: its role determines whether it
// can be deferred. Unknown expressions still fail with the original path.
func (call *CallContext) evaluationArguments(displayName string, start, end int, allowPlaceholder bool, plan callEvaluationPlan) ([]CHType, bool, error) {
	values := make([]CHType, 0, end-start)
	placeholder := false
	for index := start; index < end; index++ {
		if plan.skipStars && isStarArgument(call.args[index]) {
			continue
		}
		value, err := call.argumentType(index)
		if err != nil {
			if allowPlaceholder && errors.Is(err, errPlaceholderResultType) {
				placeholder = true
				values = append(values, CHType{})
				continue
			}
			role := "argument"
			if plan.firstValue && index == 0 {
				role = "first argument"
			}
			return nil, false, fmt.Errorf("function %s %s: %w", displayName, role, err)
		}
		values = append(values, value)
	}
	return values, placeholder, nil
}

func (call *CallContext) validateEvaluationDomain(displayName string, spec functionSpec, indexes []int, start int) error {
	if spec.domainMode != argumentDomainRestricted || spec.domain == nil {
		return nil
	}
	for _, index := range indexes {
		if index < start {
			continue
		}
		if err := checkArgumentDomainAt(displayName, *spec.domain, []int{index}, len(call.args), func(index int) (CHType, bool) {
			value, err := call.argumentType(index)
			if err != nil {
				return CHType{}, false
			}
			base, _, _ := splitCHWrappers(value)
			if inner, marked := simpleAggregateWrapperInner(base); marked {
				base, _, _ = splitCHWrappers(inner)
			}
			return base, true
		}); err != nil {
			return err
		}
		// Portability constrains the full input, not just its bare domain.
		value, err := call.argumentType(index)
		if err == nil && len(spec.nonPortableInputs) != 0 && !portableFunctionInput(value, spec.nonPortableInputs) {
			return fmt.Errorf("function %s has a build-dependent result for %s; explicitly CAST the input AS Float64 (preserving nullability), or use server-assisted generation", displayName, value.String())
		}
	}
	return nil
}

func (call *CallContext) validateEvaluationPairs(name, displayName string, spec functionSpec, values []CHType) error {
	if err := checkHasElementPair(displayName, name, call.args, values, call.scope); err != nil {
		return err
	}
	if spec.facts&functionFactComparesArgPair != 0 && len(call.args) == 2 && len(values) == 2 {
		return checkComparableOperandExprs("function "+displayName, call.args[0], call.args[1], values[0], values[1], call.scope)
	}
	return nil
}

func evaluationRuleArgument(name string, spec functionSpec, plan callEvaluationPlan, value CHType) (CHType, wrapperStack) {
	if spec.class == wrapperOpaque {
		return value, wrapperStack{}
	}
	if plan.firstValue {
		base, nullable, lowCardinality := splitCHWrappers(value)
		_, preserves := valuePreservingDomainFunctions[name]
		if spec.domainMode == argumentDomainRestricted && !preserves && !hasFunctionTransportOverride(name) {
			if inner, marked := simpleAggregateWrapperInner(base); marked {
				var innerNullable, innerLowCardinality bool
				base, innerNullable, innerLowCardinality = splitCHWrappers(inner)
				nullable = nullable || innerNullable
				lowCardinality = lowCardinality || innerLowCardinality
			}
		}
		base, stack := splitWrapperStack(base)
		stack.lowCardinality = stack.lowCardinality || lowCardinality
		stack.nullable = stack.nullable || nullable
		stack.outerNullable = stack.outerNullable || nullable
		return base, stack
	}
	base, stack := splitWrapperStack(value)
	if wrapperValuePreservingRules[name] && stack.simpleAggregate != nil {
		// nullIf owns its marker INSIDE the Nullable returned by the rule.
		base, _, _ = splitCHWrappers(value)
	}
	return base, stack
}
