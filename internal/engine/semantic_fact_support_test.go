package engine

import (
	"fmt"
	"sort"
	"strings"
)

// functionFactEvidence links one spec fact to its measured function set.
// A measured function can be absent from the registry. If it is added later,
// the audit requires the fact immediately.
type functionFactEvidence struct {
	fact      functionFact
	name      string
	evidence  string
	functions []string
}

// functionFactEvidenceCatalog is an audit view that the semantic source
// builds. A fact mark and its evidence cannot drift because one measured fact
// produces both values.
var functionFactEvidenceCatalog = buildFunctionFactEvidenceCatalog(functionSemanticSpecs)

func buildFunctionFactEvidenceCatalog(source map[string]functionSpec) []functionFactEvidence {
	type catalogKey struct {
		fact     functionFact
		evidence semanticEvidenceRef
	}
	grouped := make(map[catalogKey][]string)
	for name, spec := range source {
		for _, measured := range spec.measuredFacts {
			key := catalogKey(measured)
			grouped[key] = append(grouped[key], name)
		}
	}
	result := make([]functionFactEvidence, 0, len(grouped))
	for key, functions := range grouped {
		sort.Strings(functions)
		result = append(result, functionFactEvidence{
			fact:      key.fact,
			name:      functionFactName(key.fact),
			evidence:  string(key.evidence),
			functions: functions,
		})
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].fact != result[right].fact {
			return result[left].fact < result[right].fact
		}
		return result[left].evidence < result[right].evidence
	})
	return result
}

func functionFactName(fact functionFact) string {
	switch fact {
	case functionFactComparesArgPair:
		return "compares argument pair"
	case functionFactDynamicForcesNullable:
		return "Dynamic forces Nullable"
	default:
		return fmt.Sprintf("unknown fact %d", fact)
	}
}

// auditFunctionFactEvidence reports drift in both directions. A registry mark
// without evidence is an unsourced claim. An evidenced registry member
// without its mark is a missing rule. The messages differ so that a self-test
// can prove that both witnesses run.
func auditFunctionFactEvidence(registry map[string]functionSpec, catalog []functionFactEvidence) []string {
	var problems []string
	knownFacts := functionFact(0)
	for _, item := range catalog {
		knownFacts |= item.fact
		measured := make(map[string]bool, len(item.functions))
		for _, rawName := range item.functions {
			name := strings.ToLower(rawName)
			if measured[name] {
				problems = append(problems, fmt.Sprintf("fact %q repeats evidence for function %s", item.name, name))
			}
			measured[name] = true
			if spec, present := registry[name]; present && spec.facts&item.fact == 0 {
				problems = append(problems, fmt.Sprintf(
					"function %s has evidence %q for fact %q but its spec has no mark",
					name, item.evidence, item.name,
				))
			}
		}
		for name, spec := range registry {
			if spec.facts&item.fact != 0 && !measured[name] {
				problems = append(problems, fmt.Sprintf(
					"function %s marks fact %q but the evidence catalog has no measured entry",
					name, item.name,
				))
			}
		}
	}
	for name, spec := range registry {
		if unknown := spec.facts &^ knownFacts; unknown != 0 {
			problems = append(problems, fmt.Sprintf("function %s has unknown fact bits %d", name, unknown))
		}
	}
	sort.Strings(problems)
	return problems
}
