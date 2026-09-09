package engine

import (
	"fmt"
	"strconv"
	"strings"

	clickhouse "github.com/AfterShip/clickhouse-sql-parser/parser"
)

// arithmeticIntegerClass classifies an integer operand for the widening
// rules. Bool behaves as UInt8 in ClickHouse arithmetic.
type arithmeticIntegerClass struct {
	signed bool
	size   int // bytes: 1, 2, 4 or 8
}

func arithmeticIntegerClassOf(operand CHType) (arithmeticIntegerClass, bool) {
	if len(operand.Params) != 0 || len(operand.LiteralParams) != 0 {
		return arithmeticIntegerClass{}, false
	}
	switch operand.normalizedName() {
	case "uint8", "bool":
		return arithmeticIntegerClass{signed: false, size: 1}, true
	case "uint16":
		return arithmeticIntegerClass{signed: false, size: 2}, true
	case "uint32":
		return arithmeticIntegerClass{signed: false, size: 4}, true
	case "uint64":
		return arithmeticIntegerClass{signed: false, size: 8}, true
	case "int8":
		return arithmeticIntegerClass{signed: true, size: 1}, true
	case "int16":
		return arithmeticIntegerClass{signed: true, size: 2}, true
	case "int32":
		return arithmeticIntegerClass{signed: true, size: 4}, true
	case "int64", "int":
		return arithmeticIntegerClass{signed: true, size: 8}, true
	case "uint128":
		return arithmeticIntegerClass{signed: false, size: 16}, true
	case "uint256":
		return arithmeticIntegerClass{signed: false, size: 32}, true
	case "int128":
		return arithmeticIntegerClass{signed: true, size: 16}, true
	case "int256":
		return arithmeticIntegerClass{signed: true, size: 32}, true
	default:
		return arithmeticIntegerClass{}, false
	}
}

// arithmeticIntegerTypeNames maps a result class back to a ClickHouse type
// name.
var arithmeticIntegerTypeNames = map[arithmeticIntegerClass]string{
	{signed: false, size: 1}:  "UInt8",
	{signed: false, size: 2}:  "UInt16",
	{signed: false, size: 4}:  "UInt32",
	{signed: false, size: 8}:  "UInt64",
	{signed: true, size: 1}:   "Int8",
	{signed: true, size: 2}:   "Int16",
	{signed: true, size: 4}:   "Int32",
	{signed: true, size: 8}:   "Int64",
	{signed: false, size: 16}: "UInt128",
	{signed: false, size: 32}: "UInt256",
	{signed: true, size: 16}:  "Int128",
	{signed: true, size: 32}:  "Int256",
}

// promotedArithmeticSize doubles the operand size below 64 bits and keeps
// it at 64 bits, which follows the documented ClickHouse promotion rule.
func promotedArithmeticSize(size int) int {
	if size >= 8 {
		return size
	}
	return size * 2
}

func arithmeticFloatType(operand CHType) bool {
	name := operand.normalizedName()
	return name == "float32" || name == "float64"
}

func arithmeticDecimalType(operand CHType) bool {
	switch operand.normalizedName() {
	case "decimal", "decimal32", "decimal64", "decimal128", "decimal256":
		return true
	default:
		return false
	}
}

// decimalPrecisionScale reads the precision and the scale of a
// Decimal(P, S) type. A Decimal spelled without both parameters is not
// usable for the measured rules and reports false.
//
// The sized spellings Decimal32(S), Decimal64(S), Decimal128(S) and
// Decimal256(S) name the same types as Decimal(P, S): each size fixes the
// precision. canonicalDecimalType rewrites them into the canonical form
// first, thus a sized spelling gets the same rule as the bare one. Without
// this step every sized Decimal fell through to a refusal, although the
// server answers (measured on ClickHouse 25.8.29.51 with real columns:
// Decimal128(4) + Int16 is Decimal(38, 4)).
func decimalPrecisionScale(value CHType) (precision, scale int, ok bool) {
	value = canonicalDecimalType(value)
	if !strings.EqualFold(value.Name, "Decimal") || len(value.LiteralParams) != 2 {
		return 0, 0, false
	}
	precision, precisionErr := strconv.Atoi(strings.TrimSpace(value.LiteralParams[0]))
	scale, scaleErr := strconv.Atoi(strings.TrimSpace(value.LiteralParams[1]))
	if precisionErr != nil || scaleErr != nil {
		return 0, 0, false
	}
	return precision, scale, true
}

// decimalClassPrecision maps a precision to the full precision of its
// storage class: Decimal32 holds 9 digits, Decimal64 18, Decimal128 38
// and Decimal256 76.
func decimalClassPrecision(precision int) int {
	switch {
	case precision <= 9:
		return 9
	case precision <= 18:
		return 18
	case precision <= 38:
		return 38
	default:
		return 76
	}
}

// decimalDigitsForInteger gives the count of decimal digits that the
// integer type needs. The common-supertype rule widens a Decimal to the
// storage class that holds this count.
func decimalDigitsForInteger(name string) (int, bool) {
	switch strings.ToLower(name) {
	case "bool":
		return 1, true
	case "uint8", "int8":
		return 3, true
	case "uint16", "int16":
		return 5, true
	case "uint32", "int32":
		return 10, true
	case "int64", "int":
		return 19, true
	case "uint64":
		return 20, true
	default:
		return 0, false
	}
}

func arithmeticDateType(operand CHType) bool {
	switch operand.normalizedName() {
	case "date", "date32", "datetime", "datetime64":
		return true
	default:
		return false
	}
}

// dateTime64DifferenceType answers the result of a subtraction where at least
// one operand is a DateTime64. It reports false when the pair has no rule, so
// that the caller can refuse.
//
// Measured on ClickHouse 25.8.29.51 against real columns of a fixture table,
// in the analysis form and over a real row. Every precision from 0 to 9 gives
// Decimal(18, precision):
//
//	t0 - t0    Decimal(18, 0)      t5 - t5    Decimal(18, 5)
//	t1 - t1    Decimal(18, 1)      t6 - t6    Decimal(18, 6)
//	t2 - t2    Decimal(18, 2)      t7 - t7    Decimal(18, 7)
//	t3 - t3    Decimal(18, 3)      t8 - t8    Decimal(18, 8)
//	t4 - t4    Decimal(18, 4)      t9 - t9    Decimal(18, 9)
//
// A mixed pair takes the LARGER of the two scales, in both operand orders:
//
//	t3 - t6    Decimal(18, 6)      t6 - t3    Decimal(18, 6)
//	t1 - t2    Decimal(18, 2)      t2 - t9    Decimal(18, 9)
//	t0 - t9    Decimal(18, 9)      t9 - t0    Decimal(18, 9)
//
// A DateTime operand behaves as a DateTime64 of precision 0, thus the
// DateTime64 scale wins, again in both orders:
//
//	t3 - dt    Decimal(18, 3)      dt - t3    Decimal(18, 3)
//	t0 - dt    Decimal(18, 0)
//
// A date-only operand has NO rule and stays a refusal. The server answers
// code 43 ILLEGAL_TYPE_OF_ARGUMENT for it:
//
//	t3 - d     Code 43             d - t3     Code 43
//	t3 - d32   Code 43
//
// The timezone does not reach the result: DateTime64(6, 'UTC') minus
// DateTime64(6, 'Europe/Berlin') is Decimal(18, 6), with no zone. The
// Nullable wrapper does propagate, and the caller adds it.
//
// chgen used to answer Int32 for every one of these pairs, because the Date
// rule was widened to all date-like types. That answer was silently wrong:
// the generated Go compiled and then held a truncated value.
func dateTime64DifferenceType(left, right CHType) (CHType, bool) {
	// scaleOf reports the sub-second scale of one operand. A DateTime has
	// no sub-second part, thus its scale is 0. Any other type, a Date or a
	// Date32, has no rule here.
	scaleOf := func(operand CHType) (int, bool) {
		switch operand.normalizedName() {
		case "datetime64":
			return dateTime64Precision(operand), true
		case "datetime":
			return 0, true
		default:
			return 0, false
		}
	}
	leftScale, leftOK := scaleOf(left)
	rightScale, rightOK := scaleOf(right)
	if !leftOK || !rightOK {
		return CHType{}, false
	}
	// One operand must be a DateTime64. A DateTime pair keeps its own
	// Int32 rule, which the caller holds.
	if !strings.EqualFold(left.normalizedName(), "datetime64") &&
		!strings.EqualFold(right.normalizedName(), "datetime64") {
		return CHType{}, false
	}
	scale := maxInt(leftScale, rightScale)
	return CHType{
		Name:          "Decimal",
		LiteralParams: []string{"18", strconv.Itoa(scale)},
	}, true
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// inferArithmeticResultType applies the ClickHouse common-type rules for
// the binary operators "+", "-", "*", "/" and "%".
//
// Sources for the table:
//   - https://clickhouse.com/docs/en/sql-reference/functions/arithmetic-functions
//     documents the common result type: the next bigger size below 64 bits,
//     the bigger operand size at 64 bits, signed when one operand is
//     signed, and Float64 for "/".
//   - src/DataTypes/NumberTraits.h in the ClickHouse source defines
//     ResultOfAdditionMultiplication, ResultOfSubtraction (always signed)
//     and ResultOfModulo (sign from the dividend only; a signed dividend
//     widens the divisor size, an unsigned dividend keeps it).
//   - Each rule was verified on ClickHouse 25.8.16 with
//     SELECT toTypeName(<expression>).
//
// A combination without an established rule returns an explicit error. A
// generation error is better than a silently wrong Go type.
func inferArithmeticResultType(operator string, left, right CHType) (CHType, error) {
	// A SimpleAggregateFunction(f, T) operand of a binary arithmetic
	// operator behaves exactly as T. The unwrap comes first, because the
	// inner type can itself be Nullable and the Nullable rule below must
	// see it.
	//
	// The unwrap is deliberately LOCAL to this function, which serves only
	// the binary operators "+", "-", "*", "/" and "%". It is not a global
	// rule: other positions KEEP the wrapper, thus a blanket unwrap would
	// answer the wrong type there. See simpleAggregateInnerType.
	left = simpleAggregateInnerType(left)
	right = simpleAggregateInnerType(right)

	nullable := false
	if strings.EqualFold(left.Name, "Nullable") && len(left.Params) == 1 {
		nullable = true
		left = left.Params[0]
	}
	if strings.EqualFold(right.Name, "Nullable") && len(right.Params) == 1 {
		nullable = true
		right = right.Params[0]
	}
	// wrap puts the Nullable wrapper back through the ONE constructor
	// (wrapNullable), instead of building the node by hand. Every
	// result that reaches this closure is a scalar (Date, Int, Float or
	// Decimal): the Array branch above returns before this closure ever
	// runs, and it never wraps its own Array result in Nullable either
	// (measured on ClickHouse 25.8.29.51: arr_i32 * ni32 answers code
	// 43, "Nested type Array(Int64) cannot be inside Nullable type",
	// because the server itself refuses that Nullable(Array(T))
	// shape). canBeInsideNullable therefore always holds for a value
	// this closure sees today, so routing through wrapNullable cannot
	// change any measured answer; it removes the hand-built node so
	// the rejection rule has one owner instead of two copies that can
	// drift apart.
	wrap := func(result CHType) CHType {
		if nullable {
			return wrapNullable(result)
		}
		return result
	}
	cannotInfer := func() (CHType, error) {
		return CHType{}, fmt.Errorf("cannot infer result type for %s %s %s", left.String(), operator, right.String())
	}

	// Array arithmetic, measured on ClickHouse 25.8.29.51 with real
	// columns for every operator and both operand orders. The two forms
	// are disjoint, and each operator supports exactly one of them:
	//   - An Array with a scalar under "*", "/" and "%" applies the
	//     ordinary scalar rule to the element type and keeps the Array
	//     wrapper (arr_i * f64 is Array(Float64), i64 % arr_i is
	//     Array(Int64)). The operand order does not matter. The rule
	//     recurses, thus Array(Array(Int64)) / Int64 is
	//     Array(Array(Float64)).
	//   - Two Arrays under "+" and "-" are elementwise vector
	//     arithmetic ([1,2] + [10,20] is [11,22], not a concatenation).
	//     The element types take the ordinary common type.
	//   - Every other combination answers code 43 and stays a refusal
	//     here: an Array with a scalar under "+" or "-", two Arrays
	//     under "*", "/" or "%", an Array against a Nullable scalar,
	//     and an Array of a Nullable element against anything.
	leftIsArray := strings.EqualFold(left.Name, "Array") && len(left.Params) == 1
	rightIsArray := strings.EqualFold(right.Name, "Array") && len(right.Params) == 1
	if leftIsArray || rightIsArray {
		// A Nullable scalar operand never combines with an Array.
		if nullable {
			return cannotInfer()
		}
		elementLeft, elementRight := left, right
		switch {
		case leftIsArray && rightIsArray:
			if operator != "+" && operator != "-" {
				return cannotInfer()
			}
			elementLeft, elementRight = left.Params[0], right.Params[0]
		case leftIsArray:
			if operator != "*" && operator != "/" && operator != "%" {
				return cannotInfer()
			}
			elementLeft = left.Params[0]
		default:
			if operator != "*" && operator != "/" && operator != "%" {
				return cannotInfer()
			}
			elementRight = right.Params[0]
		}
		// An Array of a Nullable element has no rule. The recursive
		// call would otherwise strip the Nullable and answer a type
		// that the server refuses.
		if strings.EqualFold(elementLeft.Name, "Nullable") || strings.EqualFold(elementRight.Name, "Nullable") {
			return cannotInfer()
		}
		element, err := inferArithmeticResultType(operator, elementLeft, elementRight)
		if err != nil {
			return cannotInfer()
		}
		return CHType{Name: "Array", Params: []CHType{element}}, nil
	}

	leftInteger, leftIsInteger := arithmeticIntegerClassOf(left)
	rightInteger, rightIsInteger := arithmeticIntegerClassOf(right)

	// Date and DateTime arithmetic, measured on ClickHouse 25.8.29.51 with
	// real columns for every operator and both operand orders:
	//   - A date moves by an offset under "+" and "-" and keeps its type.
	//     The offset may be a narrow integer or a float (Date - Float32 is
	//     Date). A wide 128 bit or 256 bit integer offset is rejected by
	//     the server with code 43, thus it must stay a refusal here.
	//   - Only "+" is commutative: Float64 + Date is Date, but
	//     Float64 - Date is code 43. A date on the right of "-" is only
	//     valid when the left side is a date too.
	//   - Date - Date and DateTime - DateTime subtract to Int32. Mixing
	//     Date with DateTime is code 43.
	//   - Date32 - Date32 is NOT a difference. The server answers code 43,
	//     "Illegal types Date32 and Date32 of arguments of function minus",
	//     for a column pair and for a literal pair alike. Date32 shares the
	//     offset rules with Date (d32 + 1 and d32 - 1 are both Date32) but
	//     it has no same-type difference, thus it stays a refusal.
	//   - "%" takes the date as the dividend only. The result follows the
	//     divisor: Date % Int16 is Int16, Date % Float64 is Float64. A
	//     wide integer divisor is code 43.
	//   - "*" and "/" have no date rule at all; the server answers code 43.
	if arithmeticDateType(left) || arithmeticDateType(right) {
		leftIsDate := arithmeticDateType(left)
		rightIsDate := arithmeticDateType(right)
		// An offset must be a narrow integer or a float. The wide
		// integers are rejected by the server.
		narrowOffset := func(operand CHType, isInteger bool) bool {
			if isInteger {
				return !wideIntegerCHType(operand)
			}
			return arithmeticFloatType(operand)
		}
		switch operator {
		case "+", "-":
			if leftIsDate && rightIsDate {
				// A difference with a DateTime64 operand keeps
				// the sub-second scale, thus it is NOT Int32.
				if operator == "-" {
					if result, ok := dateTime64DifferenceType(left, right); ok {
						return wrap(result), nil
					}
				}
				// Only the identical date type subtracts, and
				// Date32 is excluded: the server has no Date32
				// difference at all.
				if operator == "-" && left.normalizedName() == right.normalizedName() &&
					left.normalizedName() != "date32" {
					return wrap(CHType{Name: "Int32"}), nil
				}
				return cannotInfer()
			}
			if leftIsDate && narrowOffset(right, rightIsInteger) {
				return wrap(left), nil
			}
			// The reversed order works for "+" only.
			if operator == "+" && rightIsDate && narrowOffset(left, leftIsInteger) {
				return wrap(right), nil
			}
		case "%":
			// The date must be the dividend and the divisor keeps its
			// own type. Only Date and DateTime have a "%" rule.
			// Date32 and DateTime64 have NONE, thus they must stay a
			// refusal here even though they pass the "+"/"-" offset
			// rule above. Measured on ClickHouse 25.8.29.51 with real
			// columns: "d % u64" is UInt64, but "d32 % u64" and
			// "dt64 % u64" both answer code 43, ILLEGAL_TYPE_OF_ARGUMENT
			// ("Illegal types Date32 and UInt64 of arguments of
			// function modulo").
			modName := left.normalizedName()
			leftHasModuloRule := modName == "date" || modName == "datetime"
			if leftIsDate && !rightIsDate && leftHasModuloRule {
				if rightIsInteger && !wideIntegerCHType(right) {
					return wrap(right), nil
				}
				if arithmeticFloatType(right) {
					return wrap(CHType{Name: "Float64"}), nil
				}
			}
		}
		return cannotInfer()
	}

	// Decimal arithmetic, measured on ClickHouse 25.8.29.51 with real
	// columns for every operator and both operand orders:
	//   - Decimal with an integer keeps the Decimal type unchanged
	//     (dec2 + i64 is Decimal(10, 2), u32 % dec is Decimal(18, 4)).
	//   - Decimal with a float gives Float64 (dec % f32 is Float64).
	//   - Decimal with Decimal takes the full precision of the widest
	//     storage class (dec2 + dec2 is Decimal(18, 2), dec * dec is
	//     Decimal(18, 8)). The scale is max(s1, s2) for "+" and "-",
	//     s1 + s2 for "*", and the left scale for "/" and "%".
	// A Decimal whose precision and scale are unknown stays a refusal.
	leftIsFloat := arithmeticFloatType(left)
	rightIsFloat := arithmeticFloatType(right)
	if arithmeticDecimalType(left) || arithmeticDecimalType(right) {
		leftPrecision, leftScale, leftDecimal := decimalPrecisionScale(left)
		rightPrecision, rightScale, rightDecimal := decimalPrecisionScale(right)
		switch {
		case leftDecimal && rightDecimal:
			precision := decimalClassPrecision(maxInt(leftPrecision, rightPrecision))
			var scale int
			switch operator {
			case "+", "-":
				scale = maxInt(leftScale, rightScale)
			case "*":
				scale = leftScale + rightScale
			case "/", "%":
				scale = leftScale
			default:
				return cannotInfer()
			}
			return wrap(CHType{Name: "Decimal", LiteralParams: []string{strconv.Itoa(precision), strconv.Itoa(scale)}}), nil
		// The Decimal keeps its type, but the result is spelled in the
		// canonical Decimal(P, S) form that the server reports: a
		// Decimal32(4) column with an integer gives Decimal(9, 4), not
		// Decimal32(4). Measured on ClickHouse 25.8.29.51 with real
		// columns for both operand orders.
		case leftDecimal && rightIsInteger:
			return wrap(canonicalDecimalType(left)), nil
		case rightDecimal && leftIsInteger:
			return wrap(canonicalDecimalType(right)), nil
		case (leftDecimal && rightIsFloat) || (rightDecimal && leftIsFloat):
			return wrap(CHType{Name: "Float64"}), nil
		}
		return cannotInfer()
	}
	if leftIsFloat || rightIsFloat {
		if (leftIsFloat || leftIsInteger) && (rightIsFloat || rightIsInteger) {
			// A 128 bit or 256 bit integer with a float has no result for
			// the plus, minus and multiply operators: the server answers
			// code 43, ILLEGAL_TYPE_OF_ARGUMENT. Divide and modulo do work
			// on the same pair and give Float64. Measured on ClickHouse
			// 25.8.29.51 with real columns, thus constant folding cannot
			// explain the split.
			if operator == "+" || operator == "-" || operator == "*" {
				if wideIntegerCHType(left) || wideIntegerCHType(right) {
					return cannotInfer()
				}
			}
			return wrap(CHType{Name: "Float64"}), nil
		}
		return cannotInfer()
	}

	if !leftIsInteger || !rightIsInteger {
		return cannotInfer()
	}

	if operator == "/" {
		return wrap(CHType{Name: "Float64"}), nil
	}

	var result arithmeticIntegerClass
	switch operator {
	case "+", "*":
		result = arithmeticIntegerClass{
			signed: leftInteger.signed || rightInteger.signed,
			size:   promotedArithmeticSize(maxInt(leftInteger.size, rightInteger.size)),
		}
	case "-":
		result = arithmeticIntegerClass{
			signed: true,
			size:   promotedArithmeticSize(maxInt(leftInteger.size, rightInteger.size)),
		}
	case "%":
		if leftInteger.signed {
			result = arithmeticIntegerClass{signed: true, size: promotedArithmeticSize(rightInteger.size)}
		} else {
			result = arithmeticIntegerClass{signed: false, size: rightInteger.size}
		}
	default:
		return cannotInfer()
	}
	return wrap(CHType{Name: arithmeticIntegerTypeNames[result]}), nil
}

// simpleAggregateInnerType gives the inner type T of a
// SimpleAggregateFunction(f, T). Any other type passes through unchanged.
//
// The unwrap applies to ONE position only: an operand of a binary
// arithmetic operator. Do not make it global. Measured on ClickHouse
// 25.8.29.51 through the HTTP interface, against real columns of a table,
// never over literals, because the server folds constants
// (sagg is SimpleAggregateFunction(sum, Int64),
// nsagg is SimpleAggregateFunction(sum, Nullable(Int64)),
// saggf is SimpleAggregateFunction(sum, Float64),
// sagga is SimpleAggregateFunction(groupArrayArray, Array(Int64)),
// i64 is Int64, u8 is UInt8, arr is Array(Int64)).
//
// The positions that UNWRAP, each equal to the answer for the inner type
// alone:
//
//	sagg + u8      Int64      sagg - i64     Int64
//	sagg / i64     Float64    sagg % i64     Int64
//	sagg + sagg    Int64      saggf - sagg   Float64
//	u8 - sagg      Int64      i64 / sagg     Float64
//	nsagg + i64    Nullable(Int64)
//	nsagg / i64    Nullable(Float64)
//	sagga + arr    Array(Int64)
//	sagga / i64    Array(Float64)
//
// The values were also selected, not only analysed with toTypeName:
// sagg + u8 gives 7, sagg / i64 gives 1.6666666666666667 and
// (sagg - i64) > 0 gives 1.
//
// The positions that KEEP the wrapper. These are the reason the unwrap
// must stay local:
//
//	if(1, sagg, i64)        SimpleAggregateFunction(sum, Int64)
//	multiIf(1, sagg, i64)   SimpleAggregateFunction(sum, Int64)
//	max(sagg)               SimpleAggregateFunction(sum, Int64)
//	coalesce(sagg, i64)     SimpleAggregateFunction(sum, Int64)
//	sagg (bare column)      SimpleAggregateFunction(sum, Int64)
//
// The unwrap does not widen the refusal boundary. A pair that the inner
// rule refuses stays refused, and the server refuses it too:
// sagg + saggs answers Code: 43 ("Illegal types
// SimpleAggregateFunction(sum, Int64) and
// SimpleAggregateFunction(min, String) of arguments of function plus").
func simpleAggregateInnerType(value CHType) CHType {
	if strings.EqualFold(value.Name, "SimpleAggregateFunction") && len(value.Params) == 2 {
		return value.Params[1]
	}
	return value
}

// aggregateStateOperand reports whether value is an AggregateFunction
// state, or an Array of one, nested to any depth. A
// SimpleAggregateFunction is NOT this class: it unwraps to its inner type
// and behaves as an ordinary value (see simpleAggregateInnerType and
// simple_aggregate_arithmetic_test.go).
func aggregateStateOperand(value CHType) bool {
	for strings.EqualFold(value.Name, "Array") && len(value.Params) == 1 {
		value = value.Params[0]
	}
	return strings.EqualFold(value.Name, "AggregateFunction")
}

// aggregateStateReplicationResultType decides the "state * N" idiom: an
// AggregateFunction state, or an Array of one nested to any depth, times a
// count. It is NOT arithmetic on the state's contained values; the server
// builds N copies of the state, so N must be a constant known at plan
// time, and the state keeps its exact type (the count never changes it).
//
// Measured on ClickHouse 25.8.29.51 over real columns of an
// AggregatingMergeTree table (agg AggregateFunction(uniq, UInt64), u8
// UInt8, i32 Int32, with DESCRIBE (SELECT expr FROM probe)):
//
// ACCEPTED — the state on either side, the count a column-independent
// UNSIGNED integer constant no wider than UInt64:
//
//	agg * 2                 AggregateFunction(uniq, UInt64)
//	agg * 0                 AggregateFunction(uniq, UInt64)
//	agg * toUInt8(2)         AggregateFunction(uniq, UInt64)
//	2 * groupArray(agg)      Array(AggregateFunction(uniq, UInt64))
//	groupArray(agg) * 2      Array(AggregateFunction(uniq, UInt64))
//	groupArray(groupArray(agg)) * 2
//	                         Array(Array(AggregateFunction(uniq, UInt64)))
//
// REFUSED, thus this closure reports ok = false and the caller answers a
// plain "cannot infer" error for every one of these:
//
//	agg * 2.5                code 43, a fractional constant
//	agg * -2                 code 43, a SIGNED constant (the literal -2
//	                          folds to Int8, never to a UInt class)
//	agg * toInt32(2)         code 43, a signed constant TYPE, even
//	                          though the value is non-negative: the
//	                          count must be spelled as an unsigned type
//	agg * toUInt256(2)       code 43, wider than UInt64
//	agg * '2'                code 43, a string constant
//	agg * u8                 code 44, a COLUMN, not a constant: the
//	                          count is not known at plan time even
//	                          though u8 is UInt8
//	groupArray(agg) * i32    code 43
//	groupArray(agg) * groupArray(agg)   code 43, the peer is a state too
//	agg + 2, agg % 2, agg - 2, agg / 2  code 43: no other operator has
//	                          a replication rule
//
// The predicate is: an AggregateFunction state (nested in Array to any
// depth) on one side of "*", the OTHER side a constant expression of an
// unsigned integer type no wider than UInt64. Being a constant EXPRESSION
// is necessary but not sufficient: the constant must also carry an
// unsigned type, because the server folds a literal such as -2 to Int8
// and refuses it. aggregateStateReplicationResultType therefore checks
// both isConstLiteralExpr (for the AST shape) and arithmeticIntegerClassOf
// (for the type), rather than trying to name every accepted operator or
// literal spelling by hand.
func aggregateStateReplicationResultType(
	left, right CHType,
	leftExpr, rightExpr clickhouse.Expr,
	scope queryScope,
) (CHType, bool) {
	leftIsState := aggregateStateOperand(left)
	rightIsState := aggregateStateOperand(right)
	if leftIsState == rightIsState {
		// Neither side is a state, or both sides are: no replication
		// rule applies. Two states multiplied together answers code
		// 43 (groupArray(agg) * groupArray(agg)); that refusal is
		// left to the caller.
		return CHType{}, false
	}
	state, count, countExpr := left, right, rightExpr
	if rightIsState {
		state, count, countExpr = right, left, leftExpr
	}
	if !isConstLiteralExpr(countExpr, scope) {
		return CHType{}, false
	}
	class, isInteger := arithmeticIntegerClassOf(count)
	if !isInteger || class.signed || class.size > 8 {
		return CHType{}, false
	}
	return state, true
}

// greatestLeastKeepsLowCardinality reports whether greatest or least
// keeps a LowCardinality wrapper of its argument. A call with ONE
// argument keeps it. A call with two or more arguments drops it.
//
// greatest and least have the class wrapperAggregate, and a true
// aggregate always drops LowCardinality. These two functions are scalar,
// thus they need this exception.
//
// Measured on ClickHouse 25.8.29.51 with real columns in a MergeTree
// table (a literal folds and gives a different answer):
//
//	lc   LowCardinality(String)
//	lcn  LowCardinality(Nullable(String))
//	lci  LowCardinality(Int64)
//	lcni LowCardinality(Nullable(Int64))
//	s    String, sn Nullable(String), i Int64
//
// One argument keeps the wrapper, for every inner type:
//
//	greatest(lc)      LowCardinality(String)
//	greatest(lcn)     LowCardinality(Nullable(String))
//	greatest(lci)     LowCardinality(Int64)
//	greatest(lcni)    LowCardinality(Nullable(Int64))
//
// Two or more arguments drop it, for every inner type and for every mix
// of arguments:
//
//	greatest(lc, lc)       String
//	greatest(lcn, lcn)     Nullable(String)
//	greatest(lci, lci)     Int64
//	greatest(lcni, lcni)   Nullable(Int64)
//	greatest(lc, s)        String
//	greatest(lcn, sn)      Nullable(String)
//	greatest(lc, lcn)      Nullable(String)
//	greatest(lc, lc, lc)   String
//
// least gives the same answer in every cell of this table.
//
// A true aggregate drops the wrapper at every arity, thus this
// exception must stay narrow (measured: max(lc) is String, min(lcn) is
// Nullable(String), any(lc) is String, argMax(lc, i) is String).
//
// The function takes no name argument. It reads call FACTS, not a
// function name: the one caller,
// greatestLeastKeepsLowCardinalityCondition in
// wrapper_transport.go, is wired only into the greatest/least
// transport, so a name parameter here could only ever be checked
// against a constant the caller itself supplied. An earlier version
// took a name parameter and compared it to the literal "greatest",
// which was a name-shaped branch in front of a fact-based rule with no
// second caller to justify it.
func greatestLeastKeepsLowCardinality(argCount int) bool {
	return argCount == 1
}

// greatestLeastNumericInner reports whether the inner type of a
// SimpleAggregateFunction makes greatest and least drop the wrapper, at
// arity 2 or more. Only a numeric type does this, and a Nullable of a
// numeric type does it too. A Decimal with parameters is numeric here
// (measured: greatest(saggdec, dec) is Decimal(38, 4)).
//
// greatest and least are NOT members of the branch-supertype family. The
// branch family (if, multiIf, coalesce) and the aggregate family (max)
// KEEP the wrapper. greatest and least drop it when they compute a
// numeric supertype over two or more arguments. This function is the
// scalar half of that rule; the shared, non-numeric-inner half (which
// applies at every arity, including arity 1) is
// simpleAggregateMarkerSurvivesValuePreserving.
//
// Measured on ClickHouse 25.8.29.51 through the HTTP interface, against
// real columns of a table, never over literals, because the server folds
// constants. The columns are sagg SimpleAggregateFunction(sum, Int64),
// saggf SimpleAggregateFunction(sum, Float64), saggn
// SimpleAggregateFunction(sum, Nullable(Int64)), saggu
// SimpleAggregateFunction(max, UInt8), saggdec
// SimpleAggregateFunction(sum, Decimal(38, 4)), saggs
// SimpleAggregateFunction(min, String), saggns
// SimpleAggregateFunction(min, Nullable(String)), saggfs
// SimpleAggregateFunction(min, FixedString(4)), saggd
// SimpleAggregateFunction(max, Date), saggdt SimpleAggregateFunction(max,
// DateTime), saggip SimpleAggregateFunction(max, IPv4), i64 Int64, u8
// UInt8, f64 Float64, dec Decimal(18, 4), s String and d Date.
//
// Two or more arguments with a NUMERIC inner type DROP the wrapper:
//
//	greatest(sagg, i64)       Int64
//	least(sagg, i64)          Int64
//	greatest(sagg, sagg)      Int64
//	least(sagg, sagg)         Int64
//	greatest(sagg, 3)         Int64
//	greatest(saggf, f64)      Float64
//	greatest(saggu, u8)       UInt8
//	greatest(saggu, i64)      Int64
//	greatest(saggn, i64)      Nullable(Int64)
//	greatest(saggdec, dec)    Decimal(38, 4)
//	greatest(saggns, s)       Nullable(String)
//
// A NON-numeric SCALAR inner type KEEPS the wrapper at arity 2 or more,
// because the server takes a different path there and gives back the
// first argument type as it is:
//
//	greatest(saggs, s)          SimpleAggregateFunction(min, String)
//	greatest(saggs, saggs)      SimpleAggregateFunction(min, String)
//	greatest(saggfs, saggfs)    SimpleAggregateFunction(min, FixedString(4))
//	greatest(saggd, d)          SimpleAggregateFunction(max, Date)
//	greatest(saggdt, saggdt)    SimpleAggregateFunction(max, DateTime)
//	greatest(saggip, saggip)    SimpleAggregateFunction(max, IPv4)
//	greatest(saggns, saggns)    SimpleAggregateFunction(min, Nullable(String))
//
// ONE argument always KEEPS the wrapper, for a numeric inner type too,
// because the server computes no supertype:
//
//	greatest(sagg)       SimpleAggregateFunction(sum, Int64)
//	least(sagg)          SimpleAggregateFunction(sum, Int64)
//	greatest(saggf)      SimpleAggregateFunction(sum, Float64)
//	greatest(saggdec)    SimpleAggregateFunction(sum, Decimal(38, 4))
//	greatest(saggs)      SimpleAggregateFunction(min, String)
//
// The values were also selected, not only analysed with toTypeName: for
// the row (sagg 5, i64 3) greatest(sagg, i64) gives 5 and least(sagg,
// i64) gives 3.
//
// The unwrap does not widen the refusal boundary. A mixed numeric pair
// that the server refuses stays refused: greatest(sagg, saggf) answers
// Code: 43 ("Illegal types SimpleAggregateFunction(sum, Int64) and
// SimpleAggregateFunction(sum, Float64) of arguments of function
// greatest").
//
// A COMPOSITE inner type (Array, Tuple, Map or LowCardinality) is a
// separate case, decided before this function ever runs: the marker does
// not survive over such an inner type at ANY arity, including arity 1.
// See simpleAggregateMarkerSurvivesValuePreserving for that measurement.
func greatestLeastNumericInner(inner CHType) bool {
	if strings.EqualFold(inner.Name, "Nullable") && len(inner.Params) == 1 {
		inner = inner.Params[0]
	}
	if isDecimalWithParams(inner) {
		return true
	}
	return numericCHType(inner)
}

// splitCHWrappers removes the LowCardinality and Nullable wrappers from a
// type and reports which wrappers were present. It understands the nested
// form LowCardinality(Nullable(X)).
//
// It does NOT look inside a SimpleAggregateFunction(f, X) marker. A
// caller that needs the Nullable of X asks branchArgumentNullable, which
// reports the value's nullability through the marker. The two are kept
// apart on purpose: this function answers "which wrappers are on the
// type", while branchArgumentNullable answers "can the value be null",
// and a marker-KEEPING function needs the first answer only.
func splitCHWrappers(value CHType) (base CHType, nullable, lowCardinality bool) {
	base = value
	if strings.EqualFold(base.Name, "LowCardinality") && len(base.Params) == 1 {
		lowCardinality = true
		base = base.Params[0]
	}
	if strings.EqualFold(base.Name, "Nullable") && len(base.Params) == 1 {
		nullable = true
		base = base.Params[0]
	}
	return base, nullable, lowCardinality
}

// stripNestedLowCardinality removes every LowCardinality wrapper from a
// type, at the top level and at every depth inside Array, Map and Tuple.
// A LowCardinality(Nullable(X)) member becomes Nullable(X), because only
// the LowCardinality wrapper comes off.
//
// An aggregate result never keeps a LowCardinality wrapper, and the
// removal is not limited to the top level: the server removes the
// wrapper inside a container member as well. The rule is about the
// aggregate, not about the container. A plain constructor keeps the
// wrapper at every depth, thus only the aggregate paths call this.
//
// Measured on ClickHouse 25.8.29.51 with real columns in a real table
// (i32 Int32, ni32 Nullable(Int32), lc LowCardinality(String),
// arr Array(LowCardinality(String)), m Map(String, LowCardinality(String))),
// never on literals alone, because the server folds constants:
//
//	SELECT toTypeName(tuple(i32, lc))   Tuple(Int32, LowCardinality(String))
//	SELECT toTypeName(array(lc))        Array(LowCardinality(String))
//	SELECT toTypeName(map('k', lc))     Map(String, LowCardinality(String))
//
// The constructors keep the wrapper. Every aggregate removes it, at
// every depth:
//
//	SELECT toTypeName(argMin((i32, lc), ni32))   Tuple(Int32, String)
//	SELECT toTypeName(argMin(arr, ni32))         Array(String)
//	SELECT toTypeName(argMin(m, ni32))           Map(String, String)
//	SELECT toTypeName(any(tuple(i32, lc)))       Tuple(Int32, String)
//	SELECT toTypeName(groupArray(lc))            Array(String)
//	SELECT toTypeName(argMin(tuple(tuple(tuple(lc))), ni32))
//	                                             Tuple(Tuple(Tuple(String)))
//
// Before this rule chgen answered Tuple(Int32, LowCardinality(String))
// for the first cell and Array(LowCardinality(String)) for the second.
// Those types do not come back from the server, and clickhouse-go scans
// the two forms differently, thus the generated Go was silently wrong.
func stripNestedLowCardinality(value CHType) CHType {
	if strings.EqualFold(value.Name, "LowCardinality") && len(value.Params) == 1 {
		return stripNestedLowCardinality(value.Params[0])
	}
	if len(value.Params) == 0 {
		return value
	}
	params := make([]CHType, len(value.Params))
	for index, param := range value.Params {
		params[index] = stripNestedLowCardinality(param)
	}
	result := value
	result.Params = params
	return result
}

// canBeInsideNullable reports whether ClickHouse accepts the type inside a
// Nullable wrapper. Array, Map, Tuple, AggregateFunction, Dynamic and
// Variant cannot go inside Nullable.
//
// Measured on ClickHouse 25.8.29.51:
//
//	SELECT toTypeName(CAST(NULL AS Nullable(Array(Int32))))
//	Code: 43. Nested type Array(Int32) cannot be inside Nullable type
//
// The same error comes back for Map(String, Int32), for
// Tuple(Int32, String), for AggregateFunction(uniq, UInt64), for Dynamic
// and for Variant. The Dynamic rule is also visible through an aggregate result:
// argMaxIf(dyn, ni32, b) is Dynamic, not Nullable(Dynamic), although the
// same Nullable key makes argMaxIf(i32, ni32, b) Nullable(Int32).
// Int64 is accepted and gives Nullable(Int64).
//
// SimpleAggregateFunction is a different type and IS accepted:
//
//	SELECT toTypeName(CAST(NULL AS Nullable(SimpleAggregateFunction(sum, Int64))))
//	Nullable(SimpleAggregateFunction(sum, Int64))
//
// For this reason the test below compares the whole name. A prefix test
// would catch SimpleAggregateFunction too and remove a Nullable that the
// server keeps.
//
// In this position the server drops the Nullable; it does not refuse the
// call. Measured on the same server with real columns in a real table
// (arr_n Array(Nullable(Int32)), ndec Nullable(Decimal(9, 4)),
// st AggregateFunction(uniq, UInt64), ni32 Nullable(Int32)):
//
//	SELECT toTypeName(argMin(arr_n, ndec)) FROM t GROUP BY g
//	Array(Nullable(Int32))
//	SELECT toTypeName(argMin(st, ni32)) FROM a GROUP BY g
//	AggregateFunction(uniq, UInt64)
//
// The ordering argument is Nullable, but the result is not Nullable.
func canBeInsideNullable(base CHType) bool {
	switch strings.ToLower(base.Name) {
	case "array", "map", "tuple", "aggregatefunction", "dynamic", "variant":
		return false
	case "point", "ring", "linestring", "polygon", "multilinestring", "multipolygon":
		// Geometry aliases are tuples or arrays, neither legal in Nullable.
		return false
	default:
		return true
	}
}

// applyCHWrappers puts the wrappers back in the ClickHouse nesting order
// LowCardinality(Nullable(X)). A base that is already Nullable is not
// wrapped twice. A base that cannot go inside Nullable keeps no Nullable
// wrapper, because the server gives back the bare type. The same holds
// for LowCardinality: a base whose family the server refuses inside that
// wrapper keeps no wrapper. See wrapLowCardinality.
//
// Both wrappers are built by the two constructors of
// wrapper_transport.go, never by hand. wrapNullable holds the same
// "already Nullable, and canBeInsideNullable" rule that this function
// used to inline, thus the call is behaviour-identical and there is now
// one owner of the rule instead of two copies that can drift.
func applyCHWrappers(base CHType, nullable, lowCardinality bool) CHType {
	result := base
	if nullable {
		result = wrapNullable(result)
	}
	if lowCardinality {
		result = wrapLowCardinality(result)
	}
	return result
}

// isConstLiteralExpr reports whether the expression is a constant literal
// in the SQL text. ClickHouse keeps a LowCardinality result only when the
// other arguments of the function are constants.
//
// The scope is needed for the `if` fold below. A caller that has no
// scope passes the zero queryScope; the fold then simply declines, which
// is the conservative answer.
func isConstLiteralExpr(expression clickhouse.Expr, scope queryScope) bool {
	switch expr := expression.(type) {
	case *clickhouse.NumberLiteral, *clickhouse.StringLiteral, *clickhouse.BoolLiteral:
		return true
	case *clickhouse.Ident:
		// The parser gives the true and false keywords an Ident. They
		// are constant Bool literals only when no scalar or column
		// shadows the name.
		if !strings.EqualFold(expr.Name, "true") && !strings.EqualFold(expr.Name, "false") {
			return false
		}
		if _, shadowed := scope.lookupScalar(expr.Name); shadowed {
			return false
		}
		if _, err := scope.lookupColumn("", expr.Name); err == nil {
			return false
		}
		return true
	case *clickhouse.ColumnExpr:
		return isConstLiteralExpr(expr.Expr, scope)
	case *clickhouse.UnaryExpr:
		return isConstLiteralExpr(expr.Expr, scope)
	case *clickhouse.BinaryOperation:
		return isConstLiteralExpr(expr.LeftExpr, scope) && isConstLiteralExpr(expr.RightExpr, scope)
	case *clickhouse.CastExpr:
		return isConstLiteralExpr(expr.Expr, scope)
	case *clickhouse.FunctionExpr:
		// ClickHouse folds a non-aggregate function of constants
		// into a constant (measured on ClickHouse 25.8.29.51:
		// concat(lc, trim(toString(-2.5))) and
		// concat(lc, toString(now())) keep the LowCardinality
		// wrapper; concat(lc, toString(u8)) does not).
		if isAggregateFunctionName(strings.ToLower(expr.Name.Name)) {
			return false
		}
		// `if` with a constant condition folds to its taken branch
		// BEFORE the other branch is looked at. Thus the whole `if`
		// is a constant when the TAKEN branch is a constant, even
		// when the dead branch names a column. Measured on ClickHouse
		// 25.8.29.51 with the real fixture columns:
		//
		//	if(false, i16, -129) % length(lcn)
		//	    LowCardinality(Nullable(Int64))
		//	length(lc) + if(true, 1, i16)   LowCardinality(Int64)
		//	concat(lc, if(false, s, 'x'))   LowCardinality(String)
		//
		// Only `if` folds this way. multiIf and CASE give a type to
		// every branch and do not drop a column operand:
		//
		//	length(lc) + multiIf(false, i16, 1)              Int64
		//	length(lc) + (CASE WHEN false THEN i16 ELSE 1 END)
		//	                                                 Int64
		//
		// A non-constant condition does not fold either, thus the
		// generic "every argument is constant" rule below still
		// applies there (length(lc) + if(b, 1, 2) is UInt64).
		if strings.EqualFold(expr.Name.Name, "if") {
			args := functionArgs(expr)
			if len(args) == 3 {
				if truth, constant := constantConditionTruth(args[0], scope); constant {
					taken := args[2]
					if truth {
						taken = args[1]
					}
					return isConstLiteralExpr(taken, scope)
				}
			}
		}
		for _, arg := range functionArgs(expr) {
			if !isConstLiteralExpr(arg, scope) {
				return false
			}
		}
		return true
	case *clickhouse.CaseExpr:
		// A CASE is a constant only when every explicit expression in
		// it is a constant. ClickHouse still reads every branch for this
		// decision. It does not remove an unused column branch because a
		// WHEN condition is true. A missing ELSE is an implicit NULL and
		// adds no non-constant expression.
		//
		// Measured on ClickHouse 25.8.29.51 with a real
		// LowCardinality(Nullable(String)) column:
		//
		//	lcn || CASE WHEN true THEN 'a' END
		//	    LowCardinality(Nullable(String))
		//	lcn || CASE WHEN i32 > 0 THEN 'a' END
		//	    Nullable(String)
		//	lcn || CASE WHEN true THEN 'a' ELSE s END
		//	    Nullable(String)
		if expr.Expr != nil && !isConstLiteralExpr(expr.Expr, scope) {
			return false
		}
		for _, when := range expr.Whens {
			if when.When == nil || when.Then == nil ||
				!isConstLiteralExpr(when.When, scope) ||
				!isConstLiteralExpr(when.Then, scope) {
				return false
			}
		}
		return expr.Else == nil || isConstLiteralExpr(expr.Else, scope)
	case *clickhouse.ParamExprList:
		// A parenthesized literal stays a constant (measured on
		// ClickHouse 25.8.29.51: concat(lc, ('a')) keeps the
		// LowCardinality wrapper).
		if expr.Items != nil && len(expr.Items.Items) == 1 {
			return isConstLiteralExpr(expr.Items.Items[0], scope)
		}
	}
	return false
}

// aggregateDataArgCount gives the number of leading arguments that carry
// data into an aggregate result. A condition argument of an -If form and
// the comparison key beyond the measured ones do not make the result
// Nullable. Measured on ClickHouse 25.8.29.51: avgIf(i32, nb) is Float64,
// argMax(f64, ni32) is Nullable(Float64), greatest(i32, ni32) is
// Nullable(Int32).
func aggregateDataArgCount(name string, argCount int) int {
	switch name {
	// Every argument of uniqCombined carries data. Measured on
	// ClickHouse 25.8.29.51 with real columns: uniqCombined(s, ns) and
	// uniqCombined(ns, s) are both Nullable(UInt64), thus a Nullable in
	// any position makes the result Nullable.
	case "uniqcombined", "uniqcombined64":
		return argCount
	// The -If form keeps its last argument as the condition, and the
	// condition does not carry data. Measured on the same server:
	// uniqCombinedIf(s, ni > 0) is UInt64, but
	// uniqCombinedIf(ns, i > 0) is Nullable(UInt64).
	case "uniqcombinedif":
		if argCount > 1 {
			return argCount - 1
		}
		return argCount
	case "argmax", "argmin", "greatest", "least":
		return argCount
	case "argmaxif", "argminif":
		if argCount > 2 {
			return 2
		}
		return argCount
	default:
		if argCount > 1 {
			return 1
		}
		return argCount
	}
}

// transparentWrapperFlags computes the result wrappers of a
// wrapperTransparent function from its resolved argument types.
//
// The name is needed for the Dynamic rule below: only a function that a
// measurement marked dynamicForcesNullable gains a Nullable wrapper
// from a Dynamic argument. See the dynamicForcesNullable field in
// registry.go for the measured accept-set, and for the reason the
// function CLASS cannot decide this on its own.
func transparentWrapperFlags(name string, args []clickhouse.Expr, argTypes []CHType, scope queryScope) (nullable, lowCardinality bool) {
	lowCardinalityCount := 0
	othersConstant := true
	dynamicWraps := functionDynamicForcesNullableFor(name)
	for index, argType := range argTypes {
		// A Dynamic argument makes the result of a MARKED function
		// Nullable. The null does not live in a wrapper ON the type,
		// thus splitCHWrappers and branchArgumentNullable cannot see
		// it: a Dynamic column carries the "there may be no value"
		// case in the family itself. Measured on ClickHouse
		// 25.8.29.51 in BOTH operand positions, paired with
		// ignore(...): equals(dyn, i32) and equals(i32, dyn) are both
		// Nullable(UInt8), while equals(i32, i32) is the bare UInt8.
		//
		// This must stay tied to the mark. A Dynamic cannot go inside
		// Nullable at all (toNullable(dyn) is Code 43), thus a general
		// rule here would make the type-passthrough functions answer
		// the impossible Nullable(Dynamic).
		if dynamicWraps && isDynamicCHType(argType) {
			nullable = true
		}
		argLowCardinality := branchArgumentLowCardinality(argType)
		// A SimpleAggregateFunction(f, Nullable(T)) argument makes the
		// result Nullable, exactly as a bare Nullable(T) argument does.
		// The server READS THROUGH the marker and answers about the
		// value inside it, thus splitCHWrappers alone does not see the
		// null: it reports the wrappers ON the type and the null lives
		// in the inner type. This is the aggregate rule a few lines
		// below, applied to the transparent class as well.
		//
		// Measured on ClickHouse 25.8.29.51 with real columns in a real
		// table, never over literals, because the server folds
		// constants (safn is SimpleAggregateFunction(anyLast,
		// Nullable(Int32)), saf is SimpleAggregateFunction(anyLast,
		// Int32), safns is SimpleAggregateFunction(anyLast,
		// Nullable(String)), safs is SimpleAggregateFunction(anyLast,
		// String)):
		//
		//	toString(safn)     Nullable(String)   toString(saf)   String
		//	toInt64(safn)      Nullable(Int64)    toInt64(saf)    Int64
		//	toDate(safn)       Nullable(Date)     toDate(saf)     Date
		//	hex(safn)          Nullable(String)   hex(saf)        String
		//	cityHash64(safn)   Nullable(UInt64)   cityHash64(saf) UInt64
		//	length(safns)      Nullable(UInt64)   length(safs)    UInt64
		//	concat(safns, 'x') Nullable(String)   concat(safs,'x') String
		//	trim(safns)        Nullable(String)   trim(safs)      String
		//	empty(safns)       Nullable(UInt8)    empty(safs)     UInt8
		nullable = nullable || branchArgumentNullable(argType)
		if argLowCardinality {
			lowCardinalityCount++
			continue
		}
		if index >= len(args) || !isConstLiteralExpr(args[index], scope) {
			othersConstant = false
		}
	}
	return nullable, lowCardinalityCount == 1 && othersConstant
}

// functionWrapperFlags computes the result wrappers for a classified
// function from its argument expressions and resolved argument types.
//
// The returned lowCardinality is the FACT "does the first argument
// offer a LowCardinality wrapper", not yet the function's disposition.
// greatest and least (see greatestLeastTransport) decide the
// disposition themselves, from this fact and the call's argCount,
// through their own lowCardinalityWhen condition; the caller must not
// resolve the answer here and lock it, or the transport's condition
// would never run. Every other function in this class drops
// LowCardinality unconditionally, so the fact and the disposition
// happen to coincide there, but the field still means "the fact", not
// "the answer".
func functionWrapperFlags(class functionWrapperClass, name string, args []clickhouse.Expr, argTypes []CHType, scope queryScope) (nullable, lowCardinality bool) {
	if class == wrapperAggregate {
		for _, argType := range argTypes[:aggregateDataArgCount(name, len(argTypes))] {
			// A SimpleAggregateFunction(f, Nullable(T)) argument makes
			// the result Nullable. The null lives in the inner type,
			// thus splitCHWrappers alone does not see it (measured on
			// ClickHouse 25.8.29.51 with real columns: avg(saggn) is
			// Nullable(Float64), as avg(ni64) is, while avg(sagg) is
			// the bare Float64).
			nullable = nullable || branchArgumentNullable(argType)
		}
		if len(argTypes) == 0 {
			return nullable, false
		}
		// The question is what the VALUE can be, not which wrappers
		// sit on the type. A SimpleAggregateFunction(f, LowCardinality(T))
		// argument hides the wrapper from splitCHWrappers, and the
		// wrapperCall stack built from splitWrapperStack would then
		// lose a wrapper that the server keeps. Measured:
		// greatest(saflc) is LowCardinality(Int32), not
		// SimpleAggregateFunction(anyLast, Int32). See
		// branchArgumentLowCardinality. This fact feeds
		// greatestLeastTransport.lowCardinalityWhen; it is not itself
		// the disposition.
		return nullable, branchArgumentLowCardinality(argTypes[0])
	}
	return transparentWrapperFlags(name, args, argTypes, scope)
}

// applyFunctionWrappers puts the result wrappers of a classified function
// on its result type.
//
// It is a thin adapter over the ONE applier: it evaluates the rules that
// need the argument EXPRESSIONS (the transparent LowCardinality rule and
// the aggregate Nullable rule), then hands a resolved transport to
// applyWrapperTransport. Every decision about which wrappers survive
// lives in the transport, not here. See wrapper_transport.go.
func applyFunctionWrappers(result CHType, class functionWrapperClass, name string, args []clickhouse.Expr, argTypes []CHType, scope queryScope) CHType {
	nullable, lowCardinality := functionWrapperFlags(class, name, args, argTypes, scope)
	transport := transportForFunction(name, class)
	if class != wrapperAggregate {
		// The transparent class's LowCardinality rule needs the
		// argument EXPRESSIONS (exactly one LowCardinality argument,
		// every other argument a constant literal), which
		// functionWrapperFlags already evaluated above. wrapperCall
		// carries no expressions, thus the answer must be resolved and
		// locked here, before applyWrapperTransport ever reads
		// lowCardinalityWhen.
		transport = transport.withResolvedLowCardinality(lowCardinality)
	}
	// For the aggregate class, `lowCardinality` above is the FACT
	// "does the first argument offer the wrapper" (see
	// functionWrapperFlags), fed into wrapperCall.stacks below.
	// greatestLeastTransport.lowCardinalityWhen (the only conditional
	// disposition this class carries) decides the arity-1 exception
	// from that fact and call.argCount; every other aggregate has a
	// FIXED wrapperDrop disposition, thus withResolvedLowCardinality
	// would be a no-op for them and MUST NOT run for greatest/least,
	// or their arity condition would never be consulted.
	// The SimpleAggregateFunction wrapper of the FIRST argument goes
	// into the stack, so that the transport can decide whether the
	// result carries it back. Only the first argument can carry it into
	// the result, exactly as in commonCHTypes.
	//
	// Without this the wrapper could never survive on this path,
	// because the caller removes it before the rule runs and nothing
	// put it back. The transport still owns the decision: greatest and
	// least keep it under their own measured condition (measured on
	// ClickHouse 25.8.29.51 with real columns: greatest(sagg) is
	// SimpleAggregateFunction(sum, Int64), and greatest(sagg, i64) is
	// the bare Int64).
	//
	// A function that COMPUTES from the value drops the marker, and it
	// must not even offer it to the transport, because the class
	// transport of an aggregate keeps the marker by default (measured:
	// avg(sagg) is Float64, not SimpleAggregateFunction(sum, Float64)).
	// "Has a measured argument domain" is the proxy for "computes from
	// the value", and an explicit entry in wrapperTransportOverrides
	// marks the measured exception. This is the same test that the
	// argsFirstOnly path in inferFunctionType applies, so that the two
	// paths cannot disagree.
	// A FIXED rule answers the same type for every argument, thus it
	// computes a new value and cannot carry the marker either
	// (measured: uniqCombined(saf) is UInt64). uniqCombined has the
	// class wrapperAggregate and no argument domain, so the domain test
	// alone does not catch it. See fixedResultFunctionType.
	base, firstStack := splitWrapperStack(firstArgTypeOrZero(argTypes))
	if _, keepsMarker := wrapperTransportOverrides[name]; !keepsMarker {
		_, constrained := argumentDomainFor(name)
		rule, hasRule := functionRuleFor(name)
		if constrained || (hasRule && fixedResultFunctionType(rule)) {
			firstStack.simpleAggregate = nil
		}
	}
	return applyWrapperTransport(
		result,
		transport,
		wrapperCall{
			base: base,
			stacks: []wrapperStack{{
				lowCardinality:  lowCardinality,
				nullable:        nullable,
				simpleAggregate: firstStack.simpleAggregate,
			}},
			argCount: len(argTypes),
		},
	)
}

// firstArgTypeOrZero gives the first argument type, or the zero CHType
// when the call has no arguments. A condition that reads the base type
// then sees an empty type, which no measured condition accepts.
func firstArgTypeOrZero(argTypes []CHType) CHType {
	if len(argTypes) == 0 {
		return CHType{}
	}
	return argTypes[0]
}
