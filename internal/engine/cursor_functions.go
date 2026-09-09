package engine

import (
	"fmt"
	"strings"

	clickhouse "github.com/AfterShip/clickhouse-sql-parser/parser"
)

var bitShiftIntegerDomain = argumentDomain{
	name: "bit-shift integer",
	accepts: func(value CHType) bool {
		kind, ok := arithmeticIntegerClassOf(value)
		return ok && kind.size <= 8
	},
	expected: "an integer of at most 64 bits or Bool",
}

// Measured on ClickHouse 25.8.29.51 over real columns: bitShiftRight
// uses the wider operand and is signed if either operand is signed.
// In particular UInt8 shifted by a UInt64 column returns UInt64, not UInt8.
// Wrapper propagation is owned by the ordinary scalar transport.
func bitShiftRightFunctionType(args []CHType) (CHType, error) {
	if len(args) != 2 {
		return CHType{}, fmt.Errorf("function bitShiftRight needs two arguments")
	}
	for _, arg := range args {
		if err := checkArgumentDomain("bitShiftRight", bitShiftIntegerDomain, arg); err != nil {
			return CHType{}, err
		}
	}
	left, _ := arithmeticIntegerClassOf(args[0])
	right, _ := arithmeticIntegerClassOf(args[1])
	result := arithmeticIntegerClass{signed: left.signed || right.signed, size: max(left.size, right.size)}
	return CHType{Name: arithmeticIntegerTypeNames[result]}, nil
}

func inferUnixMillisecondsType(displayName string, args []clickhouse.Expr, scope queryScope) (CHType, error) {
	if len(args) < 1 || len(args) > 2 {
		return CHType{}, fmt.Errorf("function %s needs an integer and an optional constant timezone", displayName)
	}
	value, err := inferExprType(args[0], scope)
	if err != nil {
		return CHType{}, fmt.Errorf("function %s first argument: %w", displayName, err)
	}
	base, _ := splitWrapperStack(value)
	if err := checkArgumentDomain(displayName, bitShiftIntegerDomain, base); err != nil {
		return CHType{}, err
	}
	result := CHType{Name: "DateTime64", LiteralParams: []string{"3"}}
	argTypes := []CHType{value}
	if len(args) == 2 {
		zone, ok := unwrapColumnExpr(args[1]).(*clickhouse.StringLiteral)
		if !ok {
			return CHType{}, fmt.Errorf("function %s needs a constant timezone", displayName)
		}
		quoted := "'" + strings.Trim(zone.Literal, "'") + "'"
		if err := validateMeasuredTimezone(quoted); err != nil {
			return CHType{}, err
		}
		result.LiteralParams = append(result.LiteralParams, quoted)
		argTypes = append(argTypes, CHType{Name: "String"})
	}
	return applyFunctionWrappers(result, wrapperTransparent, "fromunixtimestamp64milli", args, argTypes, scope), nil
}
