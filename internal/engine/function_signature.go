package engine

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	clickhouse "github.com/AfterShip/clickhouse-sql-parser/parser"
)

// functionSignature is the measured server call shape. It is separate from
// genSpec, which keeps the useful default shape that the random generator
// writes. Inference and the conformance probe catalog use this signature.
type functionSignature struct {
	forms          []signatureForm
	parameterForms []signatureForm
	relations      []signatureRelation
	constantRanges []signatureConstantRange
	place          placement
	allowBare      bool
	allowOver      bool
	evidence       semanticEvidenceRef
}

type signatureRelationKind uint8

const (
	signatureRelationUnknown signatureRelationKind = iota
	signatureRelationDefaultKeepsFirstType
	signatureRelationArrayElementCommonType
)

type signatureRelation struct {
	kind  signatureRelationKind
	left  int
	right int
}

type signatureConstantRange struct {
	parameter bool
	position  int
	minimum   float64
	maximum   float64
}

// signatureForm is one accepted argument sequence. A repeated group can sit
// between a fixed prefix and a fixed suffix.
type signatureForm struct {
	prefix     []argSort
	repeat     []argSort
	suffix     []argSort
	minRepeats int
	maxRepeats int
}

func validateFunctionSignatureDefinition(name string, signature functionSignature) error {
	if len(signature.forms) == 0 {
		return fmt.Errorf("function %s has no executable argument form", name)
	}
	if len(signature.parameterForms) == 0 {
		return fmt.Errorf("function %s has no executable parameter form", name)
	}
	if signature.place == placementUnset || (!signature.allowBare && !signature.allowOver) {
		return fmt.Errorf("function %s has no executable placement", name)
	}
	if signature.evidence == "" {
		return fmt.Errorf("function %s signature has no measurement evidence", name)
	}
	validateForms := func(forms []signatureForm) error {
		for _, form := range forms {
			if len(form.repeat) == 0 && (form.minRepeats != 0 || form.maxRepeats != 0) {
				return fmt.Errorf("function %s fixed form has repeat limits", name)
			}
			if len(form.repeat) > 0 && (form.minRepeats < 0 || (form.maxRepeats != -1 && form.maxRepeats < form.minRepeats)) {
				return fmt.Errorf("function %s has invalid repeat limits", name)
			}
			for _, sorts := range [][]argSort{form.prefix, form.repeat, form.suffix} {
				for _, sort := range sorts {
					if sort <= argSortUnset || sort > argSortTypeName {
						return fmt.Errorf("function %s has an unknown argument sort", name)
					}
				}
			}
		}
		return nil
	}
	if err := validateForms(signature.forms); err != nil {
		return err
	}
	if err := validateForms(signature.parameterForms); err != nil {
		return err
	}
	for _, relation := range signature.relations {
		if relation.kind <= signatureRelationUnknown || relation.kind > signatureRelationArrayElementCommonType ||
			relation.left < 0 || relation.right < 0 {
			return fmt.Errorf("function %s has an invalid linked argument rule", name)
		}
	}
	for _, valueRange := range signature.constantRanges {
		if valueRange.position < 0 || valueRange.minimum > valueRange.maximum {
			return fmt.Errorf("function %s has an invalid constant range", name)
		}
	}
	return nil
}

func fixedSignatureForm(sorts ...argSort) signatureForm {
	return signatureForm{prefix: append([]argSort(nil), sorts...)}
}

func repeatedSignatureForm(prefix, repeat, suffix []argSort, minRepeats, maxRepeats int) signatureForm {
	return signatureForm{
		prefix: append([]argSort(nil), prefix...), repeat: append([]argSort(nil), repeat...),
		suffix: append([]argSort(nil), suffix...), minRepeats: minRepeats, maxRepeats: maxRepeats,
	}
}

func (form signatureForm) sortsForArity(arity int) ([]argSort, bool) {
	fixed := len(form.prefix) + len(form.suffix)
	if len(form.repeat) == 0 {
		if arity != fixed {
			return nil, false
		}
		result := append([]argSort(nil), form.prefix...)
		return append(result, form.suffix...), true
	}
	remaining := arity - fixed
	if remaining < 0 || remaining%len(form.repeat) != 0 {
		return nil, false
	}
	repeats := remaining / len(form.repeat)
	if repeats < form.minRepeats || (form.maxRepeats != -1 && repeats > form.maxRepeats) {
		return nil, false
	}
	result := append([]argSort(nil), form.prefix...)
	for index := 0; index < repeats; index++ {
		result = append(result, form.repeat...)
	}
	return append(result, form.suffix...), true
}

func (signature functionSignature) sortsForArity(arity int) ([]argSort, bool) {
	for _, form := range signature.forms {
		if sorts, accepted := form.sortsForArity(arity); accepted {
			return sorts, true
		}
	}
	return nil, false
}

func (signature functionSignature) representativeArities() []int {
	seen := make(map[int]struct{})
	for _, form := range signature.forms {
		minimum := len(form.prefix) + len(form.suffix)
		if len(form.repeat) > 0 {
			minimum += form.minRepeats * len(form.repeat)
		}
		seen[minimum] = struct{}{}
		if len(form.repeat) > 0 && (form.maxRepeats == -1 || form.maxRepeats > form.minRepeats) {
			seen[minimum+len(form.repeat)] = struct{}{}
		}
	}
	result := make([]int, 0, len(seen))
	for arity := range seen {
		result = append(result, arity)
	}
	sort.Ints(result)
	return result
}

func (signature functionSignature) rejectedBoundaryArities() []int {
	representatives := signature.representativeArities()
	if len(representatives) == 0 {
		return nil
	}
	minimum := representatives[0]
	maximum := representatives[len(representatives)-1]
	unbounded := false
	for _, form := range signature.forms {
		if len(form.repeat) > 0 && form.maxRepeats == -1 {
			unbounded = true
		}
		if len(form.repeat) > 1 {
			candidate := len(form.prefix) + len(form.suffix) + form.minRepeats*len(form.repeat) + 1
			if _, accepted := signature.sortsForArity(candidate); !accepted && candidate > maximum {
				maximum = candidate
			}
		}
	}
	candidates := make(map[int]struct{})
	if minimum > 0 {
		candidates[minimum-1] = struct{}{}
	}
	for arity := minimum; arity <= maximum; arity++ {
		if _, accepted := signature.sortsForArity(arity); !accepted {
			candidates[arity] = struct{}{}
		}
	}
	if !unbounded {
		candidates[maximum+1] = struct{}{}
	}
	result := make([]int, 0, len(candidates))
	for arity := range candidates {
		result = append(result, arity)
	}
	sort.Ints(result)
	return result
}

func signatureFromGenSpec(spec genSpec, evidence semanticEvidenceRef) functionSignature {
	signature := functionSignature{
		place:     spec.place,
		allowBare: spec.place != placementWindow,
		evidence:  evidence,
	}
	if spec.place == placementAggregate || spec.place == placementWindow {
		signature.allowOver = true
	}
	if spec.maxArity == -1 {
		last := spec.argSorts[len(spec.argSorts)-1]
		prefix := append([]argSort(nil), spec.argSorts[:len(spec.argSorts)-1]...)
		minRepeats := spec.minArity - len(prefix)
		signature.forms = []signatureForm{repeatedSignatureForm(prefix, []argSort{last}, nil, minRepeats, -1)}
	} else {
		for arity := spec.minArity; arity <= spec.maxArity; arity++ {
			sorts := make([]argSort, arity)
			for position := range sorts {
				sortIndex := position
				if sortIndex >= len(spec.argSorts) {
					sortIndex = len(spec.argSorts) - 1
				}
				sorts[position] = spec.argSorts[sortIndex]
			}
			signature.forms = append(signature.forms, fixedSignatureForm(sorts...))
		}
	}
	signature.parameterForms = []signatureForm{fixedSignatureForm()}
	return signature
}

func applyFunctionSignatureOverride(name string, signature functionSignature) functionSignature {
	value := argSortValue
	numberValue := argSortNumber
	offsetValue := argSortOffset
	indexValue := argSortIndex
	integer := argSortConstInt
	text := argSortConstString
	interval := argSortInterval
	switch name {
	case "array", "tuple", "cityhash64", "concat", "rank", "dense_rank", "row_number":
		signature.forms = []signatureForm{repeatedSignatureForm(nil, []argSort{value}, nil, 0, -1)}
	case "map":
		signature.forms = []signatureForm{repeatedSignatureForm(nil, []argSort{value, value}, nil, 0, -1)}
	case "count":
		signature.forms = []signatureForm{fixedSignatureForm(), fixedSignatureForm(value)}
	case "laginframe", "leadinframe":
		signature.forms = []signatureForm{
			fixedSignatureForm(value), fixedSignatureForm(value, integer), fixedSignatureForm(value, integer, value),
		}
		signature.relations = []signatureRelation{{kind: signatureRelationDefaultKeepsFirstType, left: 0, right: 2}}
	case "lag", "lead":
		signature.relations = []signatureRelation{{kind: signatureRelationDefaultKeepsFirstType, left: 0, right: 2}}
	case "now":
		signature.forms = []signatureForm{fixedSignatureForm(), fixedSignatureForm(text)}
	case "today":
		signature.forms = []signatureForm{fixedSignatureForm()}
	case "now64":
		signature.forms = []signatureForm{fixedSignatureForm(), fixedSignatureForm(integer), fixedSignatureForm(integer, text)}
	case "datediff", "date_diff":
		signature.forms = append(signature.forms, fixedSignatureForm(text, value, value, text))
	case "arraysort":
		signature.forms = []signatureForm{fixedSignatureForm(value), fixedSignatureForm(argSortLambda, value)}
	case "tostartofinterval":
		signature.forms = []signatureForm{fixedSignatureForm(value, interval)}
	case "substring", "arrayslice":
		signature.forms = []signatureForm{
			fixedSignatureForm(value, offsetValue), fixedSignatureForm(value, offsetValue, offsetValue),
		}
	case "arrayresize":
		signature.forms = []signatureForm{
			fixedSignatureForm(value, numberValue), fixedSignatureForm(value, numberValue, value),
		}
		signature.relations = []signatureRelation{{kind: signatureRelationArrayElementCommonType, left: 0, right: 2}}
	case "arrayelement":
		signature.forms = []signatureForm{fixedSignatureForm(value, indexValue)}
	case "arraystringconcat":
		signature.forms = []signatureForm{fixedSignatureForm(value), fixedSignatureForm(value, text)}
	case "todatetime", "tostartofday", "tostartofhour", "tostartofminute":
		signature.forms = []signatureForm{fixedSignatureForm(value), fixedSignatureForm(value, text)}
	case "todatetime64":
		signature.forms = []signatureForm{fixedSignatureForm(value, integer), fixedSignatureForm(value, integer, text)}
	case "todatetime64ornull", "todatetime64orzero":
		signature.forms = []signatureForm{
			fixedSignatureForm(value), fixedSignatureForm(value, integer), fixedSignatureForm(value, integer, text),
		}
	}
	if _, temporalShift := temporalShiftFunctions[name]; temporalShift {
		signature.forms = []signatureForm{fixedSignatureForm(value, offsetValue)}
	}

	// ClickHouse exposes first_value as any and last_value as anyLast. Each
	// name is therefore legal as a bare aggregate and as a window call. The
	// other functions in the window placement need an OVER clause.
	switch name {
	case "first_value", "last_value", "first_value_respect_nulls", "firstvaluerespectnulls",
		"last_value_respect_nulls", "lastvaluerespectnulls":
		signature.allowBare = true
	}

	switch {
	case strings.Contains(name, "quantile") || strings.HasPrefix(name, "median"):
		signature.parameterForms = []signatureForm{fixedSignatureForm(), fixedSignatureForm(argSortConstNumber)}
		signature.constantRanges = []signatureConstantRange{{parameter: true, position: 0, minimum: 0, maximum: 1}}
	case name == "grouparray" || name == "grouparrayif" || name == "groupuniqarray" ||
		name == "groupuniqarrayif" || name == "uniqcombined" || name == "uniqcombined64":
		signature.parameterForms = []signatureForm{fixedSignatureForm(), fixedSignatureForm(integer)}
		if name == "uniqcombined" || name == "uniqcombined64" {
			signature.constantRanges = []signatureConstantRange{{parameter: true, position: 0, minimum: 12, maximum: 20}}
		} else {
			// ClickHouse 25.8.29.51 accepts a positive size and refuses zero
			// with Code 36 for all four groupArray and groupUniqArray forms.
			signature.constantRanges = []signatureConstantRange{{parameter: true, position: 0, minimum: 1, maximum: float64(^uint64(0))}}
		}
	}
	return signature
}

func functionSignatureFor(name string) (functionSignature, bool) {
	spec, found := functionRegistry[name]
	if !found || spec.signature == nil {
		return functionSignature{}, false
	}
	return *spec.signature, true
}

func validateFunctionCallSignature(name, displayName string, function *clickhouse.FunctionExpr, scope queryScope, window bool) error {
	args := functionArgs(function)
	if higherOrder, found := higherOrderArrayFunctions[name]; found && len(args) > 0 &&
		(isLambdaExpr(args[0]) || higherOrder.allowNoLambda) {
		if window {
			return fmt.Errorf("function %s cannot use an OVER clause; %s", displayName, pinTypeHint)
		}
		if err := validateHigherOrderLambdaSignature(name, higherOrder); err != nil {
			return err
		}
		if !isLambdaExpr(args[0]) {
			wantArity := 1
			if higherOrder.scalarArgument != nil {
				wantArity++
			}
			if len(args) != wantArity {
				return fmt.Errorf("function %s without a lambda needs its exact scalar arguments and one Array; %s", displayName, pinTypeHint)
			}
			return nil
		}
		arrayCount := len(args) - 1
		if higherOrder.scalarArgument != nil {
			arrayCount--
		}
		if higherOrder.accumulator != nil {
			arrayCount--
		}
		if arrayCount < higherOrder.minimumArrays ||
			(higherOrder.maximumArrays >= 0 && arrayCount > higherOrder.maximumArrays) {
			return fmt.Errorf("function %s has %d array arguments outside its measured range; %s", displayName, arrayCount, pinTypeHint)
		}
		return nil
	}
	signature, found := functionSignatureFor(name)
	if !found {
		return nil
	}
	if window && !signature.allowOver {
		return fmt.Errorf("function %s cannot use an OVER clause; %s", displayName, pinTypeHint)
	}
	if !window && !signature.allowBare {
		return fmt.Errorf("function %s needs an OVER clause; %s", displayName, pinTypeHint)
	}
	for _, argument := range args {
		unwrapped := unwrapColumnExpr(argument)
		if isStarArgument(argument) || isLambdaExpr(unwrapped) {
			continue
		}
		if _, interval := unwrapped.(*clickhouse.IntervalExpr); interval {
			continue
		}
		if _, err := inferExprType(argument, scope); err != nil && !errors.Is(err, errPlaceholderResultType) {
			return fmt.Errorf("function %s argument: %w", displayName, err)
		}
	}
	sorts, accepted := signature.sortsForArity(len(args))
	if !accepted {
		if name == "map" {
			return fmt.Errorf("function %s needs an even number of arguments, but got %d; %s",
				displayName, len(args), pinTypeHint)
		}
		return fmt.Errorf("function %s rejects argument position %d: it %s; %s",
			displayName, len(args)+1, signatureArityText(signature), pinTypeHint)
	}
	for position, sort := range sorts {
		if name == "count" && isStarArgument(args[position]) {
			continue
		}
		if err := checkSignatureArgumentSort(displayName, position, sort, args[position]); err != nil {
			return err
		}
		if sort == argSortNumber || sort == argSortOffset || sort == argSortIntegerOffset || sort == argSortIndex {
			argumentType, err := inferExprType(args[position], scope)
			if sort == argSortIntegerOffset {
				offsetType := argumentType
				if offsetType.normalizedName() == "lowcardinality" && len(offsetType.Params) == 1 {
					offsetType = offsetType.Params[0]
				}
				if offsetType.normalizedName() == "nullable" {
					return fmt.Errorf("function %s argument %d needs a non-Nullable integer offset; %s", displayName, position+1, pinTypeHint)
				}
			}
			base := domainBaseType(argumentType)
			valid := sort == argSortNumber && numberBaseType(base) ||
				sort == argSortOffset && offsetBaseType(base) ||
				sort == argSortIntegerOffset && indexBaseType(base) || sort == argSortIndex && indexBaseType(base)
			if err != nil || !valid {
				return fmt.Errorf("function %s argument %d has an unsupported numeric type; %s", displayName, position+1, pinTypeHint)
			}
		}
	}
	if (name == "laginframe" || name == "leadinframe") && len(args) >= 2 {
		literal := unwrapColumnExpr(args[1]).(*clickhouse.NumberLiteral)
		offset, err := strconv.ParseInt(strings.TrimSpace(literal.Literal), 10, 64)
		if err != nil || offset < 0 {
			return fmt.Errorf("function %s argument 2 needs a constant integer from 0 through 9223372036854775807; %s", displayName, pinTypeHint)
		}
	}
	if (name == "lag" || name == "lead" || name == "nth_value") && len(args) >= 2 {
		if literal, ok := unwrapColumnExpr(args[1]).(*clickhouse.NumberLiteral); ok {
			offset, err := strconv.ParseInt(strings.TrimSpace(literal.Literal), 10, 64)
			if err != nil || offset <= 0 {
				return fmt.Errorf("function %s argument 2 needs a positive integer offset; %s", displayName, pinTypeHint)
			}
		}
	}
	if name == "ntile" && len(args) == 1 {
		literal := unwrapColumnExpr(args[0]).(*clickhouse.NumberLiteral)
		value, err := strconv.ParseUint(strings.TrimSpace(literal.Literal), 10, 64)
		if err != nil || value == 0 {
			return fmt.Errorf("function %s argument 1 needs a positive UInt64 constant; %s", displayName, pinTypeHint)
		}
	}
	parameters := functionParameterArgs(function)
	parameterAccepted := false
	for _, form := range signature.parameterForms {
		parameterSorts, matched := form.sortsForArity(len(parameters))
		if !matched {
			continue
		}
		parameterAccepted = true
		for position, sort := range parameterSorts {
			if err := checkSignatureArgumentSort(displayName+" parameter", position, sort, parameters[position]); err != nil {
				return err
			}
		}
		break
	}
	if !parameterAccepted {
		return fmt.Errorf("function %s does not accept %d aggregate parameters; %s", displayName, len(parameters), pinTypeHint)
	}
	if err := checkSignatureConstantRanges(displayName, signature, args, parameters); err != nil {
		return err
	}
	if err := checkSignatureRelations(displayName, signature, args, scope); err != nil {
		return err
	}
	return nil
}

func checkSignatureConstantRanges(displayName string, signature functionSignature, args, parameters []clickhouse.Expr) error {
	for _, valueRange := range signature.constantRanges {
		values := args
		role := "argument"
		if valueRange.parameter {
			values = parameters
			role = "parameter"
		}
		if valueRange.position >= len(values) {
			continue
		}
		literal, ok := unwrapColumnExpr(values[valueRange.position]).(*clickhouse.NumberLiteral)
		if !ok {
			continue
		}
		value, err := strconv.ParseFloat(strings.TrimSpace(literal.Literal), 64)
		if err != nil || value < valueRange.minimum || value > valueRange.maximum {
			return fmt.Errorf("function %s %s %d must be from %g through %g; %s",
				displayName, role, valueRange.position+1, valueRange.minimum, valueRange.maximum, pinTypeHint)
		}
	}
	return nil
}

func checkSignatureRelations(displayName string, signature functionSignature, args []clickhouse.Expr, scope queryScope) error {
	for _, relation := range signature.relations {
		if relation.left >= len(args) || relation.right >= len(args) {
			continue
		}
		left, err := inferExprType(args[relation.left], scope)
		if err != nil {
			return fmt.Errorf("function %s linked argument: %w", displayName, err)
		}
		right, err := inferExprType(args[relation.right], scope)
		if err != nil {
			return fmt.Errorf("function %s linked argument: %w", displayName, err)
		}
		switch relation.kind {
		case signatureRelationDefaultKeepsFirstType:
			common, commonErr := commonCHTypes([]CHType{left, right})
			if commonErr != nil || common.String() != left.String() {
				return fmt.Errorf("function %s default argument type %s changes the value type %s; %s",
					displayName, right.String(), left.String(), pinTypeHint)
			}
		case signatureRelationArrayElementCommonType:
			base := domainBaseType(left)
			if base.normalizedName() != "array" || len(base.Params) != 1 {
				continue
			}
			if _, commonErr := commonContainerMemberCHType([]CHType{base.Params[0], right}); commonErr != nil {
				return fmt.Errorf("function %s array element and extender have no common type; %s", displayName, pinTypeHint)
			}
		default:
			return fmt.Errorf("function %s has an unknown linked argument rule; %s", displayName, pinTypeHint)
		}
	}
	return nil
}

func signatureArityText(signature functionSignature) string {
	var arities []string
	for _, form := range signature.forms {
		if len(form.repeat) == 0 {
			arities = append(arities, fmt.Sprintf("%d", len(form.prefix)+len(form.suffix)))
			continue
		}
		minimum := len(form.prefix) + len(form.suffix) + form.minRepeats*len(form.repeat)
		if form.maxRepeats == -1 {
			arities = append(arities, fmt.Sprintf("%d or more", minimum))
		} else {
			maximum := len(form.prefix) + len(form.suffix) + form.maxRepeats*len(form.repeat)
			arities = append(arities, fmt.Sprintf("%d through %d", minimum, maximum))
		}
	}
	if len(arities) == 1 && arities[0] == "1" {
		return "accepts 1 argument"
	}
	return "accepts these argument counts: " + strings.Join(arities, ", ")
}

func checkSignatureArgumentSort(displayName string, position int, sort argSort, argument clickhouse.Expr) error {
	argument = unwrapColumnExpr(argument)
	valid := true
	switch sort {
	case argSortValue, argSortNumber, argSortOffset, argSortIntegerOffset, argSortIndex, argSortPredicate:
		valid = !isLambdaExpr(argument)
	case argSortConstString:
		_, valid = argument.(*clickhouse.StringLiteral)
	case argSortConstInt:
		literal, number := argument.(*clickhouse.NumberLiteral)
		valid = number && isLexicalInteger(literal.Literal)
	case argSortConstNumber:
		_, valid = argument.(*clickhouse.NumberLiteral)
	case argSortLambda:
		valid = isLambdaExpr(argument)
	case argSortInterval:
		_, valid = argument.(*clickhouse.IntervalExpr)
	case argSortTypeName:
		switch argument.(type) {
		case *clickhouse.NumberLiteral, *clickhouse.StringLiteral:
			valid = true
		default:
			valid = false
		}
	default:
		valid = false
	}
	if !valid {
		role := "the required syntactic form"
		switch sort {
		case argSortConstString:
			role = "a constant string"
		case argSortConstInt:
			role = "a constant integer"
		case argSortConstNumber:
			role = "a constant number"
		case argSortLambda:
			role = "a lambda"
		case argSortTypeName:
			role = "a constant selector"
		case argSortInterval:
			role = "an INTERVAL expression"
		}
		return fmt.Errorf("function %s argument %d needs %s; %s", displayName, position+1, role, pinTypeHint)
	}
	return nil
}

func isLexicalInteger(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	if value[0] == '+' || value[0] == '-' {
		value = value[1:]
	}
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func functionParameterArgs(function *clickhouse.FunctionExpr) []clickhouse.Expr {
	if function == nil || function.Params == nil || function.Params.ColumnArgList == nil || function.Params.Items == nil {
		return nil
	}
	result := make([]clickhouse.Expr, 0, len(function.Params.Items.Items))
	for _, item := range function.Params.Items.Items {
		result = append(result, unwrapColumnExpr(item))
	}
	return result
}
