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
	if stripLimitWithTiesModifiers(sql) != sql {
		return nil, fmt.Errorf("IR adapter does not model LIMIT WITH TIES")
	}
	return lowerSelectIR(query)
}

func lowerSelectIR(query *clickhouse.SelectQuery) (*sqlir.Document, error) {
	if err := irFields(query, "With", "SelectItems", "From", "Where", "GroupBy", "Having", "OrderBy", "Limit"); err != nil {
		return nil, err
	}
	result := sqlir.Select{}
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
				return nil, fmt.Errorf("IR adapter requires a relation CTE")
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
			if err := irFields(order, "Expr", "Direction"); err != nil {
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
			result.OrderBy = append(result.OrderBy, sqlir.Order{Expr: expression, Direction: direction})
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
		if err := irFields(expr.Params, "Items"); err != nil {
			return sqlir.Expr{}, err
		}
		args, err := lowerExprListIR(expr.Params.Items)
		return sqlir.Expr{Kind: "call", Value: expr.Name.Name, Args: args}, err
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
		return sqlir.Expr{Kind: "identifier", Name: []string{expr.Name}}, nil
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
		if expr.Base != 10 {
			return sqlir.Expr{}, fmt.Errorf("IR adapter requires decimal number literals")
		}
		return sqlir.Expr{Kind: "number", Value: expr.Literal}, nil
	case *clickhouse.BinaryOperation:
		if err := irFields(expr, "LeftExpr", "Operation", "RightExpr"); err != nil {
			return sqlir.Expr{}, err
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
		if err := irFields(expr, "Table"); err != nil {
			return sqlir.Relation{}, err
		}
		return lowerRelationIR(expr.Table)
	case *clickhouse.TableExpr:
		if err := irFields(expr, "Expr", "Alias"); err != nil {
			return sqlir.Relation{}, err
		}
		relation, err := lowerRelationIR(expr.Expr)
		if err != nil {
			return sqlir.Relation{}, err
		}
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
