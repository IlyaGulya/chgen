package functionrules

import (
	"fmt"
	"slices"
	"strings"
	"sync"
)

// StringProfile identifies the bounded unary String-result recipe family.
const StringProfile = "string-unary-v1"

var stringBases = []string{"String", "FixedString(8)", "FixedString(32)"}

var stringNames = []string{"base64Encode", "decodeURLComponent", "encodeURLComponent", "lowerUTF8", "normalizeUTF8NFC", "normalizeUTF8NFD", "normalizeUTF8NFKC", "normalizeUTF8NFKD", "regexpQuoteMeta", "reverseUTF8", "soundex", "tryBase64Decode", "upperUTF8"}

func profileNames(profile string) ([]string, error) {
	switch profile {
	case Profile:
		return Names(), nil
	case StringProfile:
		return slices.Clone(stringNames), nil
	default:
		return nil, fmt.Errorf("unknown safe measurement profile %q", profile)
	}
}

// Recipes are immutable and the cache is bounded by the safe allowlists, never
// by names or profiles supplied in evidence. Consumers must not mutate probes.
var recipePlans = sync.OnceValue(func() map[string]map[string][]probe {
	result := make(map[string]map[string][]probe)
	for _, profile := range []string{Profile, StringProfile} {
		allowed, _ := profileNames(profile)
		result[profile] = make(map[string][]probe, len(allowed))
		for _, name := range allowed {
			result[profile][name] = buildProfilePlan(profile, name)
		}
	}
	return result
})

func profilePlan(profile, name string) []probe {
	if probes, found := recipePlans()[profile][name]; found {
		return probes
	}
	return buildProfilePlan(profile, name)
}

func buildProfilePlan(profile, name string) []probe {
	if profile == StringProfile {
		return stringPlan(name)
	}
	return plan(name)
}

func stringPlan(name string) []probe {
	var probes []probe
	for _, base := range stringBases {
		for _, input := range wrappers(base) {
			values := []string{"'AbC'", "''", "'a b'", "'Z'"}
			if base == "String" {
				values[2] = "'Привет café'"
			}
			if strings.Contains(input, "Nullable(") {
				values[3] = "NULL"
			}
			probes = append(probes, probe{id: input, input: input, expression: name + "(c)", values: values})
		}
	}
	// Refusals cover primitive numbers, decimals, enums and non-string families.
	for _, numericProbe := range plan(name) {
		if numericProbe.id != numericProbe.input || numericProbe.input == "String" {
			continue
		}
		if strings.Contains(numericProbe.input, "Nullable(") || strings.Contains(numericProbe.input, "LowCardinality(") || strings.Contains(numericProbe.input, "SimpleAggregateFunction(") {
			continue
		}
		// All canonical decimal scales are measured, not inferred from one scale.
		probes = append(probes, numericProbe)
	}
	for _, extra := range []struct{ input, value string }{
		{"Enum8('a'=1, 'b'=2)", "'a'"}, {"Enum16('a'=1, 'b'=2)", "'a'"},
		{"Array(String)", "['x']"}, {"Map(String, String)", "{'a':'b'}"},
		{"Nullable(Int32)", "1"}, {"LowCardinality(Int32)", "1"},
	} {
		probes = append(probes, probe{id: extra.input, input: extra.input, expression: name + "(c)", values: []string{extra.value, extra.value, extra.value, extra.value}})
	}
	for _, shape := range []struct{ id, expression string }{
		{"arity-zero", name + "()"}, {"arity-two", name + "(c, c)"},
		{"over", name + "(c) OVER ()"}, {"parameters", name + "(1)(c)"},
	} {
		probes = append(probes, probe{id: shape.id, input: "String", expression: shape.expression, values: []string{"'AbC'", "''", "'Привет café'", "'Z'"}})
	}
	return probes
}

func stringSpecification(function Function, digest string) (measuredSpecification, error) {
	positive := make(map[string]bool)
	for _, base := range stringBases {
		for _, input := range wrappers(base) {
			positive[input] = true
		}
	}
	byID := make(map[string]Cell)
	for _, cell := range function.Cells {
		byID[cell.ID] = cell
	}
	for _, id := range []string{"arity-zero", "arity-two", "over", "parameters"} {
		cell := byID[id]
		if !slices.Contains([]int{42, 63, 309}, cell.AnalysisCode) || !slices.Contains([]int{42, 63, 309}, cell.ExecutionCode) {
			return measuredSpecification{}, fmt.Errorf("%s does not prove unary scalar placement", id)
		}
	}
	fixed := true
	preservesMarker := byID["SimpleAggregateFunction(anyLast, String)"].Execution == "SimpleAggregateFunction(anyLast, String)"
	for _, base := range stringBases {
		cell := byID[base]
		if slices.Contains([]int{36, 43, 44}, cell.AnalysisCode) && cell.AnalysisCode == cell.ExecutionCode && base != "String" {
			fixed = false
			for _, input := range wrappers(base) {
				wrapper := byID[input]
				// Nullable analysis may preserve FixedString even though real
				// execution refuses it. Only the witnessed argument-refusal
				// codes and that exact analysis type are admissible here.
				analysisRefused := slices.Contains([]int{36, 43, 44}, wrapper.AnalysisCode)
				analysisDeferred := wrapper.AnalysisCode == 0 && strings.Contains(input, "Nullable(") && wrapper.Analysis == expectedWrapper(input, base)
				if (!analysisRefused && !analysisDeferred) || !slices.Contains([]int{36, 43, 44}, wrapper.ExecutionCode) {
					return measuredSpecification{}, fmt.Errorf("inconsistent FixedString refusal")
				}
			}
			continue
		}
		for _, input := range wrappers(base) {
			wrapper := byID[input]
			want := expectedWrapper(input, "String")
			if preservesMarker && input == "SimpleAggregateFunction(anyLast, String)" {
				want = input
			}
			if wrapper.Analysis != want || wrapper.Execution != want || wrapper.AnalysisCode != 0 || wrapper.ExecutionCode != 0 {
				return measuredSpecification{}, fmt.Errorf("string wrapper transport is not proved for %s", input)
			}
		}
	}
	for _, cell := range function.Cells {
		if cell.ID != cell.Input || positive[cell.Input] {
			continue
		}
		if cell.AnalysisCode != 43 || cell.ExecutionCode != 43 {
			return measuredSpecification{}, fmt.Errorf("negative domain %s is not proved", cell.Input)
		}
	}
	domain := `primitives: "string",`
	if fixed {
		domain += "\nfixedString: true,"
	}
	transport := ""
	if preservesMarker {
		transport = "transport: &caseFoldingTransport,"
	}
	return renderSpecification(function, digest, StringProfile, "String", domain, transport), nil
}
