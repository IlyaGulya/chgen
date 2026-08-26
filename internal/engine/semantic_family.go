package engine

import (
	"fmt"
	"strings"
)

type functionSemanticFamily uint8

const (
	semanticFamilyUnknown functionSemanticFamily = iota
	semanticFamilyFixedResult
	semanticFamilyFirstArgument
	semanticFamilyCommonSupertype
	semanticFamilyPredicate
	semanticFamilyConversion
	semanticFamilyAggregateFixedResult
	semanticFamilyAggregateFixedResultParametric
	semanticFamilyAggregateFirstArgument
	semanticFamilyAggregateDedicated
	semanticFamilyAggregateDedicatedParametric
	semanticFamilyWindowFixedResult
	semanticFamilyWindowFirstArgument
	semanticFamilyLambdaPredicate
	semanticFamilyLambdaFirstArgument
	semanticFamilyLambdaDedicated
	semanticFamilyParametric
	semanticFamilyContextDependent
	semanticFamilyDedicated
)

type functionResultPolicy uint8

const (
	resultPolicyUnknown functionResultPolicy = iota
	resultPolicyFixedMember
	resultPolicyFirstArgument
	resultPolicyCommonSupertype
	resultPolicyPredicate
	resultPolicyConversion
	resultPolicyDedicated
	resultPolicySpecialRoute
)

type functionProbePolicy uint8

const (
	probePolicyUnknown functionProbePolicy = iota
	probePolicyScalarSignature
	probePolicyAggregateSignature
	probePolicyWindowSignature
	probePolicyLambdaSignature
	probePolicyParametricSignature
	probePolicyContextSignature
)

type functionProbeAxis uint8

const (
	probeAxisUnknown functionProbeAxis = iota
	probeAxisArity
	probeAxisArgumentDomain
	probeAxisResultType
	probeAxisPlacement
	probeAxisLambda
	probeAxisParameters
)

type functionSemanticFamilyDefinition struct {
	name         string
	resultPolicy functionResultPolicy
	probePolicy  functionProbePolicy
	probeAxes    []functionProbeAxis
}

var functionSemanticFamilies = map[functionSemanticFamily]functionSemanticFamilyDefinition{
	semanticFamilyFixedResult:          familyDefinition("fixed-result-scalar", resultPolicyFixedMember, probePolicyScalarSignature),
	semanticFamilyFirstArgument:        familyDefinition("first-argument-scalar", resultPolicyFirstArgument, probePolicyScalarSignature),
	semanticFamilyCommonSupertype:      familyDefinition("common-supertype-scalar", resultPolicyCommonSupertype, probePolicyScalarSignature),
	semanticFamilyPredicate:            familyDefinition("predicate-scalar", resultPolicyPredicate, probePolicyScalarSignature),
	semanticFamilyConversion:           familyDefinition("conversion-scalar", resultPolicyConversion, probePolicyScalarSignature),
	semanticFamilyAggregateFixedResult: familyDefinition("fixed-result-aggregate", resultPolicyFixedMember, probePolicyAggregateSignature),
	semanticFamilyAggregateFixedResultParametric: familyDefinitionWithAxes("fixed-result-parametric-aggregate", resultPolicyFixedMember,
		probePolicyAggregateSignature, probeAxisParameters),
	semanticFamilyAggregateFirstArgument: familyDefinition("first-argument-aggregate", resultPolicyFirstArgument, probePolicyAggregateSignature),
	semanticFamilyAggregateDedicated:     familyDefinition("dedicated-aggregate", resultPolicyDedicated, probePolicyAggregateSignature),
	semanticFamilyAggregateDedicatedParametric: familyDefinitionWithAxes("dedicated-parametric-aggregate", resultPolicyDedicated,
		probePolicyAggregateSignature, probeAxisParameters),
	semanticFamilyWindowFixedResult:   familyDefinition("fixed-result-window", resultPolicyFixedMember, probePolicyWindowSignature),
	semanticFamilyWindowFirstArgument: familyDefinition("first-argument-window", resultPolicyFirstArgument, probePolicyWindowSignature),
	semanticFamilyLambdaPredicate:     familyDefinition("predicate-lambda", resultPolicyPredicate, probePolicyLambdaSignature),
	semanticFamilyLambdaFirstArgument: familyDefinition("first-argument-lambda", resultPolicyFirstArgument, probePolicyLambdaSignature),
	semanticFamilyLambdaDedicated:     familyDefinition("dedicated-lambda", resultPolicySpecialRoute, probePolicyLambdaSignature),
	semanticFamilyParametric:          familyDefinition("parametric-scalar", resultPolicyDedicated, probePolicyParametricSignature),
	semanticFamilyContextDependent:    familyDefinition("context-dependent-scalar", resultPolicySpecialRoute, probePolicyContextSignature),
	semanticFamilyDedicated:           familyDefinition("dedicated-scalar", resultPolicyDedicated, probePolicyScalarSignature),
}

func familyDefinition(name string, result functionResultPolicy, probe functionProbePolicy) functionSemanticFamilyDefinition {
	axes := []functionProbeAxis{probeAxisArity, probeAxisArgumentDomain, probeAxisResultType}
	switch probe {
	case probePolicyAggregateSignature, probePolicyWindowSignature, probePolicyContextSignature:
		axes = append(axes, probeAxisPlacement)
	case probePolicyLambdaSignature:
		axes = append(axes, probeAxisLambda)
	case probePolicyParametricSignature:
		axes = append(axes, probeAxisParameters)
	}
	return functionSemanticFamilyDefinition{name: name, resultPolicy: result, probePolicy: probe, probeAxes: axes}
}

func familyDefinitionWithAxes(name string, result functionResultPolicy, probe functionProbePolicy, extra ...functionProbeAxis) functionSemanticFamilyDefinition {
	definition := familyDefinition(name, result, probe)
	definition.probeAxes = append(definition.probeAxes, extra...)
	return definition
}

func functionSemanticFamilyName(family functionSemanticFamily) string {
	return functionSemanticFamilies[family].name
}

func applyFunctionSemanticFamily(spec functionSpec) functionSpec {
	definition := functionSemanticFamilies[spec.family]
	switch definition.resultPolicy {
	case resultPolicyPredicate:
		spec.rule = fixedFunctionType("UInt8")
		spec.strategy = argsIndependent
		spec.resultMode = resultRuleGeneric
	case resultPolicyFirstArgument:
		spec.rule = firstFunctionArgument
		spec.strategy = argsFirstOnly
		spec.resultMode = resultRuleGeneric
	case resultPolicyFixedMember:
		spec.strategy = argsIndependent
		spec.resultMode = resultRuleGeneric
	case resultPolicyCommonSupertype:
		spec.strategy = argsGeneric
		spec.resultMode = resultRuleGeneric
		spec.parameterPolicy = parameterResultLattice
	case resultPolicySpecialRoute:
		spec.resultMode = resultRuleSpecialRoute
		spec.rule = nil
	}
	if spec.gen != nil {
		copy := *spec.gen
		switch definition.probePolicy {
		case probePolicyAggregateSignature:
			copy.place = placementAggregate
		case probePolicyWindowSignature:
			copy.place = placementWindow
		case probePolicyLambdaSignature, probePolicyParametricSignature, probePolicyContextSignature, probePolicyScalarSignature:
			copy.place = placementScalar
		}
		spec.gen = &copy
	}
	spec.derivedFamilyIdentity = spec.family
	spec.derivedFamilyResult = definition.resultPolicy
	spec.derivedFamilyProbe = definition.probePolicy
	return spec
}

func validateFunctionSemanticFamily(name string, spec functionSpec) error {
	definition, found := functionSemanticFamilies[spec.family]
	if !found || definition.name == "" || definition.resultPolicy == resultPolicyUnknown ||
		definition.probePolicy == probePolicyUnknown || len(definition.probeAxes) == 0 {
		return fmt.Errorf("function %s has no complete semantic family", name)
	}
	if spec.derivedFamilyIdentity != spec.family || spec.derivedFamilyResult != definition.resultPolicy ||
		spec.derivedFamilyProbe != definition.probePolicy {
		return fmt.Errorf("function %s has a family policy seal mismatch", name)
	}
	if spec.gen == nil {
		return fmt.Errorf("function %s semantic family %s has no probe call shape", name, definition.name)
	}
	if err := validateFamilyResultPolicy(name, definition, spec); err != nil {
		return err
	}
	return validateFamilyProbePolicy(name, definition, spec)
}

func validateFamilyResultPolicy(name string, definition functionSemanticFamilyDefinition, spec functionSpec) error {
	switch definition.resultPolicy {
	case resultPolicyFixedMember:
		if spec.resultMode != resultRuleGeneric || spec.rule == nil || spec.strategy != argsIndependent {
			return fmt.Errorf("function %s does not match fixed-member result policy", name)
		}
		result, err := spec.rule(nil)
		if err != nil || result.normalizedName() == "uint8" || strings.HasPrefix(strings.ToLower(spec.gen.spelling), "to") {
			return fmt.Errorf("function %s does not match fixed-member identity", name)
		}
	case resultPolicyPredicate:
		result, err := spec.rule(nil)
		if err != nil || result.String() != "UInt8" || spec.strategy != argsIndependent {
			return fmt.Errorf("function %s does not match predicate result policy", name)
		}
	case resultPolicyConversion:
		if spec.resultMode != resultRuleGeneric || spec.rule == nil ||
			(spec.strategy != argsIndependent && spec.strategy != argsGeneric) ||
			!strings.HasPrefix(strings.ToLower(spec.gen.spelling), "to") {
			return fmt.Errorf("function %s does not match conversion result policy", name)
		}
	case resultPolicyFirstArgument:
		if spec.resultMode != resultRuleGeneric || spec.rule == nil || spec.strategy != argsFirstOnly {
			return fmt.Errorf("function %s does not match first-argument result policy", name)
		}
	case resultPolicyCommonSupertype:
		if spec.resultMode != resultRuleGeneric || spec.rule == nil || spec.parameterPolicy != parameterResultLattice {
			return fmt.Errorf("function %s does not match common-supertype result policy", name)
		}
	case resultPolicyDedicated:
		if spec.resultMode != resultRuleGeneric || spec.rule == nil {
			return fmt.Errorf("function %s has no dedicated result policy", name)
		}
	case resultPolicySpecialRoute:
		if spec.resultMode != resultRuleSpecialRoute || spec.rule != nil {
			return fmt.Errorf("function %s does not match special-route result policy", name)
		}
	default:
		return fmt.Errorf("function %s has an unknown result policy", name)
	}
	return nil
}

func validateFamilyProbePolicy(name string, definition functionSemanticFamilyDefinition, spec functionSpec) error {
	switch definition.probePolicy {
	case probePolicyScalarSignature, probePolicyParametricSignature, probePolicyContextSignature:
		if spec.gen.place != placementScalar {
			return fmt.Errorf("function %s does not match scalar probe policy", name)
		}
	case probePolicyAggregateSignature:
		if spec.gen.place != placementAggregate {
			return fmt.Errorf("function %s does not match aggregate probe policy", name)
		}
	case probePolicyWindowSignature:
		if spec.gen.place != placementWindow {
			return fmt.Errorf("function %s does not match window probe policy", name)
		}
	case probePolicyLambdaSignature:
		if !genSpecHasArgumentSort(*spec.gen, argSortLambda) && !signatureHasArgumentSort(spec.signature, argSortLambda) {
			return fmt.Errorf("function %s does not match lambda probe policy", name)
		}
	default:
		return fmt.Errorf("function %s has an unknown probe policy", name)
	}
	return nil
}

func functionProbeAxisName(axis functionProbeAxis) string {
	switch axis {
	case probeAxisArity:
		return "arity"
	case probeAxisArgumentDomain:
		return "argument_domain"
	case probeAxisResultType:
		return "result_type"
	case probeAxisPlacement:
		return "placement"
	case probeAxisLambda:
		return "lambda"
	case probeAxisParameters:
		return "parameters"
	default:
		return "unknown"
	}
}

func functionProbePolicyName(policy functionProbePolicy) string {
	switch policy {
	case probePolicyScalarSignature:
		return "scalar-signature"
	case probePolicyAggregateSignature:
		return "aggregate-signature"
	case probePolicyWindowSignature:
		return "window-signature"
	case probePolicyLambdaSignature:
		return "lambda-signature"
	case probePolicyParametricSignature:
		return "parametric-signature"
	case probePolicyContextSignature:
		return "context-signature"
	default:
		return "unknown"
	}
}

func functionProbeAxisNames(axes []functionProbeAxis) []string {
	result := make([]string, len(axes))
	for index, axis := range axes {
		result[index] = functionProbeAxisName(axis)
	}
	return result
}

func signatureHasArgumentSort(signature *functionSignature, want argSort) bool {
	if signature == nil {
		return false
	}
	for _, forms := range [][]signatureForm{signature.forms, signature.parameterForms} {
		for _, form := range forms {
			for _, sorts := range [][]argSort{form.prefix, form.repeat, form.suffix} {
				for _, sort := range sorts {
					if sort == want {
						return true
					}
				}
			}
		}
	}
	return false
}

func genSpecHasArgumentSort(spec genSpec, want argSort) bool {
	for _, sort := range spec.argSorts {
		if sort == want {
			return true
		}
	}
	return false
}
