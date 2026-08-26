package engine

// operatorCatalog is the declarative list of the binary operators that
// chgen has a measured rule or a measured refusal for.
//
// Before this list, the operator set lived only in the case labels of the
// switch statements in inferBinaryOperationType. The oracle generator
// held its own separate list of operator spellings. Two lists drift: a
// defect was found in "==" and in REGEXP, which inference knew and the
// generator never emitted, thus no fuzz case could reach them.
//
// The catalog is the single source. Inference reads its membership from
// here, and the generator draws its tokens from here.
//
// The catalog does NOT hold the rule bodies. A body stays in the switch,
// because a body needs the operand expressions and the scope. The catalog
// says only WHICH operator belongs to WHICH family.
//
// An operator that is absent from the catalog reaches the refusal at the
// end of inferBinaryOperationType. That refusal is the correct answer for
// an operator that chgen has not measured, and for an operator that a
// later parser version adds.

// opFamily names the group of a binary operator. The family selects the
// rule body in inferBinaryOperationType.
type opFamily int

const (
	// opFamilyUnknown is the zero value, and it is never legal.
	opFamilyUnknown opFamily = iota
	// opPredicate is the fixed-result group: the result base is UInt8
	// and no operand base type survives.
	opPredicate
	// opArith is the arithmetic group. The result follows the operand
	// types.
	opArith
	// opConcat is the concatenation operator. It shares the rule of
	// the concat function.
	opConcat
	// opRefusal is an operator whose operands are not values, thus
	// chgen refuses it with a message that names the cause.
	opRefusal
)

// operatorConstantMode says whether an operand position must be constant.
// Its zero value is illegal.
type operatorConstantMode uint8

const (
	operatorConstantsUnknown operatorConstantMode = iota
	operatorConstantsNone
)

// operatorSpec is one entry of the catalog.
type operatorSpec struct {
	// token is the operator spelling in upper case, as
	// inferBinaryOperationType sees it.
	token string
	// family selects the rule body.
	family opFamily
	// arity is the exact operand count.
	arity uint8
	// constants states the constant-position policy.
	constants operatorConstantMode
	// class states the wrapper transport rule.
	class functionWrapperClass
	// operandDomain, when not nil, reports whether the server accepts
	// a base type as an operand. It is nil when the operator accepts
	// every base type that reaches it.
	operandDomain func(CHType) bool
	// domainMode states whether operandDomain is unrestricted, restricted, or
	// handled by the refusal route.
	domainMode argumentDomainMode
	// parameterPolicy states how a parametric result is supported.
	parameterPolicy parameterResultPolicy
	// evidence names the measurement set for this operator.
	evidence semanticEvidenceRef
}

// operatorCatalog holds every measured operator. The order has no
// meaning; the tests compare it as a set.
var operatorSemanticSpecs = []operatorSpec{
	// The predicate family. "==" is the same operator as "=", and
	// ClickHouse gives both the same result in every measured cell.
	{token: "AND", family: opPredicate, arity: 2, constants: operatorConstantsNone, class: wrapperTransparent, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	{token: "OR", family: opPredicate, arity: 2, constants: operatorConstantsNone, class: wrapperTransparent, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	{token: "IN", family: opPredicate, arity: 2, constants: operatorConstantsNone, class: wrapperTransparent, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	{token: "NOT IN", family: opPredicate, arity: 2, constants: operatorConstantsNone, class: wrapperTransparent, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	{token: "=", family: opPredicate, arity: 2, constants: operatorConstantsNone, class: wrapperTransparent, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	{token: "==", family: opPredicate, arity: 2, constants: operatorConstantsNone, class: wrapperTransparent, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	{token: "!=", family: opPredicate, arity: 2, constants: operatorConstantsNone, class: wrapperTransparent, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	{token: "<>", family: opPredicate, arity: 2, constants: operatorConstantsNone, class: wrapperTransparent, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	{token: "<", family: opPredicate, arity: 2, constants: operatorConstantsNone, class: wrapperTransparent, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	{token: "<=", family: opPredicate, arity: 2, constants: operatorConstantsNone, class: wrapperTransparent, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	{token: ">", family: opPredicate, arity: 2, constants: operatorConstantsNone, class: wrapperTransparent, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	{token: ">=", family: opPredicate, arity: 2, constants: operatorConstantsNone, class: wrapperTransparent, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	{token: "LIKE", family: opPredicate, arity: 2, constants: operatorConstantsNone, class: wrapperTransparent, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	{token: "NOT LIKE", family: opPredicate, arity: 2, constants: operatorConstantsNone, class: wrapperTransparent, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	{token: "ILIKE", family: opPredicate, arity: 2, constants: operatorConstantsNone, class: wrapperTransparent, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	{token: "NOT ILIKE", family: opPredicate, arity: 2, constants: operatorConstantsNone, class: wrapperTransparent, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	// REGEXP is the match function. Its base result is UInt8, as with
	// the LIKE family, but the server refuses most operand types, thus
	// it carries a domain.
	{token: "REGEXP", family: opPredicate, operandDomain: isRegexpOperandType, arity: 2, constants: operatorConstantsNone, class: wrapperTransparent, domainMode: argumentDomainRestricted, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},

	// The arithmetic family.
	{token: "+", family: opArith, arity: 2, constants: operatorConstantsNone, class: wrapperTransparent, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultLattice, evidence: registryMeasurementEvidence},
	{token: "-", family: opArith, arity: 2, constants: operatorConstantsNone, class: wrapperTransparent, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultLattice, evidence: registryMeasurementEvidence},
	{token: "*", family: opArith, arity: 2, constants: operatorConstantsNone, class: wrapperTransparent, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultLattice, evidence: registryMeasurementEvidence},
	{token: "/", family: opArith, arity: 2, constants: operatorConstantsNone, class: wrapperTransparent, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultLattice, evidence: registryMeasurementEvidence},
	{token: "%", family: opArith, arity: 2, constants: operatorConstantsNone, class: wrapperTransparent, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultLattice, evidence: registryMeasurementEvidence},

	// The concatenation operator.
	{token: "||", family: opConcat, arity: 2, constants: operatorConstantsNone, class: wrapperTransparent, domainMode: argumentDomainUnrestricted, parameterPolicy: parameterResultLattice, evidence: registryMeasurementEvidence},

	// The refusals. The operands of these operators are not values:
	// the right operand of "::" is a type name, and the left operand
	// of "->" is a lambda parameter. An attempt to type them reports a
	// missing column and names the wrong cause.
	{token: "::", family: opRefusal, arity: 2, constants: operatorConstantsNone, class: wrapperOpaque, domainMode: argumentDomainSpecialRoute, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
	{token: "->", family: opRefusal, arity: 2, constants: operatorConstantsNone, class: wrapperOpaque, domainMode: argumentDomainSpecialRoute, parameterPolicy: parameterResultCurated, evidence: registryMeasurementEvidence},
}

var operatorCatalog = mustBuildOperatorCatalog(operatorSemanticSpecs)

func mustBuildOperatorCatalog(source []operatorSpec) []operatorSpec {
	result := make([]operatorSpec, len(source))
	copy(result, source)
	seen := make(map[string]bool, len(result))
	for index, spec := range result {
		if spec.token == "" || seen[spec.token] {
			panic("operator semantic specification has an empty or repeated token")
		}
		seen[spec.token] = true
		if spec.family == opFamilyUnknown || spec.arity == 0 || spec.constants == operatorConstantsUnknown || spec.class == wrapperClassUnset || spec.domainMode == argumentDomainUnknown || spec.parameterPolicy == parameterResultUnknown || spec.evidence == "" {
			panic("operator semantic specification has an unknown required state")
		}
		if spec.domainMode == argumentDomainRestricted && spec.operandDomain == nil {
			panic("operator semantic specification has a restricted domain without a rule")
		}
		if spec.domainMode != argumentDomainRestricted && spec.operandDomain != nil {
			panic("operator semantic specification has an unused domain rule")
		}
		if spec.arity != 2 {
			panic("operator semantic specification has an unsupported arity")
		}
		result[index] = spec
	}
	return result
}

// operatorFamilyOf reports the family of an operator token, and false
// when the catalog does not hold the token.
func operatorFamilyOf(token string) (opFamily, bool) {
	for _, spec := range operatorCatalog {
		if spec.token == token {
			return spec.family, true
		}
	}
	return 0, false
}

func operatorSpecFor(token string) (operatorSpec, bool) {
	for _, spec := range operatorCatalog {
		if spec.token == token {
			return spec, true
		}
	}
	return operatorSpec{}, false
}

// operatorTokensOfFamily gives the tokens of one family. The generator
// uses it to emit an operator of a wanted kind.
func operatorTokensOfFamily(family opFamily) []string {
	var tokens []string
	for _, spec := range operatorCatalog {
		if spec.family == family {
			tokens = append(tokens, spec.token)
		}
	}
	return tokens
}
