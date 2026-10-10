package engine

import (
	"slices"
	"strings"
)

// measuredUnaryFamily contains only facts established by the profile's complete
// matrix. It does not infer a wider domain from a representative successful call.
type measuredUnaryFamily struct {
	result      string
	primitives  string
	decimal     bool
	fixedString bool
	expected    string
	transport   *wrapperTransport
}

type measuredUnaryMember struct {
	spelling          string
	exactSpelling     string
	evidence          semanticEvidenceRef
	nonPortableInputs []string
}

func measuredUnarySpec(family measuredUnaryFamily, member measuredUnaryMember) functionSpec {
	primitives := strings.Fields(family.primitives)
	return functionSpec{
		family:    semanticFamilyFixedResult,
		rule:      fixedFunctionType(family.result),
		class:     wrapperTransparent,
		transport: family.transport,
		strategy:  argsIndependent,
		domain: &argumentDomain{
			name: "measured argument domain",
			accepts: func(value CHType) bool {
				return family.decimal && arithmeticDecimalType(value) ||
					family.fixedString && value.normalizedName() == "fixedstring" && len(value.Params) == 1 ||
					len(value.Params) == 0 && slices.Contains(primitives, value.normalizedName())
			},
			expected: family.expected,
		},
		gen:               scalarCall(member.spelling, 1),
		resultMode:        resultRuleGeneric,
		domainMode:        argumentDomainRestricted,
		domainArgs:        []int{0},
		parameterPolicy:   parameterResultCurated,
		exactSpelling:     member.exactSpelling,
		evidence:          member.evidence,
		nonPortableInputs: slices.Clone(member.nonPortableInputs),
	}
}
