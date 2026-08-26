package engine

import (
	"fmt"
	"reflect"
	"strings"

	clickhouse "github.com/AfterShip/clickhouse-sql-parser/parser"
)

// ClickHouse always refuses an aggregate function that appears in the
// argument subtree of another aggregate function:
//
//	DESCRIBE (SELECT any(any(x)) FROM t184)
//	-- Code: 184. Aggregate function any(x) is found inside another
//	-- aggregate function in query. (ILLEGAL_AGGREGATION)
//
// Without this check chgen gives such a call a type, thus the generated
// query fails at run time and not at generation time.
//
// The boundary was measured on ClickHouse 25.8.29.51 over the columns of a
// real table (x Int32, y Float64, s String), never over literals:
//
//	REFUSED, Code 184
//	  any(any(x))                          direct nesting
//	  any(abs(sum(x)))                     through a scalar function
//	  any(toString(sum(x)))                through a scalar function
//	  any(x + sum(x))                      through arithmetic
//	  any(if(x > 1, sum(x), 0))            through a branch
//	  any(arrayMap(v -> v + 1, groupArray(x)))  through a lambda body
//	  any(sumIf(x, x > 1))                 -If combinator
//	  any(sumState(x))                     -State combinator
//	  sumMerge(sumState(x))                -Merge combinator
//	  any(quantile(0.5)(x))                parametric aggregate
//	  quantile(0.5)(sum(x))                parametric outer aggregate
//
//	ACCEPTED, and it runs
//	  sum(x) + sum(y)                      arithmetic OVER aggregates
//	  abs(sum(x))                          scalar function OVER an aggregate
//	  toString(sum(x))                     scalar function OVER an aggregate
//	  sum(sum(x)) OVER ()                  window function, a new frame
//	  any((SELECT sum(x) FROM t184))       subquery, a new scope
//	  any(greatest(x, toInt32(y)))         greatest is NOT an aggregate
//	  any(least(x, 1))                     least is NOT an aggregate
//	  any(arraySlice(arr, 1, 2))           arraySlice is NOT an aggregate
//	  any(arraySort(arr))                  arraySort is NOT an aggregate
//	  any(arrayDistinct(arr))              arrayDistinct is NOT an aggregate
//	  any(arrayResize(arr, 2))             arrayResize is NOT an aggregate
//	  arraySort(groupUniqArray(x))         a scalar OVER an aggregate
//
// The refusal therefore has exactly two exits, and both are measured:
// a window function and a subquery each open a new aggregation scope, so
// an aggregate below them is legal and the walk must stop there. A wider
// refusal would break a query that runs.

// scalarNamesInTheAggregateClass lists the lowercase names that carry the
// class wrapperAggregate although the function is an ORDINARY scalar, not
// an aggregate. The class marks how a function treats the LowCardinality
// and Nullable wrappers of its arguments; it does NOT mark an aggregate,
// so it cannot serve as the name test on its own.
//
// The registry holds 34 names with class wrapperAggregate. Each one was
// measured inside any(...) on ClickHouse 25.8.29.51 over real columns.
// EXACTLY these six answer a type and run; the other 28 answer Code 184:
//
//	SELECT any(arrayDistinct(arr)), any(arraySlice(arr, 1, 2)),
//	       any(arraySort(arr)), any(arrayResize(arr, 2)),
//	       any(greatest(x, 1)), any(least(x, 1)) FROM t3   -- all run
//
// A refusal that held any of these six would be wider than the server and
// would break a query that runs.
var scalarNamesInTheAggregateClass = map[string]bool{
	"arraydistinct": true,
	"arrayresize":   true,
	"arrayslice":    true,
	"arraysort":     true,
	"greatest":      true,
	"least":         true,
}

// knownAggregateNames lists the aggregates that the registry does not
// carry with class wrapperAggregate. Each name here was measured inside
// any(...) on ClickHouse 25.8.29.51 over real columns and gave Code 184.
//
// The list does not have to be complete to be correct. A name that is
// missing keeps the OLD behaviour for that one shape, which is a missing
// refusal. A name that is wrongly present would break a query that runs,
// which is worse, so nothing enters this list without a measurement.
var knownAggregateNames = map[string]bool{
	"count":          true,
	"countdistinct":  true,
	"uniq":           true,
	"uniqexact":      true,
	"uniqhll12":      true,
	"uniqtheta":      true,
	"grouparray":     true,
	"groupuniqarray": true,
	"topk":           true,
	"stddevpop":      true,
	"stddevsamp":     true,
	"varpop":         true,
	"varsamp":        true,
	"corr":           true,
	"covarpop":       true,
	"covarsamp":      true,
	"entropy":        true,
	"sumkahan":       true,
	"avgweighted":    true,
	"maxmap":         true,
	"minmap":         true,
	"summap":         true,
	"sumcount":       true,
	"deltasum":       true,
	"histogram":      true,
}

// isAggregateCallName reports whether the lowercase function name names an
// aggregate function, with its combinator suffixes resolved. It is the
// name test of the Code 184 rule and nothing else.
//
// It does NOT reuse isAggregateFunctionName. That helper answers a
// different question, "does the result depend on the rows", for constant
// folding: it reports true for arraySlice, arraySort and arrayDistinct,
// which are scalars that RUN inside an aggregate, and it reports false for
// uniqHLL12 and uniqTheta, which are aggregates that do not. It is wrong
// for this rule in both directions.
func isAggregateCallName(name string) bool {
	if scalarNamesInTheAggregateClass[name] {
		return false
	}
	if knownAggregateNames[name] {
		return true
	}
	if functionClassFor(name) == wrapperAggregate {
		return true
	}
	// A combinator suffix over a base aggregate is still an aggregate:
	// sumIf, sumState, sumMerge and sumOrNull all answer Code 184
	// inside any(...).
	base, _, split := splitAggregateCombinator(name)
	return split && !scalarNamesInTheAggregateClass[base]
}

// checkNestedAggregateArgs refuses a call whose argument subtree holds an
// aggregate function, when the call is itself an aggregate. It names the
// server error, so the reader of the refusal can find the same shape in
// the ClickHouse message.
func checkNestedAggregateArgs(name, spelledName string, args []clickhouse.Expr) error {
	if !isAggregateCallName(name) {
		return nil
	}
	for _, arg := range args {
		inner, found, err := findNestedAggregate(arg)
		if err != nil {
			return fmt.Errorf("check aggregate function %s argument: %w", spelledName, err)
		}
		if found {
			return fmt.Errorf(
				"aggregate function %s has aggregate function %s in its argument; ClickHouse refuses this with Code: 184 (ILLEGAL_AGGREGATION): aggregate function is found inside another aggregate function in query",
				spelledName, inner)
		}
	}
	return nil
}

// findNestedAggregate walks an argument subtree and gives the spelled name
// of the first aggregate call it meets. The walk STOPS at a window
// function and at a subquery, because each opens a new aggregation scope
// and an aggregate below them is legal (measured: sum(sum(x)) OVER () and
// any((SELECT sum(x) FROM t184)) both run).
func findNestedAggregate(expression clickhouse.Expr) (string, bool, error) {
	var inner string
	err := walkSemanticExpressions(expression, func(expr clickhouse.Expr) semanticWalkAction {
		switch expr := expr.(type) {
		case *clickhouse.WindowFunctionExpr, *clickhouse.SubQuery:
			return semanticWalkPrune
		case *clickhouse.FunctionExpr:
			if expr.Name != nil && isAggregateCallName(strings.ToLower(expr.Name.Name)) {
				inner = clickhouse.Format(expr)
				return semanticWalkStop
			}
		}
		return semanticWalkContinue
	})
	if err != nil {
		return "", false, fmt.Errorf("cannot validate nested aggregates: %w", err)
	}
	return inner, inner != "", nil
}

type semanticWalkAction uint8

const (
	semanticWalkContinue semanticWalkAction = iota
	semanticWalkPrune
	semanticWalkStop
)

var clickhouseExprType = reflect.TypeFor[clickhouse.Expr]()

// walkSemanticExpressions walks all expression fields in an AST node. It
// uses the node structure, so a new expression container does not need a
// new type switch case. A prune action stops only the current subtree.
func walkSemanticExpressions(root clickhouse.Expr, visit func(clickhouse.Expr) semanticWalkAction) error {
	seen := make(map[uintptr]bool)
	_, err := walkSemanticExpression(root, visit, seen)
	return err
}

func walkSemanticExpression(expr clickhouse.Expr, visit func(clickhouse.Expr) semanticWalkAction, seen map[uintptr]bool) (bool, error) {
	if expr == nil {
		return false, nil
	}
	value := reflect.ValueOf(expr)
	if value.Kind() == reflect.Pointer && value.IsNil() {
		return false, nil
	}
	action := visit(expr)
	if action == semanticWalkStop {
		return true, nil
	}
	if action == semanticWalkPrune {
		return false, nil
	}
	if action != semanticWalkContinue {
		return false, fmt.Errorf("visitor returned unknown action %d for %T", action, expr)
	}
	if value.Kind() != reflect.Pointer || value.Elem().Kind() != reflect.Struct {
		return false, fmt.Errorf("expression %T does not use a pointer to a struct", expr)
	}
	pointer := value.Pointer()
	if seen[pointer] {
		return false, nil
	}
	seen[pointer] = true
	value = value.Elem()
	for index := range value.NumField() {
		stopped, err := walkSemanticValue(value.Field(index), visit, seen)
		if err != nil || stopped {
			return stopped, err
		}
	}
	return false, nil
}

func walkSemanticValue(value reflect.Value, visit func(clickhouse.Expr) semanticWalkAction, seen map[uintptr]bool) (bool, error) {
	if !value.IsValid() {
		return false, nil
	}
	if !value.CanInterface() {
		if typeCanHoldClickHouseExpr(value.Type(), make(map[reflect.Type]bool)) {
			return false, fmt.Errorf("field of type %s can hide an expression", value.Type())
		}
		return false, nil
	}
	if expr, ok := value.Interface().(clickhouse.Expr); ok {
		return walkSemanticExpression(expr, visit, seen)
	}
	switch value.Kind() {
	case reflect.Interface, reflect.Pointer:
		if value.IsNil() {
			return false, nil
		}
		return walkSemanticValue(value.Elem(), visit, seen)
	case reflect.Struct:
		for index := range value.NumField() {
			stopped, err := walkSemanticValue(value.Field(index), visit, seen)
			if err != nil || stopped {
				return stopped, err
			}
		}
	case reflect.Array, reflect.Slice:
		for index := range value.Len() {
			stopped, err := walkSemanticValue(value.Index(index), visit, seen)
			if err != nil || stopped {
				return stopped, err
			}
		}
	case reflect.Map:
		iterator := value.MapRange()
		for iterator.Next() {
			for _, item := range []reflect.Value{iterator.Key(), iterator.Value()} {
				stopped, err := walkSemanticValue(item, visit, seen)
				if err != nil || stopped {
					return stopped, err
				}
			}
		}
	default:
		if typeCanHoldClickHouseExpr(value.Type(), make(map[reflect.Type]bool)) {
			return false, fmt.Errorf("value of type %s can hide an expression", value.Type())
		}
	}
	return false, nil
}

func typeCanHoldClickHouseExpr(valueType reflect.Type, seen map[reflect.Type]bool) bool {
	if valueType.Implements(clickhouseExprType) {
		return true
	}
	if seen[valueType] {
		return false
	}
	seen[valueType] = true
	switch valueType.Kind() {
	case reflect.Interface:
		return clickhouseExprType.AssignableTo(valueType) || valueType.AssignableTo(clickhouseExprType)
	case reflect.Pointer, reflect.Array, reflect.Slice, reflect.Chan:
		return typeCanHoldClickHouseExpr(valueType.Elem(), seen)
	case reflect.Map:
		return typeCanHoldClickHouseExpr(valueType.Key(), seen) ||
			typeCanHoldClickHouseExpr(valueType.Elem(), seen)
	case reflect.Struct:
		for index := range valueType.NumField() {
			if typeCanHoldClickHouseExpr(valueType.Field(index).Type, seen) {
				return true
			}
		}
	}
	return false
}
