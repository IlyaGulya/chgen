package engine

import "testing"

func TestEveryFunctionDerivesSemanticsFromAKnownFamily(t *testing.T) {
	for name, spec := range functionRegistry {
		if err := validateFunctionSemanticFamily(name, spec); err != nil {
			t.Errorf("function %s: %v", name, err)
		}
	}
}

func TestFunctionSemanticFamilyMutationFailsClosed(t *testing.T) {
	spec := functionRegistry["equals"]
	spec.family = functionSemanticFamily(255)
	if err := validateFunctionSemanticFamily("equals", spec); err == nil {
		t.Fatal("an unknown function semantic family did not cause a refusal")
	}
}

func TestFunctionSemanticFamilyValidMutationFailsClosed(t *testing.T) {
	spec := functionRegistry["equals"]
	spec.family = semanticFamilyFixedResult
	if err := validateFunctionSemanticFamily("equals", spec); err == nil {
		t.Fatal("a predicate changed to the fixed-result family did not cause a refusal")
	}
}

func TestEveryValidFunctionFamilyMutationFailsClosed(t *testing.T) {
	for name, spec := range functionRegistry {
		for family := range functionSemanticFamilies {
			if family == spec.family {
				continue
			}
			mutated := spec
			mutated.family = family
			if err := validateFunctionSemanticFamily(name, mutated); err == nil {
				t.Errorf("function %s accepted family mutation from %s to %s", name,
					functionSemanticFamilyName(spec.family), functionSemanticFamilyName(family))
			}
		}
	}
}

func TestFunctionFamilyPolicySealMutationsFailClosed(t *testing.T) {
	original := functionRegistry["equals"]
	tests := []struct {
		name   string
		mutate func(*functionSpec)
	}{
		{name: "identity", mutate: func(spec *functionSpec) { spec.derivedFamilyIdentity = semanticFamilyFixedResult }},
		{name: "result", mutate: func(spec *functionSpec) { spec.derivedFamilyResult = resultPolicyFirstArgument }},
		{name: "probe", mutate: func(spec *functionSpec) { spec.derivedFamilyProbe = probePolicyWindowSignature }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mutated := original
			test.mutate(&mutated)
			if err := validateFunctionSemanticFamily("equals", mutated); err == nil {
				t.Fatal("a derived family policy mutation did not cause a refusal")
			}
		})
	}
}

func TestFunctionSemanticFamilyRecipeMutationFailsClosed(t *testing.T) {
	saved := functionSemanticFamilies[semanticFamilyPredicate]
	mutated := saved
	mutated.probePolicy = probePolicyUnknown
	functionSemanticFamilies[semanticFamilyPredicate] = mutated
	t.Cleanup(func() { functionSemanticFamilies[semanticFamilyPredicate] = saved })
	if err := validateFunctionSemanticFamily("equals", functionRegistry["equals"]); err == nil {
		t.Fatal("a semantic family without a probe recipe did not cause a refusal")
	}
}

func TestPredicateFamilyDerivesItsResultRule(t *testing.T) {
	spec := functionSemanticSpecs["equals"]
	spec.rule = fixedFunctionType("String")
	built, err := validateAndBuildFunctionSpec("equals", spec)
	if err != nil {
		t.Fatal(err)
	}
	result, err := built.rule(nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.String() != "UInt8" {
		t.Fatalf("predicate family derived %s, want UInt8", result.String())
	}
}

func TestAggregateFamilyDerivesItsProbePlacement(t *testing.T) {
	spec := functionSemanticSpecs["sum"]
	copy := *spec.gen
	copy.place = placementScalar
	spec.gen = &copy
	built, err := validateAndBuildFunctionSpec("sum", spec)
	if err != nil {
		t.Fatal(err)
	}
	if built.gen.place != placementAggregate {
		t.Fatal("aggregate family did not derive aggregate probe placement")
	}
}
