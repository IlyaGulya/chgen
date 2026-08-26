package engine

import (
	"fmt"
	"strconv"
	"strings"

	clickhouse "github.com/AfterShip/clickhouse-sql-parser/parser"
)

const maximumWindowFrameOffset = uint64(2147483646)

type windowFrameBound struct {
	position  int
	offset    uint64
	direction string
}

func validateWindowFunction(function *clickhouse.WindowFunctionExpr, scope queryScope) error {
	window, err := materializeWindowOver(function.OverExpr, scope, make(map[string]bool))
	if err != nil {
		return err
	}
	if err := validateMaterializedWindow(window, scope); err != nil {
		return err
	}
	name := strings.ToLower(function.Function.Name.Name)
	switch name {
	case "lag", "lead", "percentrank", "percent_rank":
		if window.Frame != nil {
			return fmt.Errorf("window function %s does not allow an explicit frame; %s", function.Function.Name.Name, pinTypeHint)
		}
	case "ntile":
		if window.OrderBy == nil || len(window.OrderBy.Items) == 0 {
			return fmt.Errorf("window function %s needs ORDER BY; %s", function.Function.Name.Name, pinTypeHint)
		}
		if window.Frame != nil && !isFullPartitionFrame(window.Frame) {
			return fmt.Errorf("window function %s needs its default frame or a full partition frame; %s", function.Function.Name.Name, pinTypeHint)
		}
	}
	return nil
}

func materializeWindowOver(expression clickhouse.Expr, scope queryScope, visiting map[string]bool) (*clickhouse.WindowExpr, error) {
	switch value := expression.(type) {
	case *clickhouse.Ident:
		return materializeNamedWindow(value.Name, scope, visiting)
	case *clickhouse.WindowExpr:
		return materializeWindowExpr(value, scope, visiting)
	default:
		return nil, fmt.Errorf("unsupported OVER specification %T; %s", expression, pinTypeHint)
	}
}

func materializeNamedWindow(name string, scope queryScope, visiting map[string]bool) (*clickhouse.WindowExpr, error) {
	window, found := scope.windows[name]
	if !found {
		return nil, fmt.Errorf("window %s is not defined; %s", name, pinTypeHint)
	}
	if visiting[name] {
		return nil, fmt.Errorf("window %s has a recursive definition; %s", name, pinTypeHint)
	}
	visiting[name] = true
	defer delete(visiting, name)
	return materializeWindowExpr(window, scope, visiting)
}

func materializeWindowExpr(window *clickhouse.WindowExpr, scope queryScope, visiting map[string]bool) (*clickhouse.WindowExpr, error) {
	if window == nil {
		return nil, fmt.Errorf("window specification is empty; %s", pinTypeHint)
	}
	result := &clickhouse.WindowExpr{
		LeftParenPos: window.LeftParenPos, RightParenPos: window.RightParenPos,
		PartitionBy: window.PartitionBy, OrderBy: window.OrderBy, Frame: window.Frame,
	}
	if window.WindowName == nil {
		return result, nil
	}
	base, err := materializeNamedWindow(window.WindowName.Name, scope, visiting)
	if err != nil {
		return nil, err
	}
	if result.PartitionBy != nil {
		return nil, fmt.Errorf("window %s cannot add or replace a PARTITION BY clause; %s", window.WindowName.Name, pinTypeHint)
	}
	if result.OrderBy != nil && base.OrderBy != nil {
		return nil, fmt.Errorf("window %s replaces an existing ORDER BY clause; %s", window.WindowName.Name, pinTypeHint)
	}
	if base.Frame != nil {
		return nil, fmt.Errorf("window %s cannot inherit a parent frame; %s", window.WindowName.Name, pinTypeHint)
	}
	if result.PartitionBy == nil {
		result.PartitionBy = base.PartitionBy
	}
	if result.OrderBy == nil {
		result.OrderBy = base.OrderBy
	}
	if result.Frame == nil {
		result.Frame = base.Frame
	}
	return result, nil
}

func validateMaterializedWindow(window *clickhouse.WindowExpr, scope queryScope) error {
	if window.PartitionBy != nil {
		expressions := []clickhouse.Expr{window.PartitionBy.Expr}
		if list, ok := window.PartitionBy.Expr.(*clickhouse.ColumnExprList); ok {
			expressions = list.Items
		}
		for _, expression := range expressions {
			if _, err := inferExprType(expression, scope); err != nil {
				return fmt.Errorf("window PARTITION BY: %w", err)
			}
		}
	}
	orderTypes := make([]CHType, 0)
	if window.OrderBy != nil {
		for _, item := range window.OrderBy.Items {
			expression := item
			if order, ok := item.(*clickhouse.OrderExpr); ok {
				expression = order.Expr
			}
			inferred, err := inferExprType(expression, scope)
			if err != nil {
				return fmt.Errorf("window ORDER BY: %w", err)
			}
			orderTypes = append(orderTypes, inferred)
		}
	}
	if window.Frame == nil {
		return nil
	}
	hasOffset, err := validateWindowFrame(window.Frame)
	if err != nil {
		return err
	}
	if !strings.EqualFold(window.Frame.Type, "RANGE") || !hasOffset {
		return nil
	}
	if len(orderTypes) != 1 {
		return fmt.Errorf("a RANGE offset frame needs exactly one ORDER BY expression; %s", pinTypeHint)
	}
	if orderTypes[0].normalizedName() == "lowcardinality" {
		return fmt.Errorf("a RANGE offset frame does not support LowCardinality ORDER BY; %s", pinTypeHint)
	}
	base := domainBaseType(orderTypes[0])
	if !rangeOffsetOrderType(base) {
		return fmt.Errorf("a RANGE offset frame does not support ORDER BY type %s; %s", base.String(), pinTypeHint)
	}
	return nil
}

func validateWindowFrame(frame *clickhouse.WindowFrameClause) (bool, error) {
	if !strings.EqualFold(frame.Type, "ROWS") && !strings.EqualFold(frame.Type, "RANGE") {
		return false, fmt.Errorf("window frame type %s is not supported; %s", frame.Type, pinTypeHint)
	}
	startExpression := frame.Extend
	endExpression := clickhouse.Expr(&clickhouse.WindowFrameCurrentRow{})
	if between, ok := frame.Extend.(*clickhouse.BetweenClause); ok {
		startExpression = between.Between
		endExpression = between.And
	}
	start, startOffset, err := parseWindowFrameBound(startExpression)
	if err != nil {
		return false, fmt.Errorf("window frame start: %w", err)
	}
	end, endOffset, err := parseWindowFrameBound(endExpression)
	if err != nil {
		return false, fmt.Errorf("window frame end: %w", err)
	}
	if start.direction == "unbounded_following" {
		return false, fmt.Errorf("window frame start cannot be UNBOUNDED FOLLOWING; %s", pinTypeHint)
	}
	if end.direction == "unbounded_preceding" {
		return false, fmt.Errorf("window frame end cannot be UNBOUNDED PRECEDING; %s", pinTypeHint)
	}
	if compareWindowBounds(start, end) > 0 {
		return false, fmt.Errorf("window frame start must not follow its end; %s", pinTypeHint)
	}
	return startOffset || endOffset, nil
}

func parseWindowFrameBound(expression clickhouse.Expr) (windowFrameBound, bool, error) {
	switch bound := expression.(type) {
	case *clickhouse.WindowFrameCurrentRow:
		return windowFrameBound{position: 0, direction: "current"}, false, nil
	case *clickhouse.WindowFrameUnbounded:
		switch strings.ToLower(bound.Direction) {
		case "preceding":
			return windowFrameBound{position: -2, direction: "unbounded_preceding"}, false, nil
		case "following":
			return windowFrameBound{position: 2, direction: "unbounded_following"}, false, nil
		default:
			return windowFrameBound{}, false, fmt.Errorf("UNBOUNDED has direction %q", bound.Direction)
		}
	case *clickhouse.WindowFrameNumber:
		value, err := strconv.ParseUint(bound.Number.Literal, 10, 64)
		if err != nil || value > maximumWindowFrameOffset {
			return windowFrameBound{}, false, fmt.Errorf("offset %q is not in the measured range 0..%d", bound.Number.Literal, maximumWindowFrameOffset)
		}
		switch strings.ToLower(bound.Direction) {
		case "preceding":
			return windowFrameBound{position: -1, offset: value, direction: "preceding"}, true, nil
		case "following":
			return windowFrameBound{position: 1, offset: value, direction: "following"}, true, nil
		default:
			return windowFrameBound{}, false, fmt.Errorf("offset has direction %q", bound.Direction)
		}
	case *clickhouse.WindowFrameExtendExpr:
		return windowFrameBound{}, false, fmt.Errorf("interval offsets are not supported by ClickHouse %s", MeasuredCHVersion)
	case *clickhouse.WindowFrameParam:
		return windowFrameBound{}, false, fmt.Errorf("parameter offsets have no measured generated binding")
	default:
		return windowFrameBound{}, false, fmt.Errorf("unsupported endpoint %T", expression)
	}
}

func compareWindowBounds(left, right windowFrameBound) int {
	if left.position != right.position {
		if left.position < right.position {
			return -1
		}
		return 1
	}
	switch left.direction {
	case "preceding":
		if left.offset > right.offset {
			return -1
		}
		if left.offset < right.offset {
			return 1
		}
	case "following":
		if left.offset < right.offset {
			return -1
		}
		if left.offset > right.offset {
			return 1
		}
	}
	return 0
}

func rangeOffsetOrderType(value CHType) bool {
	switch value.normalizedName() {
	case "bool", "int8", "int16", "int32", "int64", "int128", "int256",
		"uint8", "uint16", "uint32", "uint64", "uint128", "uint256",
		"float32", "float64", "enum", "enum8", "enum16", "date", "date32", "datetime":
		return true
	default:
		return false
	}
}

func isFullPartitionFrame(frame *clickhouse.WindowFrameClause) bool {
	if frame == nil || (!strings.EqualFold(frame.Type, "ROWS") && !strings.EqualFold(frame.Type, "RANGE")) {
		return false
	}
	between, ok := frame.Extend.(*clickhouse.BetweenClause)
	if !ok {
		return false
	}
	start, ok := between.Between.(*clickhouse.WindowFrameUnbounded)
	if !ok || !strings.EqualFold(start.Direction, "PRECEDING") {
		return false
	}
	end, ok := between.And.(*clickhouse.WindowFrameUnbounded)
	return ok && strings.EqualFold(end.Direction, "FOLLOWING")
}

func validateNamedWindowDefinitions(scope queryScope) error {
	for name := range scope.windows {
		window, err := materializeNamedWindow(name, scope, make(map[string]bool))
		if err != nil {
			return err
		}
		if err := validateMaterializedWindow(window, scope); err != nil {
			return fmt.Errorf("window %s: %w", name, err)
		}
	}
	return nil
}
