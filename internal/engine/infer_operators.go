package engine

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	clickhouse "github.com/AfterShip/clickhouse-sql-parser/parser"
)

// inferUnaryExprType types a unary minus or NOT expression. Measured on
// ClickHouse 25.8.29.51:
//
//	-1        -> Int8   (the minus folds into the bare literal)
//	-(1)      -> Int16  (negate widens UInt8)
//	-(u16)    -> Int32
//	-(u32)    -> Int64
//	-(u64)    -> Int64
//	-(i8)     -> Int8   (the signed types keep their width)
//	-(b)      -> Int16
//	-(f32)    -> Float32
//	-(dec)    -> Decimal(18, 4)
//	-(ni32)   -> Nullable(Int32)         (the wrappers stay)
//	-(length(lc)) -> LowCardinality(Int64)
//	NOT b     -> Bool
//	NOT u8    -> UInt8
//	NOT ni32  -> Nullable(UInt8)
//	NOT (lcn = 'a') -> LowCardinality(Nullable(UInt8))
//
// ClickHouse rejects -(s), -(d) and NOT dec, so a non-numeric operand is
// an explicit inference error.
func inferUnaryExprType(expression *clickhouse.UnaryExpr, scope queryScope) (CHType, error) {
	operator := string(expression.Kind)
	if operator == "-" {
		// A minus directly on a number literal folds into the
		// literal and takes the smallest type that holds the value.
		if literal, ok := expression.Expr.(*clickhouse.NumberLiteral); ok {
			if strings.ContainsAny(literal.Literal, ".eE") {
				return CHType{Name: "Float64"}, nil
			}
			inner, negative := strings.CutPrefix(literal.Literal, "-")
			if literalType, ok := integerLiteralCHType(inner, !negative); ok {
				return literalType, nil
			}
			return CHType{}, fmt.Errorf("cannot infer type for negated literal %q", literal.Literal)
		}
		operandType, err := inferExprType(expression.Expr, scope)
		if err != nil {
			return CHType{}, err
		}
		// A SimpleAggregateFunction(f, T) operand negates as T alone.
		// The unary group UNWRAPS, unlike the branch group in
		// supertype.go, which KEEPS the wrapper. Keep the two rules
		// apart: they need opposite answers.
		//
		// Measured on ClickHouse 25.8.29.51 with real table columns,
		// never over literals, because the server folds constants. Each
		// answer equals the answer for the inner type alone (sagg is
		// SimpleAggregateFunction(sum, Int64), saggu is
		// SimpleAggregateFunction(max, UInt8), saggf is
		// SimpleAggregateFunction(sum, Float64), saggn is
		// SimpleAggregateFunction(sum, Nullable(Int64))):
		//
		//	-sagg    Int64             -i64   Int64
		//	-saggu   Int16             -u8    Int16
		//	-saggf   Float64           -f64   Float64
		//	-saggn   Nullable(Int64)   -ni64  Nullable(Int64)
		//
		// The unwrap does not widen the refusal boundary. A non-numeric
		// inner type stays refused, and the server refuses it too:
		// -saggs answers Code: 43 ("Illegal type
		// SimpleAggregateFunction(min, String) of argument of function
		// negate").
		operandType = simpleAggregateInnerType(operandType)
		base, nullable, lowCardinality := splitCHWrappers(operandType)
		var result CHType
		switch strings.ToLower(base.Name) {
		case "uint8", "bool":
			result = CHType{Name: "Int16"}
		case "uint16":
			result = CHType{Name: "Int32"}
		case "uint32", "uint64":
			result = CHType{Name: "Int64"}
		// Measured on ClickHouse 25.8.29.51 with real columns: the whole wide
		// integer family negates, and an unsigned wide operand gives the
		// signed type of the same width. A sized Decimal keeps its own type,
		// thus the group needs every spelling, not the bare "decimal" alias.
		case "int8", "int16", "int32", "int64", "int128", "int256",
			"float32", "float64",
			"decimal", "decimal32", "decimal64", "decimal128", "decimal256":
			result = base
		case "uint128":
			result = CHType{Name: "Int128"}
		case "uint256":
			result = CHType{Name: "Int256"}
		default:
			return CHType{}, fmt.Errorf("cannot negate an operand of type %s", operandType.String())
		}
		return applyCHWrappers(result, nullable, lowCardinality), nil
	}
	if strings.EqualFold(operator, "NOT") {
		operandType, err := inferExprType(expression.Expr, scope)
		if err != nil {
			return CHType{}, err
		}
		base, nullable, lowCardinality := splitCHWrappers(operandType)
		var result CHType
		switch strings.ToLower(base.Name) {
		case "bool":
			result = CHType{Name: "Bool"}
		case "uint8", "uint16", "uint32", "uint64",
			"int8", "int16", "int32", "int64",
			"float32", "float64":
			result = CHType{Name: "UInt8"}
		case "dynamic":
			// Measured on ClickHouse 25.8.29.51 with a real Dynamic
			// column. Both toTypeName and execution accept NOT dyn and
			// not(dyn), and the result is Nullable(UInt8). Dynamic can
			// hold NULL without a Nullable wrapper, so this rule must add
			// the result wrapper explicitly.
			result = CHType{Name: "UInt8"}
			nullable = true
		default:
			return CHType{}, fmt.Errorf("cannot apply NOT to an operand of type %s", operandType.String())
		}
		return applyCHWrappers(result, nullable, lowCardinality), nil
	}
	return CHType{}, fmt.Errorf("cannot infer type for unary operator %q", operator)
}

// nullIfFunctionResult gives the Nullable form of the first argument
// type. Measured on ClickHouse 25.8.29.51: nullIf(u8, i64) is
// Nullable(UInt8), nullIf(lc, 'a') is LowCardinality(Nullable(String)),
// nullIf(lcn, s) is Nullable(String).
//
// nullIf(a, b) is "a = b ? NULL : a", thus it compares its two arguments
// and needs a comparable pair. That check is NOT in this function: it
// needs the argument EXPRESSIONS and not only their types, because a
// CONSTANT operand folds into the type of the other side and stays
// legal. The check runs in inferFunctionType, where the expressions and
// the scope are in hand. See checkComparableOperands.
func nullIfFunctionResult(args []CHType) (CHType, error) {
	first, err := firstFunctionArgument(args)
	if err != nil {
		return CHType{}, err
	}
	// nullIf can replace the first value with NULL. The first type must
	// therefore be legal inside Nullable. Dropping the wrapper is not a
	// valid result rule here: the server refuses the call instead.
	//
	// Measured on ClickHouse 25.8.29.51 over real columns with both
	// witnesses:
	//
	//	nullIf(dyn, nd)          Code: 43
	//	nullIf(dyn, dyn)         Code: 43
	//	nullIf(variant, variant) Code: 43
	//	nullIf(i32, dyn)         Nullable(Int32), accepted
	//
	// The last cell proves that Dynamic is legal in the comparison
	// position. The refusal is about the first argument and the result
	// wrapper, not about every Dynamic argument.
	if !canBeInsideNullable(first) {
		return CHType{}, fmt.Errorf(
			"function nullIf cannot return NULL for a first argument of type %s because ClickHouse cannot put that type inside Nullable; %s",
			first.String(), pinTypeHint,
		)
	}
	// The rule gives the BARE base type back. It does NOT add the
	// Nullable itself.
	//
	// nullIf makes the value NULL when the two arguments are equal, thus
	// the result is always nullable. That is a WRAPPER decision, and
	// every wrapper decision belongs to the transport and the ONE
	// applier. nullIfTransport declares nullable: wrapperKeep and the
	// call site adds the flag, thus the Nullable still always appears.
	//
	// The rule added the Nullable itself until the regression. That put the
	// Nullable in the wrong PLACE for a SimpleAggregateFunction
	// argument. The applier can only rebuild the marker AROUND a
	// finished result, so a Nullable that the rule had already applied
	// ended up INSIDE the marker, where the server puts it outside.
	//
	// The applier now owns the position, and it puts the Nullable where
	// the argument had it. Measured on ClickHouse 25.8.29.51 with real
	// columns (saf is SimpleAggregateFunction(anyLast, Int32), safn is
	// SimpleAggregateFunction(anyLast, Nullable(Int32))):
	//
	//	nullIf(saf, saf)    Nullable(SimpleAggregateFunction(anyLast, Int32))
	//	nullIf(safn, safn)  SimpleAggregateFunction(anyLast, Nullable(Int32))
	//
	// A bare inner type takes the Nullable OUTSIDE the marker. An inner
	// type that is already Nullable takes it INSIDE, and no second
	// Nullable is added. See nullIfTransport for the full grid.
	return first, nil
}

// inferSubscriptType types a subscript access such as arr[1] or m['k'].
// Measured on ClickHouse 25.8.29.51:
//
//	arr_i[1]    -> Int32            (the element type)
//	arr_i[ni32] -> Nullable(Int32)  (a Nullable index wraps the result)
//	m['k']      -> Int64            (the value type)
//	m[ns]       -> Nullable(Int64)
//	m[lc]       -> Int64            (a LowCardinality key does not wrap)
//	s[1]        -> rejected by ClickHouse
//
// isCHTuple reports whether the type is a Tuple with known element types.
func isCHTuple(columnType CHType) bool {
	return strings.EqualFold(columnType.Name, "Tuple") && len(columnType.Params) > 0
}

// tupleElementByIndex returns the type of the one-based element index of a
// Tuple. Measured on ClickHouse 25.8.29.51 over real table columns:
//
//	tup Tuple(Int32, String)   tup.1 is Int32, tup.2 is String
//	                           tup.0 and tup.3 are a ClickHouse error
//	                           (NOT_FOUND_COLUMN_IN_BLOCK)
//
// The index is one-based, and an index outside the range is an error on the
// ClickHouse side, thus chgen refuses instead of inventing a type.
//
// keepLowCardinality tells whether the access form keeps the LowCardinality
// wrapper of the element. Only the dot-with-a-name form keeps it; the
// index form and every tupleElement form drop it (measured:
// nn Tuple(a Nullable(Int32), b LowCardinality(String)) gives
// LowCardinality(String) for nn.b, and String for nn.2,
// tupleElement(nn, 2) and tupleElement(nn, 'b')). A Nullable element is
// kept by every form.
func tupleElementByIndex(tupleType CHType, index int, keepLowCardinality bool) (CHType, error) {
	if index < 1 || index > len(tupleType.Params) {
		return CHType{}, fmt.Errorf(
			"%s has no element with index %d; ClickHouse counts the elements from 1",
			tupleType.String(), index,
		)
	}
	return tupleElementResult(tupleType.Params[index-1], keepLowCardinality), nil
}

// tupleElementByName returns the type of the named element of a Tuple.
// A Tuple whose declaration gives no element names has no name to match,
// so the access refuses.
func tupleElementByName(tupleType CHType, name string, keepLowCardinality bool) (CHType, error) {
	if len(tupleType.ParamNames) == 0 {
		return CHType{}, fmt.Errorf(
			"%s has unnamed elements, so it has no element %q; use the index form",
			tupleType.String(), name,
		)
	}
	for index, elementName := range tupleType.ParamNames {
		if elementName == name && index < len(tupleType.Params) {
			return tupleElementResult(tupleType.Params[index], keepLowCardinality), nil
		}
	}
	return CHType{}, fmt.Errorf("%s has no element with name %q", tupleType.String(), name)
}

func tupleElementResult(elementType CHType, keepLowCardinality bool) CHType {
	if keepLowCardinality {
		return elementType
	}
	base, nullable, _ := splitCHWrappers(elementType)
	return applyCHWrappers(base, nullable, false)
}

// inferIndexOperationType types the dot form of the tuple element access,
// for example tup.1. The parser gives this form an IndexOperation node with
// the "." operation. The index is a constant integer: ClickHouse refuses a
// non-constant index outright ("Second argument to tupleElement must be a
// constant UInt or String").
func inferIndexOperationType(expression *clickhouse.IndexOperation, scope queryScope) (CHType, error) {
	if expression.Operation != "." {
		return CHType{}, fmt.Errorf(
			"cannot infer type for index operation %q (%s); %s",
			expression.Operation, clickhouse.Format(expression), pinTypeHint,
		)
	}
	objectType, err := inferExprType(expression.Object, scope)
	if err != nil {
		return CHType{}, err
	}
	if !isCHTuple(objectType) {
		return CHType{}, fmt.Errorf(
			"cannot read an element of %s: the dot form needs a Tuple with known element types",
			objectType.String(),
		)
	}
	switch index := expression.Index.(type) {
	case *clickhouse.NumberLiteral:
		position, err := strconv.Atoi(index.Literal)
		if err != nil {
			return CHType{}, fmt.Errorf("Tuple element index %q is not an integer", index.Literal)
		}
		return tupleElementByIndex(objectType, position, false)
	case *clickhouse.Ident:
		return tupleElementByName(objectType, index.Name, true)
	default:
		return CHType{}, fmt.Errorf(
			"Tuple element index %s must be a constant integer or an element name",
			clickhouse.Format(expression.Index),
		)
	}
}

// tupleElementFunctionResult types tupleElement(t, index_or_name) and the
// three-argument form tupleElement(t, index_or_name, default). Measured on
// ClickHouse 25.8.29.51:
//
//	tupleElement(tup, 1)            Int32
//	tupleElement(named, 'a')        the type of element a
//	tupleElement(tup, 3)            a ClickHouse error, no default given
//	tupleElement(tup, 3, 'def')     String, the type of the default
//
// The function form never keeps the LowCardinality wrapper of the element.
func inferTupleElementType(displayName string, args []clickhouse.Expr, scope queryScope) (CHType, error) {
	if len(args) < 2 || len(args) > 3 {
		return CHType{}, fmt.Errorf("function %s expects two or three arguments, got %d", displayName, len(args))
	}
	tupleType, err := inferExprType(args[0], scope)
	if err != nil {
		return CHType{}, fmt.Errorf("function %s first argument: %w", displayName, err)
	}
	arrayOfTuple := false
	if tupleType.normalizedName() == "array" && len(tupleType.Params) == 1 && isCHTuple(tupleType.Params[0]) {
		// ClickHouse applies tupleElement to each Tuple in an Array.
		// Measured on ClickHouse 25.8.29.51 over both a Nested column
		// and an explicit Array(Tuple(a Int32, b String)) column:
		// tupleElement(value, 2) is Array(String) in both cases.
		arrayOfTuple = true
		tupleType = tupleType.Params[0]
	}
	if !isCHTuple(tupleType) {
		return CHType{}, fmt.Errorf(
			"function %s cannot read an element of %s: it needs a Tuple with known element types",
			displayName, tupleType.String(),
		)
	}
	var elementType CHType
	switch selector := args[1].(type) {
	case *clickhouse.NumberLiteral:
		position, convErr := strconv.Atoi(selector.Literal)
		if convErr != nil {
			return CHType{}, fmt.Errorf("function %s element index %q is not an integer", displayName, selector.Literal)
		}
		elementType, err = tupleElementByIndex(tupleType, position, false)
	case *clickhouse.StringLiteral:
		elementType, err = tupleElementByName(tupleType, strings.Trim(selector.Literal, "'"), false)
	default:
		// ClickHouse itself refuses a non-constant selector: "Second
		// argument to tupleElement must be a constant UInt or String".
		return CHType{}, fmt.Errorf(
			"function %s needs a constant element index or element name, got %s",
			displayName, clickhouse.Format(args[1]),
		)
	}
	if err == nil {
		if arrayOfTuple {
			return CHType{Name: "Array", Params: []CHType{elementType}}, nil
		}
		return elementType, nil
	}
	if arrayOfTuple {
		// The measured Array(Tuple(...)) rule covers a present element.
		// Keep an absent element as a refusal until its default-value
		// result is measured.
		return CHType{}, fmt.Errorf("function %s: %w", displayName, err)
	}
	// The three-argument form replaces an out-of-range element with the
	// default value, and the result is the type of that default
	// (measured on ClickHouse 25.8.29.51: tupleElement(tup, 3, 'def') is
	// String, and tupleElement(named, 'zz', 5) is UInt8).
	if len(args) == 3 {
		defaultType, defaultErr := inferExprType(args[2], scope)
		if defaultErr != nil {
			return CHType{}, fmt.Errorf("function %s default argument: %w", displayName, defaultErr)
		}
		return defaultType, nil
	}
	return CHType{}, fmt.Errorf("function %s: %w", displayName, err)
}

func inferSubscriptType(expression *clickhouse.ObjectParams, scope queryScope) (CHType, error) {
	objectType, err := inferExprType(expression.Object, scope)
	if err != nil {
		return CHType{}, err
	}
	if expression.Params == nil || expression.Params.Items == nil || len(expression.Params.Items.Items) != 1 {
		return CHType{}, fmt.Errorf("subscript access expects exactly one index")
	}
	indexType, err := inferExprType(expression.Params.Items.Items[0], scope)
	if err != nil {
		return CHType{}, err
	}
	_, indexNullable, _ := splitCHWrappers(indexType)
	var result CHType
	switch {
	case strings.EqualFold(objectType.Name, "Array") && len(objectType.Params) == 1:
		result = objectType.Params[0]
	case strings.EqualFold(objectType.Name, "Map") && len(objectType.Params) == 2:
		result = objectType.Params[1]
	default:
		return CHType{}, fmt.Errorf("cannot subscript a value of type %s", objectType.String())
	}
	if indexNullable {
		result = applyCHWrappers(result, true, false)
	}
	return result, nil
}

// isAggregateFunctionName reports whether the lowercase function name is
// an aggregate. An aggregate result depends on the table rows, so it is
// never a constant.
func isAggregateFunctionName(name string) bool {
	if functionClassFor(name) == wrapperAggregate {
		return true
	}
	switch name {
	case "count", "countif", "uniq", "uniqexact", "uniqexactif",
		"uniqcombined", "countdistinct",
		"grouparray", "grouparrayif", "groupuniqarray", "groupuniqarrayif",
		"quantilestate", "quantilestateif":
		return true
	}
	return strings.HasSuffix(name, "merge")
}

// inferIntervalArithmeticType types "<operand> +/- INTERVAL n <unit>". The
// unit comes from the AST identifier, the operand type from the normal
// inference. A combination without a measured rule stays a refusal, because a
// guessed temporal type makes the generated Go scan into the wrong width and
// silently truncates.
func inferIntervalArithmeticType(operand clickhouse.Expr, interval *clickhouse.IntervalExpr, scope queryScope) (CHType, error) {
	if interval.Unit == nil {
		return CHType{}, fmt.Errorf("INTERVAL has no unit; %s", pinTypeHint)
	}
	operandType, err := inferExprType(operand, scope)
	if err != nil {
		return CHType{}, err
	}
	unit := interval.Unit.Name
	result, ok := intervalArithmeticResultType(operandType, unit)
	if !ok {
		return CHType{}, fmt.Errorf("cannot infer result type for %s with INTERVAL %s; %s",
			operandType.String(), strings.ToUpper(unit), pinTypeHint)
	}
	return result, nil
}

// inferToStartOfIntervalType types toStartOfInterval(<temporal>, INTERVAL n
// <unit>).
//
// Measured on ClickHouse 25.8.29.51 against real columns. The result depends
// ONLY on the unit and on the timezone of the first argument; the width of
// the first argument does NOT reach the result:
//
//	unit         result
//	NANOSECOND   DateTime64(9)[tz]
//	MICROSECOND  DateTime64(6)[tz]
//	MILLISECOND  DateTime64(3)[tz]
//	SECOND       DateTime[tz]
//	MINUTE       DateTime[tz]
//	HOUR         DateTime[tz]
//	DAY          DateTime[tz]
//	WEEK         Date
//	MONTH        Date
//	QUARTER      Date
//	YEAR         Date
//
// Every first-argument type in {Date, Date32, DateTime, DateTime64(P)} gives
// the same row. In particular toStartOfInterval(dt64, INTERVAL 1 HOUR) is
// DateTime, not DateTime64(3), and toStartOfInterval(d, INTERVAL 1 DAY) is
// DateTime, not Date. Before this rule chgen returned the first argument
// unchanged, which was wrong in both of those cells.
//
// A unit of one week or more drops the timezone entirely (measured:
// toStartOfInterval(dtz, INTERVAL 1 WEEK) is a bare Date), because the result
// is date-only.
func inferToStartOfIntervalType(displayName string, args []clickhouse.Expr, scope queryScope) (CHType, error) {
	if len(args) < 2 {
		return CHType{}, fmt.Errorf("function %s expects a value and an INTERVAL argument; %s", displayName, pinTypeHint)
	}
	interval, ok := args[1].(*clickhouse.IntervalExpr)
	if !ok || interval.Unit == nil {
		return CHType{}, fmt.Errorf("function %s second argument is not an INTERVAL with a unit; %s", displayName, pinTypeHint)
	}
	valueType, err := inferExprType(args[0], scope)
	if err != nil {
		return CHType{}, fmt.Errorf("function %s first argument: %w", displayName, err)
	}
	base, nullable, lowCardinality := splitCHWrappers(valueType)
	switch base.normalizedName() {
	case "date", "date32", "datetime", "datetime64":
	default:
		return CHType{}, fmt.Errorf("function %s has no measured rule for a %s argument; %s", displayName, valueType.String(), pinTypeHint)
	}
	unitPrecision, _, known := intervalUnitPrecision(interval.Unit.Name)
	if !known {
		return CHType{}, fmt.Errorf("function %s has no measured rule for INTERVAL %s; %s", displayName, strings.ToUpper(interval.Unit.Name), pinTypeHint)
	}
	// The unit and the argument type together decide whether the call is
	// possible at all. A date-only argument carries no time of day, thus
	// ClickHouse refuses every unit below one day:
	//
	//	                nanosecond microsecond millisecond second minute hour day week+
	//	Date, Date32    Code: 43   Code: 43    Code: 43    Code:43 Code:43 Code:43 ok  ok
	//	DateTime        Code: 43   Code: 43    Code: 43    ok      ok      ok      ok  ok
	//	DateTime64(3)   ok         ok          ok          ok      ok      ok      ok  ok
	//
	// Measured on ClickHouse 25.8.29.51 with real columns. The refusal
	// message is "Illegal interval kind for argument data type Date"
	// (ILLEGAL_TYPE_OF_ARGUMENT). It arrives at execution time, so the
	// generated Go compiles and the query then fails against the server:
	// exactly the silently wrong answer that a domain rule must remove.
	if domainErr := checkToStartOfIntervalDomain(displayName, base, interval.Unit.Name, valueType); domainErr != nil {
		return CHType{}, domainErr
	}
	// The boundary of toStartOfInterval is NOT the boundary of INTERVAL
	// arithmetic. For "dt + INTERVAL 1 DAY" the DAY unit keeps the operand
	// type, but toStartOfInterval(x, INTERVAL 1 DAY) is DateTime for every
	// argument type. The date-only row starts one unit later, at WEEK.
	// Both boundaries were measured; do not merge them.
	dateOnly := false
	switch strings.ToLower(strings.TrimSuffix(strings.ToLower(strings.TrimSpace(interval.Unit.Name)), "s")) {
	case "week", "month", "quarter", "year":
		dateOnly = true
	}
	timezone := dateTimeTimezoneParams(base)
	var result CHType
	switch {
	case dateOnly:
		// A unit of one week or more gives a bare Date, with no
		// timezone even when the argument carries one.
		result = CHType{Name: "Date"}
	case unitPrecision > 0:
		result = CHType{Name: "DateTime64", LiteralParams: append([]string{strconv.Itoa(unitPrecision)}, timezone...)}
	default:
		result = CHType{Name: "DateTime", LiteralParams: timezone}
	}
	return applyCHWrappers(result, nullable, lowCardinality), nil
}

// inferToTimeZoneType types toTimeZone(<DateTime|DateTime64>, '<zone>').
//
// Measured on ClickHouse 25.8.29.51 against real columns. The function keeps
// the width and the precision of its argument and replaces the timezone name:
//
//	toTimeZone(dt,    'Asia/Tokyo') -> DateTime('Asia/Tokyo')
//	toTimeZone(dtz,   'Asia/Tokyo') -> DateTime('Asia/Tokyo')
//	toTimeZone(dt64,  'Asia/Tokyo') -> DateTime64(3, 'Asia/Tokyo')
//	toTimeZone(dtz64, 'Asia/Tokyo') -> DateTime64(6, 'Asia/Tokyo')
//	toTimeZone(ndt,   'Asia/Tokyo') -> Nullable(DateTime('Asia/Tokyo'))
//	toTimeZone(lcdt,  'Asia/Tokyo') -> LowCardinality(DateTime('Asia/Tokyo'))
//
// Two cases stay refusals, because the server itself has no result there:
//   - a Date or Date32 argument is rejected with ILLEGAL_TYPE_OF_ARGUMENT
//     ("Should be DateTime or DateTime64");
//   - a non-constant timezone argument is rejected with ILLEGAL_COLUMN
//     ("must be a constant string"). The result type carries the zone NAME,
//     so a non-constant zone has no static type at all.
func inferToTimeZoneType(displayName string, args []clickhouse.Expr, scope queryScope) (CHType, error) {
	if len(args) < 2 {
		return CHType{}, fmt.Errorf("function %s expects a value and a timezone argument; %s", displayName, pinTypeHint)
	}
	zoneLiteral, ok := args[1].(*clickhouse.StringLiteral)
	if !ok {
		return CHType{}, fmt.Errorf("function %s needs a constant timezone; %s", displayName, pinTypeHint)
	}
	valueType, err := inferExprType(args[0], scope)
	if err != nil {
		return CHType{}, fmt.Errorf("function %s first argument: %w", displayName, err)
	}
	base, nullable, lowCardinality := splitCHWrappers(valueType)
	zone := "'" + strings.Trim(zoneLiteral.Literal, "'") + "'"
	var result CHType
	switch base.normalizedName() {
	case "datetime":
		result = CHType{Name: "DateTime", LiteralParams: []string{zone}}
	case "datetime64":
		result = CHType{Name: "DateTime64", LiteralParams: []string{strconv.Itoa(dateTime64Precision(base)), zone}}
	default:
		return CHType{}, fmt.Errorf("function %s rejects a %s argument, it needs DateTime or DateTime64; %s", displayName, valueType.String(), pinTypeHint)
	}
	return applyCHWrappers(result, nullable, lowCardinality), nil
}

// inferTimezoneCarryingType types the DateTime constructors whose result
// carries a timezone NAME. The name comes either from an explicit constant
// argument or from the argument type, so the argument expressions are needed
// and the plain rule table cannot express it.
//
// Measured on ClickHouse 25.8.29.51 against real columns:
//
//	now()                          -> DateTime
//	now('UTC')                     -> DateTime('UTC')
//	now64()                        -> DateTime64(3)
//	now64(6)                       -> DateTime64(6)
//	now64(3, 'UTC')                -> DateTime64(3, 'UTC')
//	toDateTime(dt)                 -> DateTime
//	toDateTime(dtz)                -> DateTime('UTC')      (carried from the argument)
//	toDateTime(dt, 'Europe/Berlin')-> DateTime('Europe/Berlin')
//	toDateTime64(dt, 6)            -> DateTime64(6)
//	toDateTime64(dtz64, 3)         -> DateTime64(3, 'UTC')  (carried from the argument)
//	toDateTime64(dt, 3, 'Europe/Berlin') -> DateTime64(3, 'Europe/Berlin')
//	toStartOfDay(dt)               -> DateTime
//	toStartOfDay(dtz)              -> DateTime('UTC')
//	toStartOfDay(dtz64)            -> DateTime('UTC')       (width drops to DateTime)
//	toStartOfDay(ndtz)             -> Nullable(DateTime('UTC'))
//	toStartOfDay(lcdtz)            -> LowCardinality(DateTime('UTC'))
//	toStartOfHour(dtz64)           -> DateTime('UTC')       (the regression)
//	toStartOfMinute(dtz64)         -> DateTime('UTC')       (the regression)
//
// toStartOfHour and toStartOfMinute share toStartOfDay's zone-carrying,
// width-dropping shape, but NOT its argument domain: they refuse Date and
// Date32, which toStartOfDay accepts. See hourMinuteStartOfArgumentDomain
// in argument_domain.go for the measured accept-set and the note that the
// server's own refusal message ("Should be Date, Date32, DateTime or
// DateTime64") is wrong for this narrower pair.
//
// Before this rule these functions returned a bare DateTime or DateTime64 and
// dropped the zone, which is a silently wrong type whenever the argument or
// an explicit argument carries one.
//
// An explicit timezone that is not a constant string stays a refusal: the
// result type carries the zone name, so a non-constant zone has no static
// type.
func inferTimezoneCarryingType(name string, function *clickhouse.FunctionExpr, args []clickhouse.Expr, scope queryScope) (CHType, error) {
	displayName := function.Name.Name
	// stringLiteralAt returns the constant timezone at the given argument
	// index, and whether that argument is present at all.
	stringLiteralAt := func(index int) (zone string, present, constant bool) {
		if index >= len(args) {
			return "", false, false
		}
		literal, ok := args[index].(*clickhouse.StringLiteral)
		if !ok {
			return "", true, false
		}
		return "'" + strings.Trim(literal.Literal, "'") + "'", true, true
	}

	switch name {
	case "now":
		zone, present, constant := stringLiteralAt(0)
		if present && !constant {
			return CHType{}, fmt.Errorf("function %s needs a constant timezone; %s", displayName, pinTypeHint)
		}
		if !present {
			return CHType{Name: "DateTime"}, nil
		}
		return CHType{Name: "DateTime", LiteralParams: []string{zone}}, nil
	case "now64":
		// now64 keeps the precision in its first argument and the
		// timezone in its second. The default precision is 3.
		precision := "3"
		if len(args) >= 1 {
			literal, ok := args[0].(*clickhouse.NumberLiteral)
			if !ok {
				return CHType{}, fmt.Errorf("function %s needs a constant precision; %s", displayName, pinTypeHint)
			}
			precision = strings.TrimSpace(literal.Literal)
		}
		zone, present, constant := stringLiteralAt(1)
		if present && !constant {
			return CHType{}, fmt.Errorf("function %s needs a constant timezone; %s", displayName, pinTypeHint)
		}
		params := []string{precision}
		if present {
			params = append(params, zone)
		}
		return CHType{Name: "DateTime64", LiteralParams: params}, nil
	}

	// The remaining names (todatetime, todatetime64, tostartofday,
	// tostartofhour, tostartofminute) read their value from the first
	// argument.
	if len(args) == 0 {
		return CHType{}, fmt.Errorf("function %s has no arguments; %s", displayName, pinTypeHint)
	}
	valueType, err := inferExprType(args[0], scope)
	if err != nil {
		// A first argument that cannot be typed (for example a bare
		// placeholder) keeps the historic bare result, exactly as the
		// independent rules did before.
		if errors.Is(err, errPlaceholderResultType) {
			switch name {
			case "todatetime64":
				return CHType{Name: "DateTime64", LiteralParams: []string{"3"}}, nil
			default:
				return CHType{Name: "DateTime"}, nil
			}
		}
		return CHType{}, fmt.Errorf("function %s first argument: %w", displayName, err)
	}
	base, _, _ := splitCHWrappers(valueType)
	// This route returns BEFORE the generic rule lookup, thus it must
	// apply the argument domain itself. Without this, toDateTime and
	// toDateTime64 gave a type to a container argument although the
	// server answers Code: 43, while every other conversion of the same
	// family refused it. See containerBaseType for the sweep.
	//
	// toStartOfDay does NOT share the wide scalarArgumentDomain of
	// toDateTime and toDateTime64: it refuses every non-date base type,
	// not the containers only. Measured on ClickHouse 25.8.29.51 over
	// the full wrapper alphabet (bare, LowCardinality, Nullable,
	// SimpleAggregateFunction, and the marker over LowCardinality):
	//
	//	toStartOfDay(i32)      Code: 43
	//	toStartOfDay(lci32)    Code: 43
	//	toStartOfDay(ni32)     Code: 43
	//	toStartOfDay(safi32)   Code: 43
	//	toStartOfDay(saflci)   Code: 43
	//	toDateTime(i32)        DateTime      (accepted as a Unix time)
	//	toDateTime(lci32)      LowCardinality(DateTime)
	//	toDateTime(ni32)       Nullable(DateTime)
	//	toDateTime(safi32)     DateTime
	//	toDateTime(saflci)     LowCardinality(DateTime)
	//
	// while both names agree on the date-shaped columns (d, d32, dt,
	// dt64 and their wrapped forms). Applying the wide domain to
	// toStartOfDay gave it a type for an Int32-based argument that the
	// server refuses, which is the silently-wrong-type defect that this
	// route exists to avoid.
	//
	// The marker comes off first, for the same reason as on the generic
	// route: the server judges the value inside it. Measured on
	// ClickHouse 25.8.29.51 with a VALUE select, never toTypeName
	// alone: "SELECT toDateTime(safarr_i32)" is Code: 43 and the
	// message names Array(Int32).
	domainArg := base
	if inner, ok := simpleAggregateWrapperInner(domainArg); ok {
		domainArg, _, _ = splitCHWrappers(inner)
	}
	// toDateTime additionally refuses a Decimal argument, the same gap
	// that toDate and toDate32 have on the generic registry route
	// (measured: toDateTime(dec) is Code: 44 for every Decimal width).
	// toDateTime64 keeps castArgumentDomain: toDateTime64(dec, 3) runs.
	// See dateFromValueArgumentDomain in argument_domain.go for the
	// full measured grid.
	//
	// Both toDateTime and toDateTime64 read their argument as a number
	// or a date, thus both refuse an AggregateFunction state
	// (the regression; measured: toDateTime(agg) and toDateTime64(agg, 3)
	// are both Code: 43). castArgumentDomain is the default for
	// exactly that reason: it is scalarArgumentDomain PLUS the
	// AggregateFunction refusal, and this route must not silently keep
	// the WIDER scalarArgumentDomain now that the generic registry
	// route no longer does either.
	argDomain := castArgumentDomain
	switch name {
	case "tostartofday":
		argDomain = dateArgumentDomain
	case "tostartofhour", "tostartofminute":
		argDomain = hourMinuteStartOfArgumentDomain
	case "todatetime":
		argDomain = dateFromValueArgumentDomain
	}
	if domainErr := checkArgumentDomain(displayName, argDomain, domainArg); domainErr != nil {
		return CHType{}, domainErr
	}
	// A SimpleAggregateFunction(f, LowCardinality(T)) argument makes the
	// result LowCardinality, exactly as a bare LowCardinality(T)
	// argument does. This is the SAME reading-through that the Nullable
	// note below records, on the other wrapper.
	//
	// splitCHWrappers alone does not see this LowCardinality: it reports
	// the wrappers ON the type, and this one lives in the inner type of
	// the marker. branchArgumentLowCardinality asks what the VALUE can
	// be instead, thus it looks inside the marker. The rule is stated
	// once, in that helper, and this route calls it.
	//
	// Measured on ClickHouse 25.8.29.51 with real columns of a real
	// AggregatingMergeTree table with one row, never over literals,
	// because the server folds constants (saflc is
	// SimpleAggregateFunction(anyLast, LowCardinality(Int32)), saflcd is
	// SimpleAggregateFunction(anyLast, LowCardinality(Date)), lci32 is a
	// plain LowCardinality(Int32) column, saf is
	// SimpleAggregateFunction(anyLast, Int32)):
	//
	//	toDateTime(saflc)      LowCardinality(DateTime)
	//	toDateTime(lci32)      LowCardinality(DateTime)
	//	toStartOfDay(saflcd)   LowCardinality(DateTime)
	//	toDateTime(saf)        DateTime
	//
	// The marker column and the plain LowCardinality column agree in
	// every cell, thus the marker must not change the answer.
	//
	// toDateTime64 is the one name of this group that DROPS the wrapper,
	// and it drops it for the plain column too (measured:
	// toDateTime64(saflc, 3) and toDateTime64(lci32, 3) are both a bare
	// DateTime64(3)). That needs no test on the name here: the flag says
	// only that the argument OFFERS the wrapper, and applyCHWrappers
	// asks wrapLowCardinality, which holds the measured family rule that
	// a DateTime64 result cannot carry a LowCardinality wrapper. The
	// rule is therefore stated once, on the FACTS of the result type,
	// and this route stays free of a name check.
	lowCardinality := branchArgumentLowCardinality(valueType)
	// A SimpleAggregateFunction(f, Nullable(T)) argument makes the
	// result Nullable, exactly as a bare Nullable(T) argument does. The
	// server READS THROUGH the marker and answers about the value inside
	// it, thus splitCHWrappers alone does not see the null: it reports
	// the wrappers ON the type and the null lives in the inner type. The
	// BASE keeps the marker, because the timezone read below is on the
	// type.
	//
	// Measured on ClickHouse 25.8.29.51 with real columns in a real
	// table, never over literals, because the server folds constants
	// (safn is SimpleAggregateFunction(anyLast, Nullable(Int32)), saf is
	// SimpleAggregateFunction(anyLast, Int32)):
	//
	//	toDateTime(safn)       Nullable(DateTime)
	//	toDateTime(saf)        DateTime
	//	toDateTime64(safn, 2)  Nullable(DateTime64(2))
	nullable := branchArgumentNullable(valueType)
	// The timezone of the argument survives only when the argument is
	// itself a DateTime or DateTime64. A Date, a String or a number
	// carries none.
	argumentZone := dateTimeTimezoneParams(base)

	var result CHType
	switch name {
	case "tostartofday", "tostartofhour", "tostartofminute":
		// The result is always a whole-second DateTime; a DateTime64
		// argument loses its sub-second precision but keeps its zone.
		// toStartOfHour and toStartOfMinute share this shape (measured
		// on ClickHouse 25.8.29.51: toStartOfHour(dtz64) is
		// DateTime('UTC'), same as toStartOfDay(dtz64)); only the
		// argument domain checked above differs between the three.
		zone, present, constant := stringLiteralAt(1)
		if present && !constant {
			return CHType{}, fmt.Errorf("function %s needs a constant timezone; %s", displayName, pinTypeHint)
		}
		params := argumentZone
		if present {
			params = []string{zone}
		}
		result = CHType{Name: "DateTime", LiteralParams: params}
	case "todatetime":
		zone, present, constant := stringLiteralAt(1)
		if present && !constant {
			return CHType{}, fmt.Errorf("function %s needs a constant timezone; %s", displayName, pinTypeHint)
		}
		params := argumentZone
		if present {
			params = []string{zone}
		}
		result = CHType{Name: "DateTime", LiteralParams: params}
	case "todatetime64":
		// The precision is the second argument and is mandatory in
		// every measured form. It replaces the precision of the
		// argument (toDateTime64(dt64, 1) is DateTime64(1)).
		precision := "3"
		if len(args) >= 2 {
			literal, ok := args[1].(*clickhouse.NumberLiteral)
			if !ok {
				return CHType{}, fmt.Errorf("function %s needs a constant precision; %s", displayName, pinTypeHint)
			}
			precision = strings.TrimSpace(literal.Literal)
		}
		zone, present, constant := stringLiteralAt(2)
		if present && !constant {
			return CHType{}, fmt.Errorf("function %s needs a constant timezone; %s", displayName, pinTypeHint)
		}
		params := append([]string{precision}, argumentZone...)
		if present {
			params = []string{precision, zone}
		}
		result = CHType{Name: "DateTime64", LiteralParams: params}
	}
	return applyCHWrappers(result, nullable, lowCardinality), nil
}

// temporalShiftFunctions names every addXxx and subtractXxx date-arithmetic
// function, keyed by lowercase name. inferFunctionType routes a name in
// this map to inferTemporalShiftType before the generic rule lookup,
// because the result shape depends on which UNIT the name shifts by, and a
// plain functionTypeRule sees only the argument types and not the name
// itself.
//
// The map holds two boolean-keyed groups, split by whether the unit is
// finer than a day. Measured on ClickHouse 25.8.29.51 with real columns:
//
//	addDays(d, 1)      Date         addSeconds(d, 1)    DateTime
//	addDays(d32, 1)    Date32       addSeconds(d32, 1)  DateTime64(3)
//	addDays(dt, 1)     DateTime     addSeconds(dt, 1)   DateTime
//	addDays(dt64, 1)   DateTime64(N) (zone and precision both preserved)
//
// A Date or Date32 argument STAYS a Date or Date32 under a day-or-larger
// unit (Day, Week, Month, Quarter, Year), because the result still holds
// no time of day. The SAME argument PROMOTES to DateTime (or DateTime64
// for Date32, which the server answers as DateTime64(3) with no zone,
// because Date32 itself carries none) under an Hour, Minute or Second
// unit, because the shift can now produce a time of day that a Date
// cannot hold.
//
// subFineUnit is true for the sub-day units (Hour, Minute, Second) and
// false for the day-or-larger units (Day, Week, Month, Quarter, Year).
// This is the ONLY axis that changes the result shape: a DateTime or
// DateTime64 argument keeps its own shape under every unit in both
// groups.
var temporalShiftFunctions = map[string]bool{
	"adddays": false, "subtractdays": false,
	"addweeks": false, "subtractweeks": false,
	"addmonths": false, "subtractmonths": false,
	"addquarters": false, "subtractquarters": false,
	"addyears": false, "subtractyears": false,
	"addhours": true, "subtracthours": true,
	"addminutes": true, "subtractminutes": true,
	"addseconds": true, "subtractseconds": true,
}

// inferTemporalShiftType types one addXxx or subtractXxx call. The
// argument expressions are not needed beyond the first (the shift COUNT
// is a plain number and never changes the result shape), so this reads
// only args[0].
//
// Measured domain: Date, Date32, DateTime and DateTime64 only
// (addSubtractTemporalArgumentDomain in argument_domain.go). A String
// argument gives a type at analysis time but fails at EXECUTION on a
// value the parser cannot read as a date (measured: Code: 41
// CANNOT_PARSE_DATETIME on addDays(s, 1) over a real 'ab' column). That
// is a value property, not a type property, so this domain refuses
// String rather than crediting an execution-fragile answer, the same
// choice the regression records for toFixedString's asymmetry.
func inferTemporalShiftType(name, displayName string, args []clickhouse.Expr, scope queryScope) (CHType, error) {
	subFineUnit, known := temporalShiftFunctions[name]
	if !known {
		return CHType{}, fmt.Errorf("function %s is not a recognised date-arithmetic function; %s", displayName, pinTypeHint)
	}
	if len(args) == 0 {
		return CHType{}, fmt.Errorf("function %s has no arguments; %s", displayName, pinTypeHint)
	}
	valueType, err := inferExprType(args[0], scope)
	if err != nil {
		if errors.Is(err, errPlaceholderResultType) {
			return CHType{Name: "DateTime"}, nil
		}
		return CHType{}, fmt.Errorf("function %s first argument: %w", displayName, err)
	}
	base, _, _ := splitCHWrappers(valueType)
	domainArg := base
	if inner, ok := simpleAggregateWrapperInner(domainArg); ok {
		domainArg, _, _ = splitCHWrappers(inner)
	}
	if domainErr := checkArgumentDomain(displayName, addSubtractTemporalArgumentDomain, domainArg); domainErr != nil {
		return CHType{}, domainErr
	}
	lowCardinality := branchArgumentLowCardinality(valueType)
	nullable := branchArgumentNullable(valueType)
	argumentZone := dateTimeTimezoneParams(base)

	var result CHType
	switch base.normalizedName() {
	case "date":
		if subFineUnit {
			// A day-only value promotes to a whole-second DateTime
			// with no zone, because Date itself carries none
			// (measured: addSeconds(d, 1) is a bare DateTime).
			result = CHType{Name: "DateTime"}
		} else {
			result = CHType{Name: "Date"}
		}
	case "date32":
		if subFineUnit {
			// Date32 promotes to DateTime64(3) with no zone
			// (measured: addSeconds(d32, 1) is DateTime64(3)).
			result = CHType{Name: "DateTime64", LiteralParams: []string{"3"}}
		} else {
			result = CHType{Name: "Date32"}
		}
	case "datetime":
		result = CHType{Name: "DateTime", LiteralParams: argumentZone}
	case "datetime64":
		result = CHType{Name: "DateTime64", LiteralParams: append([]string{strconv.Itoa(dateTime64Precision(base))}, argumentZone...)}
	default:
		// checkArgumentDomain above already refused every base type
		// outside this set, so this branch is unreachable in practice.
		// It stays as a refusal, never a guess, in case the domain and
		// this switch ever drift apart.
		return CHType{}, fmt.Errorf("function %s: unhandled base type %s; %s", displayName, base.String(), pinTypeHint)
	}
	return applyCHWrappers(result, nullable, lowCardinality), nil
}
