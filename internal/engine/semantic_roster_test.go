package engine

import (
	"os/exec"
	"testing"
)

func TestGeneratedFunctionAuditRosterHasNoDrift(t *testing.T) {
	command := exec.Command("go", "run", "../tooling/cmd/gensemantics", "-root", ".", "-check")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("check the generated semantic roster: %v\n%s", err, output)
	}

	for _, name := range generatedFunctionAuditRoster {
		spec, present := functionSemanticSpecs[name]
		if !present {
			t.Fatalf("generated audit roster names missing function %s", name)
		}
		if got, want := generatedFunctionSemanticFamilies[name], functionSemanticFamilyName(spec.family); got != want {
			t.Fatalf("generated family for %s is %q, want %q", name, got, want)
		}
	}
	if len(generatedFunctionSemanticFamilies) != len(generatedFunctionAuditRoster) {
		t.Fatalf("generated family roster has %d functions, want %d", len(generatedFunctionSemanticFamilies), len(generatedFunctionAuditRoster))
	}
	for _, token := range generatedOperatorAuditRoster {
		found := false
		for _, spec := range operatorSemanticSpecs {
			if spec.token == token {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("generated audit roster names missing operator %s", token)
		}
	}
}

func TestSemanticSpecificationRejectsEveryUnknownState(t *testing.T) {
	valid := functionSemanticSpecs["count"]
	tests := []struct {
		name   string
		mutate func(*functionSpec)
	}{
		{name: "semantic family", mutate: func(spec *functionSpec) { spec.family = semanticFamilyUnknown }},
		{name: "wrapper class", mutate: func(spec *functionSpec) { spec.class = wrapperClassUnset }},
		{name: "domain", mutate: func(spec *functionSpec) { spec.domainMode = argumentDomainUnknown }},
		{name: "parameter policy", mutate: func(spec *functionSpec) { spec.parameterPolicy = parameterResultUnknown }},
		{name: "evidence", mutate: func(spec *functionSpec) { spec.evidence = "" }},
		{name: "call shape", mutate: func(spec *functionSpec) { spec.gen = nil }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mutated := valid
			test.mutate(&mutated)
			if _, err := validateAndBuildFunctionSpec("count", mutated); err == nil {
				t.Fatal("an unknown semantic state did not cause a refusal")
			}
		})
	}
}

func TestUnknownDomainRefusesAtInference(t *testing.T) {
	original := functionRegistry["count"]
	mutated := original
	mutated.domainMode = argumentDomainUnknown
	functionRegistry["count"] = mutated
	t.Cleanup(func() { functionRegistry["count"] = original })

	schema, err := schemaFromDDLErr(t, "CREATE TABLE t (i Int32) ENGINE = Memory")
	if err != nil {
		t.Fatalf("parse the probe schema: %v", err)
	}
	if _, err := inferSelectItemCHType(t, schema, "SELECT count(i) AS a FROM t"); err == nil {
		t.Fatal("an unknown domain did not cause a refusal")
	}
}

func TestOperatorSpecificationRejectsEveryUnknownState(t *testing.T) {
	valid := operatorSemanticSpecs[0]
	tests := []struct {
		name   string
		mutate func(*operatorSpec)
	}{
		{name: "family", mutate: func(spec *operatorSpec) { spec.family = opFamilyUnknown }},
		{name: "arity", mutate: func(spec *operatorSpec) { spec.arity = 0 }},
		{name: "constant positions", mutate: func(spec *operatorSpec) { spec.constants = operatorConstantsUnknown }},
		{name: "wrapper class", mutate: func(spec *operatorSpec) { spec.class = wrapperClassUnset }},
		{name: "domain", mutate: func(spec *operatorSpec) { spec.domainMode = argumentDomainUnknown }},
		{name: "parameter policy", mutate: func(spec *operatorSpec) { spec.parameterPolicy = parameterResultUnknown }},
		{name: "evidence", mutate: func(spec *operatorSpec) { spec.evidence = "" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mutated := valid
			test.mutate(&mutated)
			defer func() {
				if recover() == nil {
					t.Fatal("an unknown operator state did not cause a refusal")
				}
			}()
			mustBuildOperatorCatalog([]operatorSpec{mutated})
		})
	}
}
