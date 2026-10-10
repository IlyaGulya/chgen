package engine

import (
	"fmt"
	"reflect"
	"slices"
	"strings"

	clickhouse "github.com/AfterShip/clickhouse-sql-parser/parser"
	"github.com/IlyaGulya/chgen/internal/sqlir"
)

// The adapter refuses an entire query when any non-position property is not
// accounted for. A partially erased tree must never compare as equivalent.
// Checking nonzero fields also makes an upstream AST extension fail closed.
func irFields(node any, handled ...string) error {
	value := reflect.ValueOf(node).Elem()
	for i := range value.NumField() {
		field := value.Type().Field(i)
		property := value.Field(i)
		emptyCollection := (property.Kind() == reflect.Slice || property.Kind() == reflect.Map) && property.Len() == 0
		if field.Type == reflect.TypeFor[clickhouse.Pos]() || slices.Contains(handled, field.Name) || property.IsZero() || emptyCollection {
			continue
		}
		return fmt.Errorf("IR adapter does not model SQL property %s", field.Name)
	}
	return nil
}

func lowerSQLIR(query *clickhouse.SelectQuery, sql string) (*sqlir.Document, error) {
	document, err := lowerSelectIR(query)
	if err != nil {
		return nil, err
	}
	markIRLimitModifiers(document, irLimitModifiers(query, sql))
	return document, nil
}

// Parser normalization blanks WITH TIES without changing byte positions.
// Associate each removed modifier with the nearest preceding AST LIMIT, not
// the outer SELECT: CTEs and derived tables own independent limits.
func irLimitModifiers(query *clickhouse.SelectQuery, sql string) map[int]bool {
	limits := make(map[int]int)
	clickhouse.Walk(query, func(node clickhouse.Expr) bool {
		if selectQuery, ok := node.(*clickhouse.SelectQuery); ok && selectQuery.Limit != nil {
			limits[int(selectQuery.Limit.Pos())] = int(selectQuery.Limit.Limit.Pos())
		}
		return true
	})
	markers := make(map[int]bool)
	normalized := stripLimitWithTiesModifiers(sql)
	for index := range len(sql) {
		if sql[index] == normalized[index] || !sqlKeywordAt(sql, index, "WITH") {
			continue
		}
		owner := -1
		for position := range limits {
			if position < index && position > owner {
				owner = position
			}
		}
		if owner >= 0 {
			markers[limits[owner]] = true
		}
	}
	return markers
}

func markIRLimitModifiers(document *sqlir.Document, markers map[int]bool) {
	sqlir.WalkSelect(&document.Select, func(query *sqlir.Select) {
		if query.Limit != nil && query.Limit.Span != nil {
			query.LimitWithTies = markers[query.Limit.Span.Start]
		}
	})
}

func lowerSelectIR(query *clickhouse.SelectQuery) (*sqlir.Document, error) {
	if err := irFields(query, "With", "SelectItems", "From", "Where", "GroupBy", "Having", "OrderBy", "Limit", "Window", "InnerQuery", "UnionAll", "UnionDistinct", "Except", "Intersect", "HasDistinct", "DistinctOn", "Prewhere", "Top", "LimitBy", "Settings", "Format"); err != nil {
		return nil, err
	}
	result := sqlir.Select{}
	result.Distinct = query.HasDistinct
	if query.DistinctOn != nil {
		if err := irFields(query.DistinctOn, "Idents"); err != nil {
			return nil, err
		}
		for _, identifier := range query.DistinctOn.Idents {
			item, err := lowerExprIR(identifier)
			if err != nil {
				return nil, err
			}
			result.DistinctOn = append(result.DistinctOn, item)
		}
	}
	if query.Prewhere != nil {
		if err := irFields(query.Prewhere, "Expr"); err != nil {
			return nil, err
		}
		expression, err := lowerExprIR(query.Prewhere.Expr)
		if err != nil {
			return nil, err
		}
		result.Prewhere = &expression
	}
	if query.Top != nil {
		if err := irFields(query.Top, "Number", "WithTies"); err != nil {
			return nil, err
		}
		count, err := lowerExprIR(query.Top.Number)
		if err != nil {
			return nil, err
		}
		result.Top = &sqlir.Top{Count: count, WithTies: query.Top.WithTies}
	}
	if query.LimitBy != nil {
		if err := irFields(query.LimitBy, "Limit", "ByExpr"); err != nil {
			return nil, err
		}
		if err := irFields(query.LimitBy.Limit, "Limit", "Offset"); err != nil {
			return nil, err
		}
		count, err := lowerExprIR(query.LimitBy.Limit.Limit)
		if err != nil {
			return nil, err
		}
		keys, err := lowerExprListIR(query.LimitBy.ByExpr)
		if err != nil {
			return nil, err
		}
		result.LimitBy = &sqlir.LimitBy{Count: count, Keys: keys}
		if query.LimitBy.Limit.Offset != nil {
			offset, err := lowerExprIR(query.LimitBy.Limit.Offset)
			if err != nil {
				return nil, err
			}
			result.LimitBy.Offset = &offset
		}
	}
	if query.Settings != nil {
		if err := irFields(query.Settings, "Items"); err != nil {
			return nil, err
		}
		for _, setting := range query.Settings.Items {
			if err := irFields(setting, "Name", "Expr"); err != nil {
				return nil, err
			}
			value, err := lowerExprIR(setting.Expr)
			if err != nil {
				return nil, err
			}
			result.Settings = append(result.Settings, sqlir.Setting{Name: setting.Name.Name, Value: value})
		}
	}
	if query.Format != nil {
		if err := irFields(query.Format, "Format"); err != nil {
			return nil, err
		}
		result.Format = query.Format.Format.Name
	}
	if query.InnerQuery != nil {
		inner, err := lowerSelectIR(query.InnerQuery)
		if err != nil {
			return nil, err
		}
		result.Inner = &inner.Select
	}
	for _, operation := range []struct {
		kind  string
		query *clickhouse.SelectQuery
	}{
		{"UNION ALL", query.UnionAll}, {"UNION DISTINCT", query.UnionDistinct},
		{"EXCEPT", query.Except}, {"INTERSECT", query.Intersect},
	} {
		if operation.query == nil {
			continue
		}
		branch, err := lowerSelectIR(operation.query)
		if err != nil {
			return nil, err
		}
		result.SetOperations = append(result.SetOperations, sqlir.SetOperation{Kind: operation.kind, Query: branch.Select})
	}
	if query.Window != nil {
		if err := irFields(query.Window, "Windows"); err != nil {
			return nil, err
		}
		for _, definition := range query.Window.Windows {
			if err := irFields(definition, "Name", "Expr"); err != nil {
				return nil, err
			}
			spec, err := lowerWindowIR(definition.Expr)
			if err != nil {
				return nil, err
			}
			result.Windows = append(result.Windows, sqlir.WindowDefinition{Name: definition.Name.Name, Spec: *spec})
		}
	}
	if query.With != nil {
		if err := irFields(query.With, "CTEs"); err != nil {
			return nil, err
		}
		for _, cte := range query.With.CTEs {
			if err := irFields(cte, "Expr", "Alias"); err != nil {
				return nil, err
			}
			body, ok := cte.Alias.(*clickhouse.SelectQuery)
			if !ok {
				name, err := relationName(cte.Alias)
				if err != nil {
					return nil, err
				}
				expression, err := lowerExprIR(cte.Expr)
				if err != nil {
					return nil, err
				}
				result.With = append(result.With, sqlir.CTE{Name: name, Expr: &expression})
				continue
			}
			name, err := relationName(cte.Expr)
			if err != nil {
				return nil, err
			}
			nested, err := lowerSelectIR(body)
			if err != nil {
				return nil, err
			}
			result.With = append(result.With, sqlir.CTE{Name: name, Query: nested.Select})
		}
	}
	for _, item := range query.SelectItems {
		if err := irFields(item, "Expr", "Alias"); err != nil {
			return nil, err
		}
		expression, err := lowerExprIR(item.Expr)
		if err != nil {
			return nil, err
		}
		projection := sqlir.Item{Expr: expression}
		if item.Alias != nil {
			projection.Alias = item.Alias.Name
		}
		result.Items = append(result.Items, projection)
	}
	if query.From != nil {
		if err := irFields(query.From, "Expr"); err != nil {
			return nil, err
		}
		relation, err := lowerRelationIR(query.From.Expr)
		if err != nil {
			return nil, err
		}
		result.From = []sqlir.Relation{relation}
	}
	if query.Where != nil {
		if err := irFields(query.Where, "Expr"); err != nil {
			return nil, err
		}
		expression, err := lowerExprIR(query.Where.Expr)
		if err != nil {
			return nil, err
		}
		result.Where = &expression
	}
	if query.GroupBy != nil {
		if err := irFields(query.GroupBy, "Expr"); err != nil {
			return nil, err
		}
		list, ok := query.GroupBy.Expr.(*clickhouse.ColumnExprList)
		if !ok {
			return nil, fmt.Errorf("IR adapter requires ordinary GROUP BY expressions")
		}
		var err error
		result.GroupBy, err = lowerExprListIR(list)
		if err != nil {
			return nil, err
		}
	}
	if query.Having != nil {
		if err := irFields(query.Having, "Expr"); err != nil {
			return nil, err
		}
		expression, err := lowerExprIR(query.Having.Expr)
		if err != nil {
			return nil, err
		}
		result.Having = &expression
	}
	if query.OrderBy != nil {
		if err := irFields(query.OrderBy, "Items"); err != nil {
			return nil, err
		}
		for _, item := range query.OrderBy.Items {
			order, ok := item.(*clickhouse.OrderExpr)
			if !ok {
				return nil, fmt.Errorf("IR adapter requires ordinary ORDER BY expressions")
			}
			if err := irFields(order, "Expr", "Direction", "Fill"); err != nil {
				return nil, err
			}
			expression, err := lowerExprIR(order.Expr)
			if err != nil {
				return nil, err
			}
			direction := strings.ToUpper(string(order.Direction))
			if direction == "" {
				direction = "ASC"
			}
			lowered := sqlir.Order{Expr: expression, Direction: direction}
			if order.Fill != nil {
				if err := irFields(order.Fill, "From", "To", "Step", "Staleness"); err != nil {
					return nil, err
				}
				lowered.Fill = &sqlir.Fill{}
				for _, field := range []struct {
					source clickhouse.Expr
					target **sqlir.Expr
				}{
					{order.Fill.From, &lowered.Fill.From}, {order.Fill.To, &lowered.Fill.To},
					{order.Fill.Step, &lowered.Fill.Step}, {order.Fill.Staleness, &lowered.Fill.Staleness},
				} {
					if field.source == nil {
						continue
					}
					value, err := lowerExprIR(field.source)
					if err != nil {
						return nil, err
					}
					*field.target = &value
				}
			}
			result.OrderBy = append(result.OrderBy, lowered)
		}
	}
	if query.Limit != nil {
		if err := irFields(query.Limit, "Limit", "Offset"); err != nil {
			return nil, err
		}
		limit, err := lowerExprIR(query.Limit.Limit)
		if err != nil {
			return nil, err
		}
		result.Limit = &limit
		if query.Limit.Offset != nil {
			offset, err := lowerExprIR(query.Limit.Offset)
			if err != nil {
				return nil, err
			}
			result.Offset = &offset
		}
	}
	return &sqlir.Document{Version: 1, Select: result}, nil
}

func lowerExprListIR(list *clickhouse.ColumnExprList) ([]sqlir.Expr, error) {
	if list == nil {
		return nil, nil
	}
	if err := irFields(list, "Items"); err != nil {
		return nil, err
	}
	var values []sqlir.Expr
	for _, item := range list.Items {
		value, err := lowerExprIR(item)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, nil
}

func lowerExprIR(expression clickhouse.Expr) (result sqlir.Expr, err error) {
	defer func() {
		if err == nil && result.Span == nil {
			result.Span = irSourceSpan(expression)
		}
	}()
	switch expr := expression.(type) {
	case *clickhouse.IntervalExpr:
		if err := irFields(expr, "Expr", "Unit"); err != nil {
			return sqlir.Expr{}, err
		}
		if err := irFields(expr.Unit, "Name", "QuoteType"); err != nil {
			return sqlir.Expr{}, err
		}
		args, err := lowerExprArgsIR(expr.Expr)
		return sqlir.Expr{Kind: "interval", Value: strings.ToUpper(expr.Unit.Name), Args: args}, err
	case *clickhouse.CaseExpr:
		if err := irFields(expr, "Expr", "Whens", "Else"); err != nil {
			return sqlir.Expr{}, err
		}
		result := sqlir.Expr{Kind: "case", Value: "searched"}
		if expr.Expr != nil {
			base, err := lowerExprIR(expr.Expr)
			if err != nil {
				return result, err
			}
			result.Value = "simple"
			result.Args = append(result.Args, base)
		}
		for _, arm := range expr.Whens {
			if err := irFields(arm, "When", "Then", "Else"); err != nil {
				return result, err
			}
			operands := []clickhouse.Expr{arm.When, arm.Then}
			if arm.Else != nil {
				operands = append(operands, arm.Else)
			}
			args, err := lowerExprArgsIR(operands...)
			if err != nil {
				return result, err
			}
			result.Args = append(result.Args, sqlir.Expr{Kind: "when", Span: irSourceSpan(arm), Args: args})
		}
		if expr.Else != nil {
			otherwise, err := lowerExprIR(expr.Else)
			if err != nil {
				return result, err
			}
			result.Args = append(result.Args, sqlir.Expr{Kind: "else", Args: []sqlir.Expr{otherwise}})
		}
		return result, nil
	case *clickhouse.CastExpr:
		if err := irFields(expr, "Expr", "Separator", "AsType"); err != nil {
			return sqlir.Expr{}, err
		}
		args, err := lowerExprArgsIR(expr.Expr)
		return sqlir.Expr{Kind: "cast", Value: expr.Separator, Type: clickhouse.Format(expr.AsType), Args: args}, err
	case *clickhouse.IsNullExpr:
		if err := irFields(expr, "Expr"); err != nil {
			return sqlir.Expr{}, err
		}
		args, err := lowerExprArgsIR(expr.Expr)
		return sqlir.Expr{Kind: "null_check", Value: "IS NULL", Args: args}, err
	case *clickhouse.IsNotNullExpr:
		if err := irFields(expr, "Expr"); err != nil {
			return sqlir.Expr{}, err
		}
		args, err := lowerExprArgsIR(expr.Expr)
		return sqlir.Expr{Kind: "null_check", Value: "IS NOT NULL", Args: args}, err
	case *clickhouse.ObjectParams:
		if err := irFields(expr, "Object", "Params"); err != nil {
			return sqlir.Expr{}, err
		}
		args, err := lowerExprArgsIR(expr.Object, expr.Params)
		return sqlir.Expr{Kind: "subscript", Args: args}, err
	case *clickhouse.IndexOperation:
		if err := irFields(expr, "Object", "Operation", "Index"); err != nil {
			return sqlir.Expr{}, err
		}
		args, err := lowerExprArgsIR(expr.Object, expr.Index)
		return sqlir.Expr{Kind: "index", Value: string(expr.Operation), Args: args}, err
	case *clickhouse.NullLiteral:
		if err := irFields(expr); err != nil {
			return sqlir.Expr{}, err
		}
		return sqlir.Expr{Kind: "null"}, nil
	case *clickhouse.BoolLiteral:
		if err := irFields(expr, "Literal"); err != nil {
			return sqlir.Expr{}, err
		}
		return sqlir.Expr{Kind: "bool", Value: expr.Literal}, nil
	case *clickhouse.PlaceHolder:
		if err := irFields(expr, "Type"); err != nil {
			return sqlir.Expr{}, err
		}
		return sqlir.Expr{Kind: "placeholder", Value: expr.Type}, nil
	case *clickhouse.ArrayParamList:
		if err := irFields(expr, "Items"); err != nil {
			return sqlir.Expr{}, err
		}
		items, err := lowerExprListIR(expr.Items)
		return sqlir.Expr{Kind: "array", Args: items}, err
	case *clickhouse.UnaryExpr:
		if err := irFields(expr, "Kind", "Expr"); err != nil {
			return sqlir.Expr{}, err
		}
		operand, err := lowerExprIR(expr.Expr)
		return sqlir.Expr{Kind: "unary", Value: strings.ToUpper(string(expr.Kind)), Args: []sqlir.Expr{operand}}, err
	case *clickhouse.BetweenClause:
		if err := irFields(expr, "Expr", "Not", "Between", "And"); err != nil {
			return sqlir.Expr{}, err
		}
		operands, err := lowerExprArgsIR(expr.Expr, expr.Between, expr.And)
		operation := "BETWEEN"
		if expr.Not {
			operation = "NOT BETWEEN"
		}
		return sqlir.Expr{Kind: "between", Value: operation, Args: operands}, err
	case *clickhouse.StringLiteral:
		if err := irFields(expr, "Literal"); err != nil {
			return sqlir.Expr{}, err
		}
		return sqlir.Expr{Kind: "string", Value: expr.Literal}, nil
	case *clickhouse.ParamExprList:
		if err := irFields(expr, "Items"); err != nil {
			return sqlir.Expr{}, err
		}
		args, err := lowerExprListIR(expr.Items)
		if err != nil {
			return sqlir.Expr{}, err
		}
		if len(args) == 0 {
			return sqlir.Expr{}, fmt.Errorf("IR adapter does not model empty parentheses")
		}
		if len(args) == 1 {
			return args[0], nil
		}
		return sqlir.Expr{Kind: "tuple", Args: args}, nil
	case *clickhouse.FunctionExpr:
		if err := irFields(expr, "Name", "Params"); err != nil {
			return sqlir.Expr{}, err
		}
		if err := irFields(expr.Params, "Items", "ColumnArgList"); err != nil {
			return sqlir.Expr{}, err
		}
		call := sqlir.Expr{Kind: "call", Value: expr.Name.Name}
		if list := expr.Params.Items; list != nil {
			if err := irFields(list, "Items", "HasDistinct"); err != nil {
				return sqlir.Expr{}, err
			}
			call.Args, err = lowerExprArgsIR(list.Items...)
			if err != nil {
				return sqlir.Expr{}, err
			}
			call.Distinct = list.HasDistinct
		}
		if list := expr.Params.ColumnArgList; list != nil {
			if err := irFields(list, "Items", "Distinct"); err != nil {
				return sqlir.Expr{}, err
			}
			call.Parameters = call.Args
			call.Args, err = lowerExprArgsIR(list.Items...)
			call.Distinct = list.Distinct
		}
		return call, err
	case *clickhouse.WindowFunctionExpr:
		if err := irFields(expr, "Function", "OverExpr"); err != nil {
			return sqlir.Expr{}, err
		}
		function, err := lowerExprIR(expr.Function)
		if err != nil {
			return sqlir.Expr{}, err
		}
		window, err := lowerWindowIR(expr.OverExpr)
		return sqlir.Expr{Kind: "window", Args: []sqlir.Expr{function}, Window: window}, err
	case *clickhouse.SubQuery:
		if err := irFields(expr, "Select", "HasParen"); err != nil {
			return sqlir.Expr{}, err
		}
		document, err := lowerSelectIR(expr.Select)
		if err != nil {
			return sqlir.Expr{}, err
		}
		return sqlir.Expr{Kind: "subquery", Query: &document.Select, Parenthesized: expr.HasParen}, nil
	case *clickhouse.SelectQuery:
		// EXISTS stores its body directly as SelectQuery, unlike a scalar
		// subquery's SubQuery wrapper. Preserve that grammar distinction.
		document, err := lowerSelectIR(expr)
		if err != nil {
			return sqlir.Expr{}, err
		}
		return sqlir.Expr{Kind: "subquery", Query: &document.Select}, nil
	case *clickhouse.ColumnExpr:
		if err := irFields(expr, "Expr"); err != nil {
			return sqlir.Expr{}, err
		}
		return lowerExprIR(expr.Expr)
	case *clickhouse.Ident:
		if err := irFields(expr, "Name", "QuoteType"); err != nil {
			return sqlir.Expr{}, err
		}
		// The pinned parser constructs wildcard identifiers with QuoteType 0.
		// A quoted `*` is still an identifier, not a wildcard.
		if expr.Name == "*" && (expr.QuoteType == 0 || expr.QuoteType == clickhouse.Unquoted) {
			return sqlir.Expr{Kind: "wildcard"}, nil
		}
		if expr.QuoteType == clickhouse.Unquoted && strings.EqualFold(expr.Name, "NULL") {
			return sqlir.Expr{Kind: "null"}, nil
		}
		return sqlir.Expr{Kind: "identifier", Name: []string{expr.Name}, Quoted: expr.QuoteType != 0 && expr.QuoteType != clickhouse.Unquoted}, nil
	case *clickhouse.NestedIdentifier:
		if err := irFields(expr, "Ident", "DotIdent"); err != nil {
			return sqlir.Expr{}, err
		}
		name := []string{expr.Ident.Name}
		if expr.DotIdent != nil {
			if _, star := selectWildcardQualifier(expr.DotIdent); star {
				return sqlir.Expr{Kind: "wildcard", Name: name}, nil
			}
			name = append(name, expr.DotIdent.Name)
		}
		return sqlir.Expr{Kind: "identifier", Name: name}, nil
	case *clickhouse.Path:
		if err := irFields(expr, "Fields"); err != nil {
			return sqlir.Expr{}, err
		}
		result := sqlir.Expr{Kind: "identifier"}
		for _, field := range expr.Fields {
			result.Name = append(result.Name, field.Name)
		}
		return result, nil
	case *clickhouse.NumberLiteral:
		if err := irFields(expr, "Literal", "Base"); err != nil {
			return sqlir.Expr{}, err
		}
		return sqlir.Expr{Kind: "number", Value: expr.Literal, Base: expr.Base}, nil
	case *clickhouse.BinaryOperation:
		if err := irFields(expr, "LeftExpr", "Operation", "RightExpr"); err != nil {
			return sqlir.Expr{}, err
		}
		if string(expr.Operation) == "->" {
			parameters, body, ok := lambdaParts(expr)
			if !ok {
				return sqlir.Expr{}, fmt.Errorf("IR adapter requires simple lambda parameter names")
			}
			if _, err := lowerExprIR(expr.LeftExpr); err != nil {
				return sqlir.Expr{}, err
			}
			lowered, err := lowerExprIR(body)
			_, parenthesized := expr.LeftExpr.(*clickhouse.ParamExprList)
			return sqlir.Expr{Kind: "lambda", Name: parameters, Args: []sqlir.Expr{lowered}, Parenthesized: parenthesized}, err
		}
		left, err := lowerExprIR(expr.LeftExpr)
		if err != nil {
			return sqlir.Expr{}, err
		}
		right, err := lowerExprIR(expr.RightExpr)
		if err != nil {
			return sqlir.Expr{}, err
		}
		return sqlir.Expr{Kind: "operator", Value: strings.ToUpper(string(expr.Operation)), Args: []sqlir.Expr{left, right}}, nil
	default:
		return sqlir.Expr{}, fmt.Errorf("IR adapter does not model expression %s", clickhouse.Format(expression))
	}
}

func lowerExprArgsIR(expressions ...clickhouse.Expr) ([]sqlir.Expr, error) {
	result := make([]sqlir.Expr, 0, len(expressions))
	for _, expression := range expressions {
		item, err := lowerExprIR(expression)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, nil
}

func lowerWindowIR(expression clickhouse.Expr) (*sqlir.Window, error) {
	if name, ok := expression.(*clickhouse.Ident); ok {
		if err := irFields(name, "Name", "QuoteType"); err != nil {
			return nil, err
		}
		return &sqlir.Window{Span: irSourceSpan(name), Base: name.Name}, nil
	}
	window, ok := expression.(*clickhouse.WindowExpr)
	if !ok {
		return nil, fmt.Errorf("IR adapter requires an inline window")
	}
	if err := irFields(window, "WindowName", "PartitionBy", "OrderBy", "Frame"); err != nil {
		return nil, err
	}
	result := &sqlir.Window{Span: irSourceSpan(window), Parenthesized: true}
	if window.WindowName != nil {
		result.Base = window.WindowName.Name
	}
	if window.Frame != nil {
		frame, err := lowerWindowFrameIR(window.Frame)
		if err != nil {
			return nil, err
		}
		result.Frame = &frame
	}
	if window.PartitionBy != nil {
		if err := irFields(window.PartitionBy, "Expr"); err != nil {
			return nil, err
		}
		list, ok := window.PartitionBy.Expr.(*clickhouse.ColumnExprList)
		if !ok {
			return nil, fmt.Errorf("IR adapter requires a window partition expression list")
		}
		var err error
		result.PartitionBy, err = lowerExprListIR(list)
		if err != nil {
			return nil, err
		}
	}
	if window.OrderBy != nil {
		if err := irFields(window.OrderBy, "Items"); err != nil {
			return nil, err
		}
		for _, item := range window.OrderBy.Items {
			order, ok := item.(*clickhouse.OrderExpr)
			if !ok {
				return nil, fmt.Errorf("IR adapter requires ordinary window ORDER BY expressions")
			}
			if err := irFields(order, "Expr", "Direction"); err != nil {
				return nil, err
			}
			expr, err := lowerExprIR(order.Expr)
			if err != nil {
				return nil, err
			}
			direction := strings.ToUpper(string(order.Direction))
			if direction == "" {
				direction = "ASC"
			}
			result.OrderBy = append(result.OrderBy, sqlir.Order{Expr: expr, Direction: direction})
		}
	}
	return result, nil
}

func lowerWindowFrameIR(expression clickhouse.Expr) (result sqlir.Expr, err error) {
	defer func() {
		if err == nil {
			result.Span = irSourceSpan(expression)
		}
	}()
	switch frame := expression.(type) {
	case *clickhouse.WindowFrameClause:
		if err := irFields(frame, "Type", "Extend"); err != nil {
			return result, err
		}
		bound, err := lowerWindowFrameIR(frame.Extend)
		return sqlir.Expr{Kind: "window_frame", Value: strings.ToUpper(frame.Type), Args: []sqlir.Expr{bound}}, err
	case *clickhouse.BetweenClause:
		if err := irFields(frame, "Between", "And"); err != nil {
			return result, err
		}
		start, err := lowerWindowFrameIR(frame.Between)
		if err != nil {
			return result, err
		}
		end, err := lowerWindowFrameIR(frame.And)
		return sqlir.Expr{Kind: "frame_between", Args: []sqlir.Expr{start, end}}, err
	case *clickhouse.WindowFrameCurrentRow:
		if err := irFields(frame); err != nil {
			return result, err
		}
		return sqlir.Expr{Kind: "frame_bound", Value: "CURRENT ROW"}, nil
	case *clickhouse.WindowFrameUnbounded:
		if err := irFields(frame, "Direction"); err != nil {
			return result, err
		}
		return sqlir.Expr{Kind: "frame_bound", Value: "UNBOUNDED " + strings.ToUpper(frame.Direction)}, nil
	case *clickhouse.WindowFrameNumber:
		if err := irFields(frame, "Number", "Direction"); err != nil {
			return result, err
		}
		offset, err := lowerExprIR(frame.Number)
		return sqlir.Expr{Kind: "frame_bound", Value: strings.ToUpper(frame.Direction), Args: []sqlir.Expr{offset}}, err
	case *clickhouse.WindowFrameExtendExpr:
		if err := irFields(frame, "Expr", "Direction"); err != nil {
			return result, err
		}
		offset, err := lowerExprIR(frame.Expr)
		return sqlir.Expr{Kind: "frame_bound", Value: strings.ToUpper(frame.Direction), Args: []sqlir.Expr{offset}}, err
	default:
		return result, fmt.Errorf("IR adapter does not model window frame %T", expression)
	}
}

func lowerRelationIR(expression clickhouse.Expr) (result sqlir.Relation, err error) {
	defer func() {
		if err == nil && result.Span == nil {
			result.Span = irSourceSpan(expression)
		}
	}()
	switch expr := expression.(type) {
	case *clickhouse.TableIdentifier:
		if err := irFields(expr, "Database", "Table"); err != nil {
			return sqlir.Relation{}, err
		}
		name := []string{expr.Table.Name}
		if expr.Database != nil {
			name = append([]string{expr.Database.Name}, name...)
		}
		return sqlir.Relation{Kind: "table", Name: name}, nil
	case *clickhouse.AliasExpr:
		if err := irFields(expr, "Expr", "Alias"); err != nil {
			return sqlir.Relation{}, err
		}
		relation, err := lowerRelationIR(expr.Expr)
		if err != nil {
			return sqlir.Relation{}, err
		}
		alias, ok := expr.Alias.(*clickhouse.Ident)
		if !ok {
			return sqlir.Relation{}, fmt.Errorf("IR adapter requires a simple relation alias")
		}
		relation.Alias = alias.Name
		return relation, nil
	case *clickhouse.JoinExpr:
		if isArrayJoin(expr) {
			if err := irFields(expr, "Left", "Right", "Modifiers"); err != nil {
				return sqlir.Relation{}, err
			}
			list, ok := expr.Left.(*clickhouse.ColumnExprList)
			if !ok {
				return sqlir.Relation{}, fmt.Errorf("IR adapter requires ARRAY JOIN expressions")
			}
			if err := irFields(list, "Items"); err != nil {
				return sqlir.Relation{}, err
			}
			relation := sqlir.Relation{Kind: "array_join", Modifiers: slices.Clone(expr.Modifiers)}
			for _, raw := range list.Items {
				column, ok := raw.(*clickhouse.ColumnExpr)
				if !ok {
					return sqlir.Relation{}, fmt.Errorf("IR adapter requires ARRAY JOIN columns")
				}
				if err := irFields(column, "Expr", "Alias"); err != nil {
					return sqlir.Relation{}, err
				}
				expression, err := lowerExprIR(column.Expr)
				if err != nil {
					return sqlir.Relation{}, err
				}
				item := sqlir.Item{Expr: expression}
				if column.Alias != nil {
					item.Alias = column.Alias.Name
				}
				relation.Items = append(relation.Items, item)
			}
			if expr.Right != nil {
				right, err := lowerRelationIR(expr.Right)
				if err != nil {
					return sqlir.Relation{}, err
				}
				relation.Right = &right
			}
			return relation, nil
		}
		if expr.Right == nil && len(expr.Modifiers) == 0 && expr.Constraints == nil {
			if err := irFields(expr, "Left"); err != nil {
				return sqlir.Relation{}, err
			}
			return lowerRelationIR(expr.Left)
		}
		if err := irFields(expr, "Left", "Right", "Modifiers", "Constraints"); err != nil {
			return sqlir.Relation{}, err
		}
		left, err := lowerRelationIR(expr.Left)
		if err != nil {
			return sqlir.Relation{}, err
		}
		relation := sqlir.Relation{Kind: "join", Left: &left, Modifiers: slices.Clone(expr.Modifiers)}
		if expr.Right != nil {
			right, err := lowerRelationIR(expr.Right)
			if err != nil {
				return sqlir.Relation{}, err
			}
			relation.Right = &right
		}
		if expr.Constraints != nil {
			relation.On, relation.Using, err = lowerJoinConstraintIR(expr.Constraints)
			if err != nil {
				return sqlir.Relation{}, err
			}
		}
		return relation, nil
	case *clickhouse.SubQuery:
		if err := irFields(expr, "Select", "HasParen"); err != nil {
			return sqlir.Relation{}, err
		}
		document, err := lowerSelectIR(expr.Select)
		if err != nil {
			return sqlir.Relation{}, err
		}
		return sqlir.Relation{Kind: "derived", Query: &document.Select, Parenthesized: expr.HasParen}, nil
	case *clickhouse.JoinTableExpr:
		if err := irFields(expr, "Table", "HasFinal"); err != nil {
			return sqlir.Relation{}, err
		}
		relation, err := lowerRelationIR(expr.Table)
		relation.Final = relation.Final || expr.HasFinal
		return relation, err
	case *clickhouse.TableExpr:
		if err := irFields(expr, "Expr", "Alias", "HasFinal"); err != nil {
			return sqlir.Relation{}, err
		}
		relation, err := lowerRelationIR(expr.Expr)
		if err != nil {
			return sqlir.Relation{}, err
		}
		relation.Final = relation.Final || expr.HasFinal
		if expr.Alias != nil {
			if err := irFields(expr.Alias, "Alias"); err != nil {
				return sqlir.Relation{}, err
			}
			alias, ok := expr.Alias.Alias.(*clickhouse.Ident)
			if !ok {
				return sqlir.Relation{}, fmt.Errorf("IR adapter requires a simple relation alias")
			}
			relation.Alias = alias.Name
		}
		return relation, nil
	case *clickhouse.TableFunctionExpr:
		if err := irFields(expr, "Name", "Args"); err != nil {
			return sqlir.Relation{}, err
		}
		name, ok := expr.Name.(*clickhouse.Ident)
		if !ok {
			return sqlir.Relation{}, fmt.Errorf("IR adapter requires a simple table function name")
		}
		if err := irFields(expr.Args, "Args"); err != nil {
			return sqlir.Relation{}, err
		}
		call := sqlir.Expr{Kind: "call", Value: name.Name}
		for _, arg := range expr.Args.Args {
			value, err := lowerExprIR(arg)
			if err != nil {
				return sqlir.Relation{}, err
			}
			call.Args = append(call.Args, value)
		}
		return sqlir.Relation{Kind: "function", Call: &call}, nil
	default:
		return sqlir.Relation{}, fmt.Errorf("IR adapter does not model relation %s", clickhouse.Format(expression))
	}
}

func lowerJoinConstraintIR(expression clickhouse.Expr) ([]sqlir.Expr, []sqlir.Expr, error) {
	var on, using *clickhouse.ColumnExprList
	var err error
	switch constraint := expression.(type) {
	case *clickhouse.OnClause:
		err = irFields(constraint, "On")
		on = constraint.On
	case *clickhouse.UsingClause:
		err = irFields(constraint, "Using")
		using = constraint.Using
	case *clickhouse.JoinConstraintClause:
		err = irFields(constraint, "On", "Using")
		on, using = constraint.On, constraint.Using
	default:
		return nil, nil, fmt.Errorf("IR adapter does not model JOIN constraint %T", expression)
	}
	if err != nil {
		return nil, nil, err
	}
	onExpressions, err := lowerExprListIR(on)
	if err != nil {
		return nil, nil, err
	}
	usingExpressions, err := lowerExprListIR(using)
	return onExpressions, usingExpressions, err
}

func irSourceSpan(expression clickhouse.Expr) *sqlir.Span {
	start, end := int(expression.Pos()), int(expression.End())
	// The pinned parser reports an empty range for a bare asterisk.
	if identifier, ok := expression.(*clickhouse.Ident); ok && identifier.Name == "*" && end == start {
		end++
	}
	if qualified, ok := expression.(*clickhouse.NestedIdentifier); ok && qualified.DotIdent != nil && qualified.DotIdent.Name == "*" && qualified.DotIdent.End() == qualified.DotIdent.Pos() {
		end++
	}
	if start < 0 || end <= start {
		return nil
	}
	return &sqlir.Span{Start: start, End: end}
}
