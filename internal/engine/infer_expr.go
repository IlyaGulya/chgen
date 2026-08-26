package engine

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	clickhouse "github.com/AfterShip/clickhouse-sql-parser/parser"
)

func inferExprType(expression clickhouse.Expr, scope queryScope) (CHType, error) {
	switch expr := expression.(type) {
	case *clickhouse.ColumnExpr:
		return inferExprType(expr.Expr, scope)
	case *clickhouse.Ident:
		if normalizeBareNullIdentifier && expr.QuoteType == clickhouse.Unquoted && strings.EqualFold(expr.Name, "NULL") {
			return wrapNullable(CHType{Name: "Nothing"}), nil
		}
		if aliasExpr, ok := scope.projectionExprs[expr.Name]; ok && !scope.aliasExpansion[expr.Name] {
			expanded := scope
			expanded.aliasExpansion = cloneAliasExpansion(scope.aliasExpansion)
			expanded.aliasExpansion[expr.Name] = true
			return inferExprType(aliasExpr, expanded)
		}
		if scalar, ok := scope.lookupLocalScalar(expr.Name); ok {
			return scalar, nil
		}
		if value, ok := scope.arrayJoinTypes[expr.Name]; ok {
			return value, nil
		}
		columnType, err := scope.lookupLocalColumn("", expr.Name)
		if err == nil {
			return columnType, nil
		}
		if aliasExpr, ok := scope.lookupProjectionExpr(expr.Name); ok {
			expanded := scope
			expanded.aliasExpansion = cloneAliasExpansion(scope.aliasExpansion)
			expanded.aliasExpansion[expr.Name] = true
			return inferExprType(aliasExpr, expanded)
		}
		if scope.reservedScalars[expr.Name] {
			return CHType{}, fmt.Errorf("scalar CTE %q is not resolved", expr.Name)
		}
		if scope.parent != nil {
			return inferExprType(expr, *scope.parent)
		}
		if err != nil && (strings.EqualFold(expr.Name, "true") || strings.EqualFold(expr.Name, "false")) {
			// A bare true or false parses as an identifier. When no
			// column shadows the name, it is the Bool literal.
			return CHType{Name: "Bool"}, nil
		}
		return columnType, err
	case *clickhouse.SubQuery:
		if scalar, ok := scope.scalarSubqueries[expr.Select]; ok {
			return scalar, nil
		}
		return CHType{}, fmt.Errorf("scalar subquery type was not resolved")
	case *clickhouse.Path:
		if len(expr.Fields) < 2 {
			return CHType{}, fmt.Errorf("column path %q is empty", clickhouse.Format(expr))
		}
		qualifier := expr.Fields[len(expr.Fields)-2].Name
		field := expr.Fields[len(expr.Fields)-1].Name
		// A path over a named Tuple column reads one element. This
		// spelling, and only this spelling, keeps the LowCardinality
		// wrapper of the element (measured on ClickHouse 25.8.29.51:
		// nn.b is LowCardinality(String), while nn.2 and
		// tupleElement(nn, 'b') are both String).
		if columnType, err := scope.lookupColumn("", qualifier); err == nil && isCHTuple(columnType) {
			return tupleElementByName(columnType, field, true)
		}
		if value, ok := scope.arrayJoinTypes[qualifier]; ok && isCHTuple(value) {
			return tupleElementByName(value, field, true)
		}
		return scope.lookupColumn(qualifier, field)
	case *clickhouse.ParamExprList:
		// A parenthesized expression. One item keeps the type of the
		// item. More items make a tuple (measured on ClickHouse
		// 25.8.29.51: (u8, i8) is Tuple(UInt8, Int8)).
		if expr.Items == nil || len(expr.Items.Items) == 0 {
			return CHType{}, fmt.Errorf("empty parenthesized expression")
		}
		if len(expr.Items.Items) == 1 {
			return inferExprType(expr.Items.Items[0], scope)
		}
		itemTypes := make([]CHType, 0, len(expr.Items.Items))
		for _, item := range expr.Items.Items {
			itemType, err := inferExprType(item, scope)
			if err != nil {
				return CHType{}, err
			}
			itemTypes = append(itemTypes, itemType)
		}
		return tupleFunctionResult(itemTypes)
	case *clickhouse.IndexOperation:
		return inferIndexOperationType(expr, scope)
	case *clickhouse.UnaryExpr:
		return inferUnaryExprType(expr, scope)
	case *clickhouse.ObjectParams:
		return inferSubscriptType(expr, scope)
	case *clickhouse.ArrayParamList:
		// An array literal is the array constructor with another
		// spelling, thus it uses the same rule: the element type is
		// the common supertype of the members. Measured on ClickHouse
		// 25.8.29.51 with real columns, because the server folds a
		// literal array: [lc, s] is Array(String), [lc, lc] is
		// Array(LowCardinality(String)), [i8, i32] is Array(Int32).
		// See arrayFunctionResult.
		//
		// An empty literal is Array(Nothing) on the server. chgen
		// refuses it, because Nothing has no Go type to generate.
		if expr.Items == nil || len(expr.Items.Items) == 0 {
			return CHType{}, fmt.Errorf("cannot infer the element type of an empty array literal")
		}
		itemTypes := make([]CHType, 0, len(expr.Items.Items))
		for _, item := range expr.Items.Items {
			itemType, err := inferExprType(item, scope)
			if err != nil {
				return CHType{}, err
			}
			itemTypes = append(itemTypes, itemType)
		}
		return arrayFunctionResult(itemTypes)
	case *clickhouse.BinaryOperation:
		if isInOperation(string(expr.Operation)) {
			if _, ok := unwrapColumnExpression(expr.RightExpr).(*clickhouse.SubQuery); ok {
				left, err := inferExprType(expr.LeftExpr, scope)
				if err != nil {
					return CHType{}, err
				}
				// A set subquery has no scalar type for the ordinary
				// binary-operator path to read. Keep its special result,
				// but read nullability from the value. Nullable can be at
				// the top level, inside LowCardinality, or inside
				// SimpleAggregateFunction.
				if branchArgumentNullable(left) {
					return wrapNullable(CHType{Name: "UInt8"}), nil
				}
				return CHType{Name: "UInt8"}, nil
			}
		}
		return inferBinaryOperationType(expr, scope)
	case *clickhouse.CaseExpr:
		return inferCaseExprType(expr, scope)
	case *clickhouse.BetweenClause:
		return inferBetweenType(expr, scope)
	case *clickhouse.IsNullExpr:
		return inferIsNullPredicateType(expr.Expr, scope, "IS NULL")
	case *clickhouse.IsNotNullExpr:
		return inferIsNullPredicateType(expr.Expr, scope, "IS NOT NULL")
	case *clickhouse.WindowFunctionExpr:
		if err := validateWindowFunction(expr, scope); err != nil {
			return CHType{}, err
		}
		result, err := inferFunctionTypeAt(expr.Function, scope, true)
		if err != nil {
			return CHType{}, err
		}
		return windowResultWithoutGeoAliases(result), nil
	case *clickhouse.FunctionExpr:
		if expr.Name != nil && strings.EqualFold(expr.Name.Name, "exists") {
			return CHType{Name: "UInt8"}, nil
		}
		result, err := inferFunctionType(expr, scope)
		if err != nil {
			return CHType{}, err
		}
		name := strings.ToLower(expr.Name.Name)
		switch {
		case name == "first_value", name == "last_value", isAggregateCallName(name):
			return windowResultWithoutGeoAliases(result), nil
		default:
			return result, nil
		}
	case *clickhouse.CastExpr:
		// The target type gives the result, but it does not make the
		// source expression legal. The server refuses a CAST over an
		// argument that it cannot type: CAST(bogusfn(s) AS Int32) is
		// Code: 46 on ClickHouse 25.8.29.51. Thus infer the source
		// first and give its failure back. A bare positional
		// placeholder is absorbed, because CAST(? AS Int32) is a
		// normal pinned parameter and has no result type of its own.
		var sourceType CHType
		hasSourceType := false
		if expr.Expr != nil {
			inferredSource, err := inferExprType(expr.Expr, scope)
			if err != nil && !errors.Is(err, errPlaceholderResultType) {
				return CHType{}, fmt.Errorf("CAST source: %w", err)
			}
			if err == nil {
				sourceType = inferredSource
				hasSourceType = true
			}
		}
		columnType, ok := expr.AsType.(clickhouse.ColumnType)
		if ok {
			inferred, err := parseCHType(columnType)
			if err != nil {
				return CHType{}, err
			}
			return validateUntypedNullCastTarget(sourceType, hasSourceType, inferred)
		}
		if typeLiteral, ok := expr.AsType.(*clickhouse.StringLiteral); ok {
			typeName := strings.Trim(typeLiteral.Literal, "'")
			inferred, err := parseCHTypeName(typeName)
			if err != nil {
				return CHType{}, fmt.Errorf("CAST target %q: %w", typeName, err)
			}
			return validateUntypedNullCastTarget(sourceType, hasSourceType, inferred)
		}
		return CHType{}, fmt.Errorf("CAST target %T is not a ClickHouse type", expr.AsType)
	case *clickhouse.NumberLiteral:
		if strings.ContainsAny(expr.Literal, ".eE") {
			return CHType{Name: "Float64"}, nil
		}
		// ClickHouse gives an integer literal the smallest type that
		// holds its value (measured on ClickHouse 25.8.29.51:
		// toTypeName(1) is UInt8, toTypeName(18446744073709551615) is
		// UInt64). The lexer can fold a leading minus into the literal.
		literal, negative := strings.CutPrefix(expr.Literal, "-")
		if literalType, ok := integerLiteralCHType(literal, negative); ok {
			return literalType, nil
		}
		// A literal outside the 64-bit range or in a non-decimal base
		// keeps the historic fallback.
		return CHType{Name: "Int64"}, nil
	case *clickhouse.StringLiteral:
		return CHType{Name: "String"}, nil
	case *clickhouse.NullLiteral:
		return wrapNullable(CHType{Name: "Nothing"}), nil
	case *clickhouse.BoolLiteral:
		return CHType{Name: "Bool"}, nil
	case *clickhouse.PlaceHolder:
		return CHType{}, errPlaceholderResultType
	default:
		return CHType{}, fmt.Errorf("cannot infer type for ClickHouse expression %T (%s); %s", expression, clickhouse.Format(expression), pinTypeHint)
	}
}

var normalizeBareNullIdentifier = true

var bareNullCastRequiresNullableTarget = true

func validateUntypedNullCastTarget(source CHType, hasSource bool, target CHType) (CHType, error) {
	if !bareNullCastRequiresNullableTarget || !hasSource || !isUntypedNullType(source) {
		return target, nil
	}
	if !strings.EqualFold(target.Name, "Nullable") || len(target.Params) != 1 || !canBeInsideNullable(target.Params[0]) {
		return CHType{}, fmt.Errorf("CAST of bare NULL requires a valid Nullable target, got %s", target.String())
	}
	return target, nil
}

// inferIsNullPredicateType types both null predicate spellings. ClickHouse
// 25.8.29.51 returns UInt8 for nullable, non-nullable, LowCardinality, and
// LowCardinality(Nullable) real columns. It also returns UInt8 for aliases,
// subexpressions, constants, and typed query parameters. No wrapper from the
// operand reaches the result.
//
// A fixed result does not make an invalid operand legal. The operand must have
// a type before the predicate can have a type. A bare placeholder is the one
// exception. It gets its type from parameter inference. It cannot give a Go
// result type by itself, but it is a valid operand of this fixed predicate.
type isNullPredicateRule struct {
	resultName        string
	validateOperand   bool
	acceptPlaceholder bool
}

var measuredIsNullPredicateRule = isNullPredicateRule{
	resultName:        "UInt8",
	validateOperand:   true,
	acceptPlaceholder: true,
}

func inferIsNullPredicateType(operand clickhouse.Expr, scope queryScope, operator string) (CHType, error) {
	rule := measuredIsNullPredicateRule
	if isBarePlaceholderOperand(operand) && rule.acceptPlaceholder {
		return CHType{Name: rule.resultName}, nil
	}
	if rule.validateOperand {
		if _, err := inferExprType(operand, scope); err != nil {
			return CHType{}, fmt.Errorf("%s operand: %w", operator, err)
		}
	}
	return CHType{Name: rule.resultName}, nil
}

func isBarePlaceholderOperand(operand clickhouse.Expr) bool {
	switch expr := operand.(type) {
	case *clickhouse.ColumnExpr:
		return expr.Expr != nil && isBarePlaceholderOperand(expr.Expr)
	case *clickhouse.ParamExprList:
		return expr.Items != nil && len(expr.Items.Items) == 1 && isBarePlaceholderOperand(expr.Items.Items[0])
	case *clickhouse.PlaceHolder:
		return true
	default:
		return false
	}
}

func cloneAliasExpansion(source map[string]bool) map[string]bool {
	clone := make(map[string]bool, len(source)+1)
	for name, active := range source {
		clone[name] = active
	}
	return clone
}

func inferBinaryOperationType(expression *clickhouse.BinaryOperation, scope queryScope) (CHType, error) {
	operator := strings.ToUpper(string(expression.Operation))
	if spec, known := operatorSpecFor(operator); known {
		if spec.family == opFamilyUnknown || spec.domainMode == argumentDomainUnknown || spec.parameterPolicy == parameterResultUnknown {
			return CHType{}, fmt.Errorf("operator %s has an unknown semantic state; %s", operator, pinTypeHint)
		}
	}
	switch operator {
	// "==" is the same operator as "=". ClickHouse accepts both spellings
	// and gives them the same result in every measured cell. Before this
	// list held "==", the operator fell to the historic fallback and took
	// the left operand type, thus "e8 == s" answered Enum8 and
	// "f64 == i32" answered Float64, where the server answers UInt8 for
	// both.
	//
	// REGEXP is the match function. Its base result is UInt8 and no
	// operand base type survives, exactly as with the LIKE family, thus it
	// joins this rule and takes the UInt8 base below. The operand types
	// that the server refuses are checked before that.
	//
	// The measured tables are in docs/binary-operator-fallback-survey.md.
	//
	// The token list itself lives in operatorCatalog. The generator of the
	// type oracle draws its operators from the same list, thus an operator
	// that inference knows can never stay unfuzzed. The pin in
	// operator_catalog_test.go holds these case labels against the
	// catalog, so the two cannot drift apart in silence.
	case "AND", "OR", "IN", "NOT IN", "=", "==", "!=", "<>", "<", "<=", ">", ">=",
		"LIKE", "NOT LIKE", "ILIKE", "NOT ILIKE", "REGEXP":
		// ClickHouse makes the predicate result Nullable when an
		// operand is Nullable, and keeps LowCardinality when one
		// operand is LowCardinality and the other is a constant
		// (measured on 25.8.29.51: ns = s is Nullable(UInt8),
		// lc = 'a' is LowCardinality(UInt8), lc = lc is UInt8).
		// The predicate result type is fixed, but a fixed result does
		// not make the call legal. Two operand failures are different
		// and must stay different:
		//
		//   - A bare positional placeholder has no result type by
		//     construction. That is "not known yet", not "impossible",
		//     thus the predicate keeps the bare fixed result. This is
		//     why "i32 = ?" and "s LIKE ?" continue to work.
		//   - Every other failure is a measured refusal: the server
		//     rejects the operand itself. Measured on ClickHouse
		//     25.8.29.51 with real table columns, the outer predicate
		//     then also fails with Code: 43 ("empty(e8) OR true",
		//     "trim(e8) LIKE '%a%'", "avg(s) > 1" and
		//     "trim(e8) IN ('a')" are all Code: 43). A type here would
		//     be a silently wrong answer, thus the refusal propagates.
		operands := []clickhouse.Expr{expression.LeftExpr, expression.RightExpr}
		nullable := false
		lowCardinalityCount := 0
		othersConstant := true
		// operandTypes keeps the type of each operand that inference
		// could fill in, so that the comparability of the PAIR can be
		// checked after the loop. An operand whose type is unknown, for
		// example a bare placeholder, leaves its slot empty and the
		// pair check then accepts the pair.
		operandTypes := make([]CHType, len(operands))
		for operandIndex, operand := range operands {
			operandType, err := inferExprType(operand, scope)
			if err != nil {
				if !errors.Is(err, errPlaceholderResultType) {
					return CHType{}, fmt.Errorf("operator %s operand: %w", operator, err)
				}
				othersConstant = false
				continue
			}
			operandTypes[operandIndex] = operandType
			operandBase, _, _ := splitCHWrappers(operandType)
			switch operator {
			case "=", "==", "!=", "<>", "<", "<=", ">", ">=":
				// A Dynamic operand makes a legal comparison result
				// Nullable. The pair check below still refuses a Dynamic
				// against a Variant or another measured illegal neighbor.
				if isDynamicCHType(operandBase) {
					nullable = true
				}
			}
			// REGEXP reads both of its operands as text. Measured on
			// ClickHouse 25.8.29.51 with real columns, only String,
			// FixedString, Enum8 and Enum16 are legal; every other
			// operand type is rejected with Code: 43
			// (ILLEGAL_TYPE_OF_ARGUMENT, "Illegal type <T> of
			// argument of function match"), in analysis and at
			// execution alike. The historic fallback gave those a
			// type, which is a type for an expression that the
			// server cannot run.
			if operator == "REGEXP" && !isRegexpOperandType(operandBase) {
				return CHType{}, fmt.Errorf(
					"operator REGEXP does not accept an operand of type %s; ClickHouse needs String, FixedString or an Enum here; %s",
					operandType.String(), pinTypeHint,
				)
			}
			// The LIKE family reads both of its operands as text, and
			// it reads the SAME set that REGEXP does. Measured on
			// ClickHouse 25.8.29.51 with real columns: String,
			// FixedString and Enum8 give a result, and the wrappers
			// travel through it (s LIKE 'a' is UInt8, lc_s LIKE 'a' is
			// LowCardinality(UInt8), n_s LIKE 'a' is Nullable(UInt8),
			// lcn_s LIKE 'a' is LowCardinality(Nullable(UInt8))).
			// Every other operand base type is Code: 43, among them
			// Int32 and Bool, which the wrapper grid reported as 48
			// cells that chgen typed and the server refuses.
			//
			// The check runs on the operand BASE, that is after the
			// Nullable and the LowCardinality wrappers come off,
			// because the server decides on the inner type.
			// A SimpleAggregateFunction operand is judged on the value
			// INSIDE the marker, because the server reads through it
			// (measured: saf_s LIKE 'a%' is UInt8, saf_s ILIKE 'a%' is
			// UInt8, and saf_i32 LIKE 'a%' is Code: 43 with the message
			// "Illegal type SimpleAggregateFunction(anyLast, Int32) of
			// argument"). Without the look-through the check refuses a
			// call the server runs.
			likeBase := operandBase
			if inner, ok := simpleAggregateWrapperInner(likeBase); ok {
				likeBase, _, _ = splitCHWrappers(inner)
			}
			if strings.Contains(operator, "LIKE") && !isRegexpOperandType(likeBase) {
				return CHType{}, fmt.Errorf(
					"operator %s does not accept an operand of type %s; ClickHouse needs String, FixedString or an Enum here; %s",
					operator, operandType.String(), pinTypeHint,
				)
			}
			// AND and OR read each operand as a condition. The server
			// accepts the narrow integers, the two floats and Bool, and
			// refuses everything else with Code: 43. See
			// isLogicOperandType for the measured table, and note that
			// the WIDE integers are refused although the arithmetic
			// aggregates accept them.
			//
			// A SimpleAggregateFunction operand is judged on the value
			// inside the marker, exactly as the nullability rule below
			// reads through it (measured: saf_i32 AND saf_i32 is UInt8
			// and safn_i32 AND safn_i32 is Nullable(UInt8), while
			// saf_s AND saf_s is Code: 43).
			if operator == "AND" || operator == "OR" {
				logicBase := operandBase
				if inner, ok := simpleAggregateWrapperInner(logicBase); ok {
					logicBase, _, _ = splitCHWrappers(inner)
				}
				if !isLogicOperandType(logicBase) {
					return CHType{}, fmt.Errorf(
						"operator %s does not accept an operand of type %s; ClickHouse needs an integer of at most 64 bits, a float or a Bool here; %s",
						operator, operandType.String(), pinTypeHint,
					)
				}
			}
			// A SimpleAggregateFunction(f, Nullable(T)) operand makes
			// the result Nullable, exactly as a bare Nullable(T)
			// operand does. The server READS THROUGH the marker and
			// answers about the value inside it, thus operandNullable
			// alone does not see the null: it reports the wrappers ON
			// the type and the null lives in the inner type. The
			// operand BASE keeps the marker, because the comparability
			// check below is on the type and not on the value.
			//
			// Measured on ClickHouse 25.8.29.51 with real columns in a
			// real table, never over literals, because the server folds
			// constants (safn is SimpleAggregateFunction(anyLast,
			// Nullable(Int32)), saf is SimpleAggregateFunction(anyLast,
			// Int32), safns is SimpleAggregateFunction(anyLast,
			// Nullable(String)), safs is SimpleAggregateFunction(anyLast,
			// String), i32 is Int32):
			//
			//	safn = i32        Nullable(UInt8)   saf = i32       UInt8
			//	safn > i32        Nullable(UInt8)   saf > i32       UInt8
			//	safn IN (1, 2)    Nullable(UInt8)
			//	safns LIKE 'a%'   Nullable(UInt8)   safs LIKE 'a%'  UInt8
			//	safns || 'x'      Nullable(String)  safs || 'x'     String
			//	safn AND 1        Nullable(UInt8)
			nullable = nullable || branchArgumentNullable(operandType)
			// A SimpleAggregateFunction(f, LowCardinality(T)) operand
			// carries the wrapper exactly as a bare LowCardinality(T)
			// operand does, for the same reason as the Nullable rule
			// above: the server reads through the marker and answers
			// about the value inside it, thus operandLowCardinality
			// alone does not see the wrapper. Measured on ClickHouse
			// 25.8.29.51 with real columns (saflcs is
			// SimpleAggregateFunction(anyLast, LowCardinality(String)),
			// lcs is LowCardinality(String)):
			//
			//	saflcs LIKE 'a'   LowCardinality(UInt8)
			//	lcs LIKE 'a'      LowCardinality(UInt8)
			//
			// See branchArgumentLowCardinality.
			if branchArgumentLowCardinality(operandType) {
				lowCardinalityCount++
				continue
			}
			if !isConstLiteralExpr(operand, scope) {
				othersConstant = false
			}
		}
		// This is the LowCardinality rule of the PREDICATE operators:
		// the wrapper survives when exactly one operand carries it and
		// every other operand is a constant literal. It is the same
		// measured rule that a transparent function follows.
		//
		// The rule does not hold for every operator in this case list,
		// thus it is a candidate answer only. The transport table
		// decides below whether this operator keeps the wrapper; see
		// logicOperatorTransport in wrapper_transport.go.
		lowCardinality := lowCardinalityCount == 1 && othersConstant
		// The six comparison operators need a pair of operand types that
		// the server can compare. The result type of a comparison is
		// always Bool or UInt8, thus a rule that reads only the result
		// shape answers a type for a pair that ClickHouse refuses, for
		// example "Decimal(38, 2) <= String" (Code: 43, "No operation
		// lessOrEquals between Decimal(38, 2) and String").
		//
		// AND, OR, IN, NOT IN and the LIKE family are NOT in this list.
		// They do not compare their two operands against each other:
		// AND and OR read each operand as a condition, IN reads the
		// right operand as a set, and the LIKE family reads both
		// operands as text. REGEXP has its own per-operand rule above.
		//
		// A CONSTANT operand folds into the type of the other side and
		// keeps the pair legal ("dec <= '1.5'" gives 1, while
		// "dec <= s" gives Code: 43), thus the check reads the operand
		// expressions and not only their types.
		switch operator {
		case "=", "==", "!=", "<>", "<", "<=", ">", ">=":
			if err := checkComparableOperandExprs(
				"operator "+operator,
				expression.LeftExpr, expression.RightExpr,
				operandTypes[0], operandTypes[1], scope,
			); err != nil {
				return CHType{}, err
			}
		}
		// AND and OR are not predicates. They are operations on Bool
		// and they PRESERVE Bool under ONE rule, measured on
		// ClickHouse 25.8.29.51 with real columns and NEVER over a
		// literal, which the server folds:
		//
		//   The base is Bool if AT LEAST ONE operand carries a Bool
		//   base OUTSIDE a Nullable wrapper and OUTSIDE a
		//   SimpleAggregateFunction marker that SURVIVES a value
		//   read. Otherwise the base is UInt8. The Nullable wrapper
		//   on the RESULT is unchanged by this rule: it still
		//   applies whenever any operand is Nullable.
		//
		// This is a single predicate, applied by andOrOperandCarriesBareBool
		// below. There is no arity threshold and no counting: the
		// predicate asks, of one operand, only whether a bare Bool is
		// present once the Nullable wrapper and a surviving marker are
		// set aside.
		//
		// Evidence (b is a bare Bool column; n1, n2, n3 are distinct
		// Nullable(Bool) columns; saf, saf2, saf3 are distinct
		// SimpleAggregateFunction(anyLast, Bool) columns, whose
		// marker SURVIVES a value read, measured as
		// identity(saf) = SimpleAggregateFunction(anyLast, Bool),
		// see simpleAggregateMarkerSurvives; lb, lb2 are distinct
		// LowCardinality(Bool) columns, which this rule does NOT
		// exclude, so a LowCardinality(Bool) operand counts as
		// carrying Bool):
		//
		//	b AND b2                 Bool             (a bare Bool operand is present)
		//	n1 AND n2                Nullable(UInt8)  (every Bool is under Nullable)
		//	n1 AND b                 Nullable(Bool)   (a bare Bool operand is present)
		//	b AND n1 AND n2          Nullable(Bool)   (the bare b still counts)
		//	n1 AND b AND n2          Nullable(Bool)   (order does not matter)
		//	n1 AND n2 AND n3         Nullable(UInt8)  (no bare Bool anywhere)
		//	saf AND saf2             UInt8            (every Bool is behind a surviving marker)
		//	saf AND b                Bool             (a bare Bool operand is present)
		//	saf AND n1               Nullable(UInt8)  (Bool only behind Nullable or a surviving marker)
		//	saf AND saf2 AND n1      Nullable(UInt8)  (same: no bare Bool, no LowCardinality(Bool))
		//	lb AND lb2               Bool             (LowCardinality(Bool) is NOT excluded)
		//	lb AND n1                Nullable(Bool)   (the bare-carrying lb still counts)
		//
		// A SimpleAggregateFunction(f, LowCardinality(Bool)) marker
		// does NOT survive a value read (measured: identity(saflc) is
		// LowCardinality(Bool), the marker is already gone), so such
		// an operand is judged on its LowCardinality(Bool) value and
		// counts as carrying Bool, exactly like a bare lb column.
		//
		// PARENTHESISATION IS PART OF THE RULE, not an artifact of it.
		// An UNPARENTHESISED chain of the SAME operator is one flat
		// operand list on the server, so the check must look straight
		// through to the LEAVES of such a chain; an EXPLICITLY
		// parenthesised sub-expression is sealed, so the check must
		// stop at its own computed type and go no further. Measured on
		// ClickHouse 25.8.29.51, with the same b, n1, n2 as above:
		//
		//	b AND n1 AND n2   (no parens)   Nullable(Bool)
		//	(b AND n1) AND n2 (explicit)    Nullable(UInt8)
		//	b AND (n1 AND n2) (explicit)    Nullable(Bool)
		//
		// The middle row seals "b AND n1" at Nullable(Bool): the outer
		// AND then reads ONE operand of that computed type, which
		// carries no bare Bool, so the whole expression is
		// Nullable(UInt8). The first row has NO seal: the parser gives
		// "b AND n1 AND n2" the identical AST shape as the middle row
		// (BinaryOperation{BinaryOperation{b,n1}, n2}, left-associative,
		// with no ParamExprList node marking a paren), yet the server
		// answers differently for the two. The distinguishing AST
		// signal is exactly the explicit-parenthesis node: the parser
		// wraps an EXPLICITLY parenthesised sub-expression in a
		// ParamExprList, and only that wrapping marks a seal. This
		// inference reads that signal directly by recursing through an
		// un-parenthesised nested BinaryOperation of the SAME operator,
		// and treating a ParamExprList (or any other node) as sealed.
		//
		// Every other operator in this case list IS a predicate, and
		// a predicate always answers plain UInt8, even over a Bool
		// operand (measured: equals(Bool, Bool) is UInt8, less(Bool,
		// Bool) is UInt8, s LIKE '%a%' is UInt8, lcn ILIKE '%a%' is
		// LowCardinality(Nullable(UInt8))). A wrapper still applies on
		// top of this base; only the base name changes with the
		// operator family.
		base := CHType{Name: "UInt8"}
		if operator == "AND" || operator == "OR" {
			base = CHType{Name: "UInt8"}
			leftHasBool, err := andOrOperandCarriesBareBool(expression.LeftExpr, operator, scope)
			if err != nil {
				return CHType{}, err
			}
			rightHasBool, err := andOrOperandCarriesBareBool(expression.RightExpr, operator, scope)
			if err != nil {
				return CHType{}, err
			}
			if leftHasBool || rightHasBool {
				base = CHType{Name: "Bool"}
			}
		}
		// One transport table decides which wrappers the result
		// carries. Most operators in this case list keep the candidate
		// answer above, because their class transport is transparent.
		// The logic operators AND and OR remove LowCardinality always,
		// and the table holds that measurement.
		//
		// The lookup key is the FUNCTION spelling of the operator,
		// which is the same key that a call of the function form uses.
		// The measured behaviour of "a AND b" and of "and(a, b)" is one
		// behaviour, thus it must have one entry.
		transport := transportForFunction(
			strings.ToLower(operator), wrapperTransparent,
		).withResolvedLowCardinality(lowCardinality)
		return applyWrapperTransport(
			base,
			transport,
			wrapperCall{
				base:     base,
				stacks:   []wrapperStack{{lowCardinality: lowCardinality, nullable: nullable, outerNullable: nullable}},
				argCount: len(operands),
			},
		), nil
	}
	// INTERVAL arithmetic is resolved before the operand inference,
	// because an INTERVAL has no result type of its own: only the pair
	// (temporal operand, unit) has one. See the measured matrix in
	// temporal.go.
	//
	// Measured on ClickHouse 25.8.29.51: the INTERVAL must be the RIGHT
	// operand of "-". "INTERVAL 1 DAY - dt" is rejected with
	// ILLEGAL_TYPE_OF_ARGUMENT ("argument of type Interval cannot be
	// first"). For "+" both operand orders are legal and give the same
	// type ("INTERVAL 1 DAY + dt" is DateTime).
	// The two refusals below come BEFORE the operand inference, because
	// the operands of these operators are not values. The right operand of
	// "::" is a type name and the left operand of "->" is a lambda
	// parameter, thus an attempt to type them reports a missing column and
	// names the wrong cause.
	switch operator {
	case "::":
		// The result of "x :: T" is the type T, which the RIGHT
		// operand names. Measured on ClickHouse 25.8.29.51:
		// "i32 :: String" is String and "arr_i :: Array(Int64)" is
		// Array(Int64); no operand base type reaches the result.
		// chgen has no rule for the operator, thus it refuses and
		// points to the CAST form, which it does infer.
		return CHType{}, fmt.Errorf(
			"chgen cannot infer the :: cast operator in %s; write CAST(x AS T) instead; %s",
			clickhouse.Format(expression), pinTypeHint,
		)
	case "->":
		// A lambda has no result type of its own: only the call that
		// receives it has one. A lambda inside a known higher-order
		// array function never arrives here, because that function
		// takes it first. Outside such a call the historic fallback
		// returned the type of the lambda PARAMETER, which is a type
		// for an expression that the server cannot run.
		return CHType{}, fmt.Errorf(
			"the lambda %s has no result type of its own; use it as the argument of a higher-order array function; %s",
			clickhouse.Format(expression), pinTypeHint,
		)
	}
	if operator == "+" || operator == "-" {
		if interval, ok := expression.RightExpr.(*clickhouse.IntervalExpr); ok {
			return inferIntervalArithmeticType(expression.LeftExpr, interval, scope)
		}
		if interval, ok := expression.LeftExpr.(*clickhouse.IntervalExpr); ok && operator == "+" {
			return inferIntervalArithmeticType(expression.RightExpr, interval, scope)
		}
	}
	left, err := inferArithmeticOperandType(expression.LeftExpr, scope)
	if err != nil {
		return CHType{}, err
	}
	right, err := inferArithmeticOperandType(expression.RightExpr, scope)
	if err != nil {
		return CHType{}, err
	}
	// An AggregateFunction state (bare, or nested inside one or more
	// Array wrappers) is not an ordinary value under arithmetic. The
	// server treats "state * N" as a STATE-REPLICATION idiom, not
	// arithmetic: it builds N copies of the state, thus N must be
	// known at plan time. This is the ONLY arithmetic operator that
	// an AggregateFunction state ever survives.
	//
	// This check must run before the generic arithmetic rules below,
	// which would otherwise strip the AggregateFunction wrapper off
	// an Array element (see aggregateStateReplicationResultType).
	if operator == "*" {
		if result, ok := aggregateStateReplicationResultType(
			left, right, expression.LeftExpr, expression.RightExpr, scope,
		); ok {
			return result, nil
		}
	}
	if aggregateStateOperand(left) || aggregateStateOperand(right) {
		// Every other operator, and multiply against anything that
		// is not a non-negative-fitting unsigned integer constant,
		// has no rule. Measured on ClickHouse 25.8.29.51: agg + 2,
		// agg % 2, agg * 2.5, agg * -2, agg * '2', agg * u8 (a
		// COLUMN, not a constant) and groupArray(agg) * i32 all
		// answer code 43 or 44; -groupArray(agg) and
		// abs(groupArray(agg)) do too.
		return CHType{}, fmt.Errorf(
			"chgen cannot infer result type for %s %s %s; an AggregateFunction state only supports multiplication by a non-negative unsigned integer constant",
			left.String(), operator, right.String(),
		)
	}
	switch operator {
	case "+", "-", "*", "/", "%":
		// The arithmetic rules work on the plain types. The
		// LowCardinality wrapper follows the same constant rule as the
		// functions (measured on ClickHouse 25.8.29.51:
		// length(lc) + 300 is LowCardinality(UInt64),
		// u8 * length(lcn) is Nullable(UInt64)).
		leftLowCardinality := strings.EqualFold(left.Name, "LowCardinality") && len(left.Params) == 1
		if leftLowCardinality {
			left = left.Params[0]
		}
		rightLowCardinality := strings.EqualFold(right.Name, "LowCardinality") && len(right.Params) == 1
		if rightLowCardinality {
			right = right.Params[0]
		}
		result, err := inferArithmeticResultType(operator, left, right)
		if err != nil {
			return CHType{}, err
		}
		lowCardinality := (leftLowCardinality && !rightLowCardinality && isConstLiteralExpr(expression.RightExpr, scope)) ||
			(rightLowCardinality && !leftLowCardinality && isConstLiteralExpr(expression.LeftExpr, scope))
		// ClickHouse has no LowCardinality(Decimal), thus a Decimal
		// result drops the wrapper while an integer result keeps it.
		// Measured on ClickHouse 25.8.29.51 with real columns:
		// 1 + length(lower(lc)) is LowCardinality(UInt64), while
		// toDecimal32(toUInt8(255), 2) / length(lower(lc)) is
		// Decimal(9, 2) for every operator and both operand orders.
		if resultRejectsLowCardinality(result) {
			lowCardinality = false
		}
		return applyCHWrappers(result, false, lowCardinality), nil
	}
	if operator == "||" {
		// The concatenation operator IS the concat function. It has no
		// rule of its own: it reads the "concat" entry of the function
		// registry, so that one rule serves both spellings.
		//
		// The two spellings had two rules before, and they diverged:
		// concat(e8, s) gave String while e8 || s gave
		// Enum8('a' = 1, 'zz' = 2). That is a silently wrong type, and
		// the generated Go would scan a String column into the wrong
		// kind of field. One entry cannot diverge from itself.
		//
		// The registry entry gives the base result String, because no
		// operand base type survives: each operand is converted to its
		// string form first. That includes an Enum, whose name would
		// otherwise leak into the result.
		//
		// Measured on ClickHouse 25.8.29.51 with real table columns
		// (e8 Enum8('a'=1,'zz'=2), e16 Enum16('a'=1,'zz'=2),
		// s String, fs FixedString(5), i64 Int64, f64 Float64,
		// d Decimal(10,2)):
		//
		//	e8 || s    String    e8 || e8   String
		//	e8 || i64  String    e8 || f64  String
		//	e16 || s   String    e8 || 'x'  String
		//	i64 || s   String    i64 || i64 String
		//	f64 || s   String    d || s     String
		//	fs || fs   String    fs || s    String
		//
		// The entry is wrapperTransparent, thus the wrappers move from
		// the operands into the result by the same constant rule as a
		// function call. Measured on the same server: 'a' || lc is
		// LowCardinality(String), lc || lc is String, 'a' || ns is
		// Nullable(String), e8 || ns is Nullable(String).
		// The scope goes to the wrapper rule, because a constant
		// operand is not always a literal: a constant condition folds
		// first, thus if(false, s, 'a') is a constant as well.
		rule, ok := functionRuleFor("concat")
		if !ok {
			return CHType{}, fmt.Errorf(
				"the concatenation operator in %s needs the concat entry of the function registry, which is absent; %s",
				clickhouse.Format(expression), pinTypeHint,
			)
		}
		operands := []clickhouse.Expr{expression.LeftExpr, expression.RightExpr}
		operandTypes := []CHType{left, right}
		// The rule reads the BASE type of each operand, exactly as the
		// function path does. inferFunctionType removes the wrapper
		// stack before it calls the rule, thus the operator must remove
		// the same stack, or the two spellings answer differently for
		// the same operands.
		//
		// This matters for the container join. Measured on ClickHouse
		// 25.8.29.51 over a real AggregatingMergeTree column
		// c_arr_i32 SimpleAggregateFunction(anyLast, Array(Int32)):
		//
		//	c_arr_i32 || c_arr_i32   Array(Int32)   [1,2,1,2]
		//	c_arr_s   || c_arr_s     Array(String)  ['a','b','a','b']
		//
		// With the raw operand types the rule sees
		// SimpleAggregateFunction and not Array, thus it takes the
		// string case and answers String, which is a silently wrong
		// type. The wrappers go back on through applyFunctionWrappers
		// below, which still receives the RAW operand types.
		ruleArgs := make([]CHType, 0, len(operandTypes))
		for _, operandType := range operandTypes {
			base, _ := splitWrapperStack(operandType)
			ruleArgs = append(ruleArgs, base)
		}
		result, err := rule(ruleArgs)
		if err != nil {
			return CHType{}, err
		}
		return applyFunctionWrappers(
			result, functionClassFor("concat"), "concat", operands, operandTypes, scope,
		), nil
	}
	// Every remaining operator is an explicit refusal.
	//
	// This position held a historic fallback: a Float operand gave
	// Float64, and if not, the type of the left operand won. That rule was
	// never measured. It made "e8 || s" answer Enum8 until the
	// concatenation operator received its own rule, and it did the same
	// for every other operator that arrived here.
	//
	// The survey in docs/binary-operator-fallback-survey.md lists the
	// whole operator set that the parser can make. The operators that used
	// to arrive here now have a measured rule ("==" and "REGEXP") or a
	// refusal ("::" and "->"), all of them above.
	//
	// A refusal is the correct answer for anything that is left, and for
	// an operator that a later parser version adds. A wrong type is worse
	// than a refusal: a refusal stops the build, while a wrong type makes
	// the generated Go read a column into a field of the wrong kind and
	// tells nobody.
	return CHType{}, fmt.Errorf(
		"chgen has no measured rule for the binary operator %q in %s; %s",
		operator, clickhouse.Format(expression), pinTypeHint,
	)
}

// andOrOperandCarriesBareBool reports whether one operand of an AND or OR
// expression carries a Bool base OUTSIDE a Nullable wrapper and OUTSIDE a
// SimpleAggregateFunction marker that SURVIVES a value read. This is the
// single predicate that inferBinaryOperationType applies to each of the two
// operands of AND and OR; see the measured rule and evidence table there.
//
// An UNPARENTHESISED nested AND/OR of the SAME operator is not a leaf: it
// is one flat operand list on the server, so this function recurses
// straight through such a node into ITS OWN two operands, rather than
// asking the computed (Nullable-wrapped) type of the nested expression. An
// EXPLICITLY parenthesised sub-expression is sealed and stops the
// recursion: the parser marks an explicit paren with a *ParamExprList
// node, which carries no operand of its own to recurse into, so the
// function falls through to inferExprType and asks about the SEALED
// result type instead. This is what makes "(b AND n1) AND n2" answer
// Nullable(UInt8) (the seal hides the bare b) while "b AND n1 AND n2"
// answers Nullable(Bool) (no seal, so the bare b is still visible),
// although the two spellings parse to the identical BinaryOperation tree
// shape once the ParamExprList wrapper is set aside; see the worked
// example in inferBinaryOperationType.
func andOrOperandCarriesBareBool(expr clickhouse.Expr, operator string, scope queryScope) (bool, error) {
	// *ColumnExpr is a transparent wrapper that the parser also uses
	// around a parenthesised child; it carries no seal of its own; see
	// inferExprType, which reads straight through it the same way.
	if column, ok := expr.(*clickhouse.ColumnExpr); ok {
		return andOrOperandCarriesBareBool(column.Expr, operator, scope)
	}
	if nested, ok := expr.(*clickhouse.BinaryOperation); ok &&
		strings.EqualFold(string(nested.Operation), operator) {
		leftHasBool, err := andOrOperandCarriesBareBool(nested.LeftExpr, operator, scope)
		if err != nil {
			return false, err
		}
		if leftHasBool {
			return true, nil
		}
		return andOrOperandCarriesBareBool(nested.RightExpr, operator, scope)
	}
	// A leaf: a column, a literal, a function call, or an EXPLICITLY
	// parenthesised sub-expression (a *ParamExprList, which the switch
	// above does not match). Ask about its own computed type, which
	// already carries the Nullable wrapper and the SimpleAggregateFunction
	// marker that the rule reads through.
	operandType, err := inferExprType(expr, scope)
	if err != nil {
		if errors.Is(err, errPlaceholderResultType) {
			return false, nil
		}
		return false, err
	}
	if inner, ok := simpleAggregateWrapperInner(operandType); ok {
		if !simpleAggregateMarkerSurvives(inner) {
			operandType = inner
		}
	}
	operandBase, operandNullable, _ := splitCHWrappers(operandType)
	return !operandNullable && strings.EqualFold(operandBase.Name, "Bool"), nil
}

// xorOperandCarriesBool reports whether one operand of a call to the
// FUNCTION xor carries a Bool value, for the rule that decides the base
// result type of that call. This is the xor twin of
// andOrOperandCarriesBareBool, and the two rules differ on ONE point: xor
// counts a Bool that sits UNDER a Nullable wrapper, while AND and OR do
// not.
//
// Measured on ClickHouse 25.8.29.51 with real columns, never over a
// literal, which the server folds (nb is a Nullable(Bool) column, u8 is
// UInt8, saf is SimpleAggregateFunction(anyLast, Bool) whose marker
// SURVIVES a value read, safn is SimpleAggregateFunction(anyLast,
// Nullable(Bool)) whose marker does NOT survive):
//
//	xor(nb, nb)     Nullable(Bool)    (AND gives Nullable(UInt8) here)
//	xor(nb, u8)     Nullable(Bool)
//	xor(saf, saf)   UInt8             (the surviving marker hides the Bool)
//	xor(safn, safn) Nullable(Bool)    (the marker does not survive, so the
//	                                   Nullable(Bool) underneath counts)
//
// xor has no infix spelling (measured: "1 xor 0" is Code: 62, a parse
// error), thus this function reads the argument's own computed type
// directly and never needs the parenthesisation walk that
// andOrOperandCarriesBareBool performs for the infix AND/OR chain: a
// function call is already a flat argument list, with no ambiguity for a
// nested xor(...) call to resolve.
func xorOperandCarriesBool(expr clickhouse.Expr, scope queryScope) (bool, error) {
	operandType, err := inferExprType(expr, scope)
	if err != nil {
		if errors.Is(err, errPlaceholderResultType) {
			return false, nil
		}
		return false, err
	}
	if inner, ok := simpleAggregateWrapperInner(operandType); ok {
		if !simpleAggregateMarkerSurvives(inner) {
			operandType = inner
		}
	}
	operandBase, _, _ := splitCHWrappers(operandType)
	return strings.EqualFold(operandBase.Name, "Bool"), nil
}

// isRegexpOperandType reports whether ClickHouse accepts the base type as
// an operand of REGEXP, which is the match function.
//
// Measured on ClickHouse 25.8.29.51 against real columns. String,
// FixedString, Enum8 and Enum16 are legal and give UInt8. Every other type
// that the oracle table holds is rejected with Code: 43: Bool, the whole
// integer family including the wide widths, Float64, Decimal, Date,
// DateTime, UUID, IPv4, Array, Map and Tuple.
func isRegexpOperandType(base CHType) bool {
	switch base.normalizedName() {
	case "string", "fixedstring", "enum8", "enum16", "enum":
		return true
	}
	return false
}

// isLogicOperandType reports whether ClickHouse accepts the base type as
// an operand of AND or OR.
//
// Measured on ClickHouse 25.8.29.51 against real columns in a real
// table. The accepted set is the NARROW integers, the two floats and
// Bool:
//
//	i8 AND i8     UInt8      u8 AND u8      UInt8
//	i32 AND i32   UInt8      u64 AND u64    UInt8
//	i64 AND i64   UInt8      f32 AND f32    UInt8
//	f64 AND f64   UInt8      b AND b        Bool
//
// Every other type is Code: 43, with the message
// "Illegal type (<T>) of 1 argument of function and":
//
//	Int128, UInt128, Int256, UInt256, Decimal(18, 4), String,
//	FixedString, Enum8, Date, DateTime, UUID and Array.
//
// The WIDE integers are why this set is not integerBaseType. That helper
// accepts Int128, Int256, UInt128 and UInt256, because the arithmetic
// aggregates accept them, and all four were measured as Code: 43 here.
// A domain built on integerBaseType would therefore keep on typing four
// cells that the server refuses.
func isLogicOperandType(base CHType) bool {
	if len(base.Params) != 0 {
		return false
	}
	switch base.normalizedName() {
	case "int8", "int16", "int32", "int64", "int",
		"uint8", "uint16", "uint32", "uint64",
		"float32", "float64", "bool", "boolean":
		return true
	}
	return false
}

// inferArithmeticOperandType types one operand of a binary arithmetic
// expression. ClickHouse gives an integer literal the smallest type that
// holds its value (SELECT toTypeName(1) is UInt8, toTypeName(-1) is Int8;
// measured on ClickHouse 25.8.16), so a bare or negated integer literal
// must not take the generic Int64 that inferExprType assigns.
func inferArithmeticOperandType(expression clickhouse.Expr, scope queryScope) (CHType, error) {
	switch expr := expression.(type) {
	case *clickhouse.NumberLiteral:
		// The lexer can fold a leading minus into the literal itself.
		literal, negative := strings.CutPrefix(expr.Literal, "-")
		if literalType, ok := integerLiteralCHType(literal, negative); ok {
			return literalType, nil
		}
	case *clickhouse.UnaryExpr:
		if string(expr.Kind) == "-" {
			if literal, ok := expr.Expr.(*clickhouse.NumberLiteral); ok {
				if literalType, ok := integerLiteralCHType(literal.Literal, true); ok {
					return literalType, nil
				}
			}
		}
	}
	return inferExprType(expression, scope)
}

// integerLiteralCHType returns the smallest ClickHouse integer type that
// holds the literal value, following the ClickHouse literal typing rule.
func integerLiteralCHType(literal string, negative bool) (CHType, bool) {
	if strings.ContainsAny(literal, ".eE") {
		return CHType{}, false
	}
	if negative {
		value, err := strconv.ParseInt("-"+literal, 10, 64)
		if err != nil {
			return CHType{}, false
		}
		switch {
		case value >= math.MinInt8:
			return CHType{Name: "Int8"}, true
		case value >= math.MinInt16:
			return CHType{Name: "Int16"}, true
		case value >= math.MinInt32:
			return CHType{Name: "Int32"}, true
		default:
			return CHType{Name: "Int64"}, true
		}
	}
	value, err := strconv.ParseUint(literal, 10, 64)
	if err != nil {
		return CHType{}, false
	}
	switch {
	case value <= math.MaxUint8:
		return CHType{Name: "UInt8"}, true
	case value <= math.MaxUint16:
		return CHType{Name: "UInt16"}, true
	case value <= math.MaxUint32:
		return CHType{Name: "UInt32"}, true
	default:
		return CHType{Name: "UInt64"}, true
	}
}
