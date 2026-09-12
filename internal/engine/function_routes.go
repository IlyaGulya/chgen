package engine

import (
	"strings"

	clickhouse "github.com/AfterShip/clickhouse-sql-parser/parser"
)

// expressionFunctionRoutes is executable dispatch, not a second support
// allowlist. Registry guards consult this table instead of restating names
// from a switch. A route does not constitute measured support on its own.
var expressionFunctionRoutes map[string]func(*clickhouse.FunctionExpr, queryScope) (CHType, error)

func init() {
	expressionFunctionRoutes = map[string]func(*clickhouse.FunctionExpr, queryScope) (CHType, error){
		"fromunixtimestamp64milli": routeArguments(inferUnixMillisecondsType),
		"tupleelement":             routeArguments(inferTupleElementType),
		"tostartofinterval":        routeArguments(inferToStartOfIntervalType),
		"totimezone":               routeArguments(inferToTimeZoneType),
		"todatetime":               routeTimezone,
		"todatetime64":             routeTimezone,
		"tostartofday":             routeTimezone,
		"tostartofhour":            routeTimezone,
		"tostartofminute":          routeTimezone,
		"now":                      routeTimezone,
		"now64":                    routeTimezone,
		"and":                      routeLogic,
		"or":                       routeLogic,
		"xor":                      routeLogic,
	}
}

func routeArguments(infer func(string, []clickhouse.Expr, queryScope) (CHType, error)) func(*clickhouse.FunctionExpr, queryScope) (CHType, error) {
	return func(function *clickhouse.FunctionExpr, scope queryScope) (CHType, error) {
		return infer(function.Name.Name, functionArgs(function), scope)
	}
}

func routeTimezone(function *clickhouse.FunctionExpr, scope queryScope) (CHType, error) {
	return inferTimezoneCarryingType(strings.ToLower(function.Name.Name), function, functionArgs(function), scope)
}

func routeLogic(function *clickhouse.FunctionExpr, scope queryScope) (CHType, error) {
	return inferLogicOperatorFunctionType(strings.ToLower(function.Name.Name), function.Name.Name, functionArgs(function), scope)
}
