package engine

import (
	"strconv"
	"strings"

	clickhouse "github.com/AfterShip/clickhouse-sql-parser/parser"
)

// renderedNumberStandIn stands for the text that ClickHouse prints for a
// numeric literal, whose exact form is not modelled. It holds a NUL
// byte, which no SQL string literal in these grammars can hold, so that
// a real string literal can never be mistaken for it.
const renderedNumberStandIn = "\x00rendered-number"

// unwrapConstantOperand removes the wrappers that carry no meaning for a
// constant fold: the ColumnExpr shell of an argument, and the
// ParamExprList that a redundant pair of parentheses makes. The grammars
// parenthesise nearly every operand, thus `(false AND (e16 = 'x'))`
// reaches this code as a ParamExprList around a BinaryOperation.
//
// A ParamExprList is only a parenthesis when it holds exactly one item
// and no ColumnArgList. Any other shape is an argument list, thus it
// stays as it is and does not fold.
func unwrapConstantOperand(expression clickhouse.Expr) clickhouse.Expr {
	for {
		switch expr := expression.(type) {
		case *clickhouse.ColumnExpr:
			expression = expr.Expr
		case *clickhouse.ParamExprList:
			if expr.ColumnArgList != nil || expr.Items == nil || len(expr.Items.Items) != 1 {
				return expression
			}
			expression = expr.Items.Items[0]
		default:
			return expression
		}
	}
}

// constantConditionTruth tells whether the condition of an `if` folds to
// a constant, and which way it folds.
//
// ClickHouse folds the constants of a query before it gives the branches
// of an `if` a type. When the condition is constant, the server removes
// the branch that the condition does not take, and it removes that
// branch BEFORE the branch gets a type. Thus a dead branch that has no
// type at all does not stop the expression. Measured on ClickHouse
// 25.8.29.51 with the real columns of the fixture table:
//
//	if(false, if(b, i64, f64), u8)   UInt8
//	if(true,  u8, if(b, i64, f64))   UInt8
//	if(b,     if(b, i64, f64), u8)   Code: 386
//
// Only `if` does this. multiIf and CASE give a type to every branch, and
// a constant condition does not save them:
//
//	multiIf(false, if(b, i64, f64), b, u8, u16)        Code: 386
//	CASE WHEN false THEN if(b, i64, f64) ELSE u8 END   Code: 386
//
// This function is deliberately conservative. It reports a constant only
// for the forms measured against the server, and it reports "not
// constant" for everything else. A false "not constant" keeps the
// earlier behaviour, which is a refusal at worst. A false "constant"
// would drop a branch that the server types, thus it could turn a
// refusal into a wrong type, which is the worse failure.
func constantConditionTruth(expression clickhouse.Expr, scope queryScope) (truth bool, constant bool) {
	switch expr := unwrapConstantOperand(expression).(type) {
	case *clickhouse.Ident:
		// The parser gives the keywords `true` and `false` an Ident,
		// not a BoolLiteral. A scalar or a column of the same name
		// shadows the keyword, exactly as in inferExprType, thus the
		// name is a constant only when nothing shadows it.
		if _, shadowed := scope.lookupScalar(expr.Name); shadowed {
			return false, false
		}
		if _, err := scope.lookupColumn("", expr.Name); err == nil {
			return false, false
		}
		switch strings.ToUpper(expr.Name) {
		case "TRUE":
			return true, true
		case "FALSE":
			return false, true
		}
		return false, false
	case *clickhouse.BoolLiteral:
		switch strings.ToUpper(strings.TrimSpace(expr.Literal)) {
		case "TRUE":
			return true, true
		case "FALSE":
			return false, true
		}
		return false, false
	case *clickhouse.NumberLiteral:
		// A numeric condition is false only when it is zero.
		text := strings.TrimSpace(expr.Literal)
		if value, err := strconv.ParseFloat(text, 64); err == nil {
			return value != 0, true
		}
		return false, false
	case *clickhouse.UnaryExpr:
		switch strings.ToUpper(strings.TrimSpace(string(expr.Kind))) {
		case "NOT":
			if inner, ok := constantConditionTruth(expr.Expr, scope); ok {
				return !inner, true
			}
		case "-":
			if literal, ok := expr.Expr.(*clickhouse.NumberLiteral); ok {
				if value, err := strconv.ParseFloat(strings.TrimSpace(literal.Literal), 64); err == nil {
					return value != 0, true
				}
			}
		}
		return false, false
	case *clickhouse.BinaryOperation:
		return constantBinaryTruth(expr, scope)
	case *clickhouse.FunctionExpr, *clickhouse.CastExpr:
		return constantCallTruth(expr, scope)
	}
	return false, false
}

// constantCallTruth folds a condition that is a call, which the earlier
// code always reported as "not constant". That refusal was the cause of
// the false refusals of the fuzz oracle, because the grammars build a
// condition such as `empty(concat('abc', ”))` or
// `CAST(10000000000 AS Int64) <= nullIf(4000000000, 255)`.
//
// The rule that the server uses is NARROWER than isConstant, and this
// matters. isConstant reports 1 for every call whose arguments are
// constants, but the server drops the dead branch only for SOME of
// them. Measured on 25.8.29.51 with the real fixture columns, with the
// live branch second and the dead branch third:
//
//	if(toUInt8(2),    u8, if(b, i64, f64))   UInt8       folds
//	if(toUInt16(2),   u8, if(b, i64, f64))   Code: 386   does NOT fold
//	if(toInt64(2),    u8, if(b, i64, f64))   Code: 386   does NOT fold
//	if(toFloat64(2),  u8, if(b, i64, f64))   Code: 386   does NOT fold
//	if(length('abc'), u8, if(b, i64, f64))   Code: 386   does NOT fold
//
// Every one of those conditions has isConstant = 1 and a non-zero
// value, thus truth alone does not decide the fold. The TYPE of the
// constant decides it. A condition of type UInt8, Bool, or Nullable of
// those, needs no conversion, and the server folds it. Any other
// numeric type needs a conversion first, and that conversion stops the
// fold. Measured: toUInt8(2), CAST(2 AS UInt8), toBool(2),
// toNullable(toUInt8(1)) and nullIf(toUInt8(1), 255) all fold, while
// the wider and the signed types above do not.
//
// A plain numeric literal keeps its own path in constantConditionTruth
// and is not affected: the parser gives 2 and 3 the type UInt8, and the
// server folds them.
//
// A non-deterministic function never folds here, although the server
// may report isConstant = 1 for it. Measured: isConstant(now()) is 1,
// and the server does fold if(now() > toDateTime(0), u8, ...). chgen
// must NOT. The value of now() depends on the moment the query runs,
// thus a condition such as `now() > '2030-01-01'` takes one branch
// today and the other branch later. chgen would have to predict which
// branch the server drops, and a wrong prediction drops a branch that
// the server types, which turns a refusal into a WRONG TYPE. That is
// the failure that the rule of this file forbids, thus determinism is
// required even where the server itself folds. rand(), randomString()
// and generateUUIDv4() are excluded for the same reason; the server
// reports isConstant = 0 for the first two, but the list does not
// depend on that report.
func constantCallTruth(expression clickhouse.Expr, scope queryScope) (bool, bool) {
	value, ok := constantCallValue(expression, scope)
	if !ok {
		return false, false
	}
	return value != 0, true
}

// constantConditionCallNames lists the functions that fold a constant
// condition. Each name was measured on ClickHouse 25.8.29.51, and each
// has a regression test. All of them are deterministic: for fixed
// arguments they give the same answer at every moment. A function that
// is not in this list does not fold, which costs at worst a refusal.
//
// The list holds only functions that give UInt8 or Bool for constant
// arguments, because only such a result folds. `nullIf` is here because
// it keeps the type of its first argument, thus nullIf of a UInt8 gives
// Nullable(UInt8), which folds.
var constantConditionCallNames = map[string]struct{}{
	"empty":           {},
	"notempty":        {},
	"isnull":          {},
	"isnotnull":       {},
	"not":             {},
	"nullif":          {},
	"ifnull":          {},
	"touint8":         {},
	"tobool":          {},
	"equals":          {},
	"notequals":       {},
	"less":            {},
	"greater":         {},
	"lessorequals":    {},
	"greaterorequals": {},
	"and":             {},
	"or":              {},
}

// constantCallValue evaluates a constant call whose result is UInt8,
// Bool or Nullable of those, and gives the value as a number, where 0
// is false. It reports false when it cannot prove the value, which is
// the safe answer.
func constantCallValue(expression clickhouse.Expr, scope queryScope) (float64, bool) {
	switch expr := unwrapConstantOperand(expression).(type) {
	case *clickhouse.CastExpr:
		// A CAST folds only when the target type is UInt8 or Bool, for
		// the reason in the doc comment of constantCallTruth. The
		// argument must itself be a constant.
		typeName, ok := castTargetTypeName(expr)
		if !ok {
			return 0, false
		}
		switch strings.ToUpper(strings.TrimSpace(typeName)) {
		case "UINT8", "BOOL", "BOOLEAN":
		default:
			return 0, false
		}
		value, ok := constantOperandValue(expr.Expr, scope)
		if !ok {
			return 0, false
		}
		// The cast keeps the low byte, thus a multiple of 256 is 0.
		return float64(int64(value) & 0xff), true
	case *clickhouse.FunctionExpr:
		name := strings.ToLower(expr.Name.Name)
		if _, allowed := constantConditionCallNames[name]; !allowed {
			return 0, false
		}
		args := functionArgs(expr)
		switch name {
		case "empty", "notempty":
			if len(args) != 1 {
				return 0, false
			}
			text, ok := constantString(args[0])
			if !ok {
				return 0, false
			}
			// The exact text of a rendered number is not modelled, but
			// it is never empty, thus the answer is still decided.
			isEmpty := text != renderedNumberStandIn && len(text) == 0
			if name == "notempty" {
				isEmpty = !isEmpty
			}
			if isEmpty {
				return 1, true
			}
			return 0, true
		case "isnull", "isnotnull":
			// None of the constants that this file evaluates is NULL,
			// thus isNull is false. A NULL keyword is not evaluated
			// here, thus it never reaches this point.
			if len(args) != 1 {
				return 0, false
			}
			if _, ok := constantOperandValue(args[0], scope); !ok {
				return 0, false
			}
			if name == "isnull" {
				return 0, true
			}
			return 1, true
		case "not":
			if len(args) != 1 {
				return 0, false
			}
			truth, ok := constantConditionTruth(args[0], scope)
			if !ok {
				return 0, false
			}
			if truth {
				return 0, true
			}
			return 1, true
		case "touint8", "tobool":
			if len(args) != 1 {
				return 0, false
			}
			value, ok := constantOperandValue(args[0], scope)
			if !ok {
				return 0, false
			}
			if name == "tobool" {
				if value != 0 {
					return 1, true
				}
				return 0, true
			}
			return float64(int64(value) & 0xff), true
		case "nullif":
			// nullIf(a, b) keeps the type of a. It gives NULL when the
			// two are equal, and NULL takes the false branch.
			if len(args) != 2 {
				return 0, false
			}
			left, leftOK := constantOperandValue(args[0], scope)
			right, rightOK := constantOperandValue(args[1], scope)
			if !leftOK || !rightOK {
				return 0, false
			}
			if left == right {
				return 0, true
			}
			return left, true
		case "ifnull":
			// Neither operand of a constant that this file evaluates
			// is NULL, thus the first one wins.
			if len(args) != 2 {
				return 0, false
			}
			left, ok := constantOperandValue(args[0], scope)
			if !ok {
				return 0, false
			}
			if _, ok := constantOperandValue(args[1], scope); !ok {
				return 0, false
			}
			return left, true
		case "and", "or":
			if len(args) != 2 {
				return 0, false
			}
			operator := "AND"
			if name == "or" {
				operator = "OR"
			}
			truth, ok := constantBinaryTruth(&clickhouse.BinaryOperation{
				LeftExpr:  args[0],
				RightExpr: args[1],
				Operation: clickhouse.TokenKind(operator),
			}, scope)
			if !ok {
				return 0, false
			}
			if truth {
				return 1, true
			}
			return 0, true
		default:
			// The comparison functions: equals, less and the rest.
			if len(args) != 2 {
				return 0, false
			}
			operator, ok := comparisonFunctionOperator(name)
			if !ok {
				return 0, false
			}
			truth, ok := constantComparisonTruth(operator, args[0], args[1])
			if !ok {
				return 0, false
			}
			if truth {
				return 1, true
			}
			return 0, true
		}
	}
	return 0, false
}

// comparisonFunctionOperator maps the call form of a comparison to the
// operator text that constantComparisonTruth uses.
func comparisonFunctionOperator(name string) (string, bool) {
	switch name {
	case "equals":
		return "=", true
	case "notequals":
		return "!=", true
	case "less":
		return "<", true
	case "lessorequals":
		return "<=", true
	case "greater":
		return ">", true
	case "greaterorequals":
		return ">=", true
	}
	return "", false
}

// castTargetTypeName gives the name of the type that a CAST targets.
func castTargetTypeName(expr *clickhouse.CastExpr) (string, bool) {
	switch target := expr.AsType.(type) {
	case *clickhouse.StringLiteral:
		return strings.Trim(target.Literal, "'"), true
	case clickhouse.ColumnType:
		parsed, err := parseCHType(target)
		if err != nil {
			return "", false
		}
		return parsed.Name, true
	}
	return "", false
}

// constantOperandValue reads the numeric value of a constant operand of
// a call. It accepts a plain number, and a call that this file can
// evaluate, thus a nest such as nullIf(toUInt8(1), 255) folds. A column
// or any other expression reports false.
func constantOperandValue(expression clickhouse.Expr, scope queryScope) (float64, bool) {
	if value, ok := constantNumber(expression); ok {
		return value, true
	}
	switch unwrapConstantOperand(expression).(type) {
	case *clickhouse.FunctionExpr, *clickhouse.CastExpr:
		return constantCallValue(expression, scope)
	case *clickhouse.Ident, *clickhouse.BoolLiteral:
		// `true` and `false` reach this point as an Ident, unless a
		// column or a scalar shadows the name.
		if truth, ok := constantConditionTruth(expression, scope); ok {
			if truth {
				return 1, true
			}
			return 0, true
		}
	}
	return 0, false
}

// constantBinaryTruth folds the binary conditions that the fixture
// grammars build. AND and OR fold on one constant side alone, because a
// constant false with AND and a constant true with OR decide the result
// whatever the other side is. Measured on 25.8.29.51:
//
//	if(b AND false, if(b, i64, f64), u8)   UInt8
//	if(1 > 2, if(b, i64, f64), u8)         UInt8
//
// A comparison folds only when BOTH sides are constants of one kind.
func constantBinaryTruth(expression *clickhouse.BinaryOperation, scope queryScope) (bool, bool) {
	operator := strings.ToUpper(strings.TrimSpace(string(expression.Operation)))
	switch operator {
	case "AND":
		leftTruth, leftConstant := constantConditionTruth(expression.LeftExpr, scope)
		if leftConstant && !leftTruth {
			return false, true
		}
		rightTruth, rightConstant := constantConditionTruth(expression.RightExpr, scope)
		if rightConstant && !rightTruth {
			return false, true
		}
		if leftConstant && rightConstant {
			return true, true
		}
		return false, false
	case "OR":
		leftTruth, leftConstant := constantConditionTruth(expression.LeftExpr, scope)
		if leftConstant && leftTruth {
			return true, true
		}
		rightTruth, rightConstant := constantConditionTruth(expression.RightExpr, scope)
		if rightConstant && rightTruth {
			return true, true
		}
		if leftConstant && rightConstant {
			return false, true
		}
		return false, false
	case "=", "==", "!=", "<>", "<", "<=", ">", ">=":
		return constantComparisonTruth(operator, expression.LeftExpr, expression.RightExpr)
	}
	return false, false
}

// constantComparisonTruth folds a comparison of two constant operands.
// It handles a numeric pair and a string pair only. A mixed pair, or an
// operand that is not a plain literal, reports "not constant".
func constantComparisonTruth(operator string, left, right clickhouse.Expr) (bool, bool) {
	if leftValue, ok := constantComparisonNumber(left); ok {
		if rightValue, ok := constantComparisonNumber(right); ok {
			return compareOrdered(operator, leftValue, rightValue), true
		}
		return false, false
	}
	leftText, leftOK := constantString(left)
	rightText, rightOK := constantString(right)
	if !leftOK || !rightOK {
		return false, false
	}
	leftStandIn := leftText == renderedNumberStandIn
	rightStandIn := rightText == renderedNumberStandIn
	switch {
	case !leftStandIn && !rightStandIn:
		// Two exact texts. The comparison is decided.
		return compareOrdered(operator, leftText, rightText), true
	case leftStandIn && rightStandIn:
		// Two stand-in texts would compare as equal although the two
		// renderings need not be, thus this pair does not fold.
		return false, false
	}
	// One side is a rendered number, whose exact text is not modelled,
	// and the other is an exact text. Only a comparison against the
	// EMPTY string folds: a rendered number is never empty (measured on
	// 25.8.29.51: toString(0.5) is '0.5' and toString(0) is '0'), and
	// that single fact decides the result. A comparison against any
	// other string needs the exact rendering, thus it does not fold.
	if leftStandIn {
		if rightText != "" {
			return false, false
		}
		// The rendering is non-empty, thus it is greater than "".
		return compareOrdered(operator, 1, 0), true
	}
	if leftText != "" {
		return false, false
	}
	return compareOrdered(operator, 0, 1), true
}

func compareOrdered[T int | float64 | string](operator string, left, right T) bool {
	switch operator {
	case "=", "==":
		return left == right
	case "!=", "<>":
		return left != right
	case "<":
		return left < right
	case "<=":
		return left <= right
	case ">":
		return left > right
	default: // ">="
		return left >= right
	}
}

// constantComparisonValueNames lists the calls whose numeric VALUE this
// file can read for a comparison operand. A comparison of two constants
// gives UInt8, or Nullable(UInt8) when an operand is nullable, and both
// of those fold, thus the type of the operand itself does not need to
// be UInt8 here. Only the value must be exact.
//
// Every name is deterministic and value-preserving for the arguments
// that reach it. Measured on 25.8.29.51:
//
//	CAST(10000000000 AS Int64)             10000000000
//	nullIf(4000000000, 255)                4000000000
//	toInt64(5)                             5
//
// A widening CAST such as CAST(x AS Int64) keeps the value, thus it is
// read here although it does NOT fold as a condition on its own. A
// NARROWING cast does not keep the value (measured:
// CAST(300 AS UInt8) is 44), thus only the casts listed below are read,
// and a cast to a narrower type is refused.
var constantComparisonValueNames = map[string]struct{}{
	"toint8":    {},
	"toint16":   {},
	"toint32":   {},
	"toint64":   {},
	"touint8":   {},
	"touint16":  {},
	"touint32":  {},
	"touint64":  {},
	"tofloat32": {},
	"tofloat64": {},
	"nullif":    {},
	"ifnull":    {},
}

// constantComparisonNumber reads the numeric value of a comparison
// operand. It accepts a plain literal, and the deterministic calls of
// constantComparisonValueNames whose own arguments are constants.
// Anything else, in particular a column, reports false.
func constantComparisonNumber(expression clickhouse.Expr) (float64, bool) {
	if value, ok := constantNumber(expression); ok {
		return value, true
	}
	switch expr := unwrapConstantOperand(expression).(type) {
	case *clickhouse.CastExpr:
		typeName, ok := castTargetTypeName(expr)
		if !ok {
			return 0, false
		}
		value, ok := constantComparisonNumber(expr.Expr)
		if !ok {
			return 0, false
		}
		// Only a cast that keeps the value is read. A narrowing cast
		// changes it, thus it is refused rather than modelled.
		if !castKeepsNumericValue(typeName, value) {
			return 0, false
		}
		return value, true
	case *clickhouse.FunctionExpr:
		name := strings.ToLower(expr.Name.Name)
		if _, allowed := constantComparisonValueNames[name]; !allowed {
			return 0, false
		}
		args := functionArgs(expr)
		switch name {
		case "nullif":
			if len(args) != 2 {
				return 0, false
			}
			left, leftOK := constantComparisonNumber(args[0])
			right, rightOK := constantComparisonNumber(args[1])
			if !leftOK || !rightOK || left == right {
				// An equal pair gives NULL, whose comparison is NULL,
				// which this file does not model. It does not fold.
				return 0, false
			}
			return left, true
		case "ifnull":
			if len(args) != 2 {
				return 0, false
			}
			left, ok := constantComparisonNumber(args[0])
			if !ok {
				return 0, false
			}
			if _, ok := constantComparisonNumber(args[1]); !ok {
				return 0, false
			}
			return left, true
		default:
			// The toXxx conversions. One argument, and the value must
			// survive the conversion.
			if len(args) != 1 {
				return 0, false
			}
			value, ok := constantComparisonNumber(args[0])
			if !ok {
				return 0, false
			}
			if !castKeepsNumericValue(strings.TrimPrefix(name, "to"), value) {
				return 0, false
			}
			return value, true
		}
	}
	return 0, false
}

// castKeepsNumericValue tells whether a conversion to the named type
// keeps the value exactly. A value outside the range of the target type
// wraps or saturates, thus it is refused rather than modelled.
func castKeepsNumericValue(typeName string, value float64) bool {
	var low, high float64
	switch strings.ToUpper(strings.TrimSpace(typeName)) {
	case "INT8":
		low, high = -128, 127
	case "INT16":
		low, high = -32768, 32767
	case "INT32":
		low, high = -2147483648, 2147483647
	case "INT64":
		low, high = -9223372036854775808, 9223372036854775807
	case "UINT8":
		low, high = 0, 255
	case "UINT16":
		low, high = 0, 65535
	case "UINT32":
		low, high = 0, 4294967295
	case "UINT64":
		low, high = 0, 18446744073709551615
	case "FLOAT32", "FLOAT64":
		// A float keeps the value of the literals that the grammars
		// build, which are already read as float64.
		return true
	default:
		return false
	}
	return value >= low && value <= high
}

// constantNumber reads a numeric literal, with an optional leading minus.
func constantNumber(expression clickhouse.Expr) (float64, bool) {
	switch expr := unwrapConstantOperand(expression).(type) {
	case *clickhouse.NumberLiteral:
		value, err := strconv.ParseFloat(strings.TrimSpace(expr.Literal), 64)
		return value, err == nil
	case *clickhouse.UnaryExpr:
		if strings.TrimSpace(string(expr.Kind)) == "-" {
			if value, ok := constantNumber(expr.Expr); ok {
				return -value, true
			}
		}
	}
	return 0, false
}

// constantString reads a string literal, and the toString of a numeric
// literal, because the grammars build that form. Nothing else folds: a
// column, or any other function, is not a constant.
func constantString(expression clickhouse.Expr) (string, bool) {
	switch expr := unwrapConstantOperand(expression).(type) {
	case *clickhouse.StringLiteral:
		return strings.Trim(expr.Literal, "'"), true
	case *clickhouse.FunctionExpr:
		// concat of constant strings gives a constant string, whose
		// exact text IS known when every part is an exact text.
		// Measured on 25.8.29.51: concat('abc', '') is 'abc' and
		// empty(concat('abc', '')) is 0.
		if strings.EqualFold(expr.Name.Name, "concat") {
			var builder strings.Builder
			args := functionArgs(expr)
			if len(args) == 0 {
				return "", false
			}
			for _, arg := range args {
				text, ok := constantString(arg)
				// A rendered number has no modelled text, thus a
				// concat that holds one has no modelled text either.
				if !ok || text == renderedNumberStandIn {
					return "", false
				}
				builder.WriteString(text)
			}
			return builder.String(), true
		}
		if !strings.EqualFold(expr.Name.Name, "toString") {
			return "", false
		}
		if expr.Params == nil || expr.Params.Items == nil || len(expr.Params.Items.Items) != 1 {
			return "", false
		}
		// The exact text that ClickHouse prints for a number is not
		// modelled. A stand-in text is returned, and the caller uses
		// it only against the empty string literal, where the single
		// fact that a rendering is never empty decides the result.
		if _, ok := constantNumber(expr.Params.Items.Items[0]); ok {
			return renderedNumberStandIn, true
		}
	}
	return "", false
}
