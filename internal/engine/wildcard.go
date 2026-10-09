package engine

import (
	"fmt"
	"strings"

	clickhouse "github.com/AfterShip/clickhouse-sql-parser/parser"
)

// QueryWildcardState preserves catalog-resolved projection state across the
// public facade without exposing it as a client assertion.
func QueryWildcardState(query Query) string          { return query.wildcardSQL }
func SetQueryWildcardState(query *Query, sql string) { query.wildcardSQL = sql }

func validateWildcardResolution(query Query) error {
	if query.Command == CommandExec || query.serverVersion != "" {
		return nil
	}
	statements, err := parseChgenStatements(query.SQL, query.Command)
	if err != nil {
		return err
	}
	if len(statements) != 1 {
		return nil
	}
	selectQuery, ok := statements[0].(*clickhouse.SelectQuery)
	if !ok {
		return nil
	}
	for _, item := range selectQuery.SelectItems {
		if _, star := selectWildcardQualifier(item.Expr); star && query.wildcardSQL == "" {
			return fmt.Errorf("wildcard results require catalog resolution; parse the query before generation")
		}
	}
	return nil
}

// Expand only the resolver's AST. The source SQL and runtime query stay intact.
// One source avoids guessing JOIN wildcard ordering and duplicate names.
func expandSelectWildcards(query *clickhouse.SelectQuery, scope queryScope) error {
	var items []*clickhouse.SelectItem
	for _, item := range query.SelectItems {
		qualifier, star := selectWildcardQualifier(item.Expr)
		if !star {
			items = append(items, item)
			continue
		}
		if item.Alias != nil || len(item.Modifiers) != 0 {
			return fmt.Errorf("wildcard aliases and modifiers are not supported")
		}
		if len(scope.tables) != 1 || len(scope.usingTypes) != 0 || len(scope.arrayJoinTypes) != 0 {
			return fmt.Errorf("wildcard expansion requires exactly one FROM source without JOIN or ARRAY JOIN")
		}
		table := scope.tables[0].table
		if qualifier != "" && qualifier != scope.tables[0].alias {
			return fmt.Errorf("wildcard qualifier %q is not a FROM source", qualifier)
		}
		for _, name := range table.ColumnOrder {
			column := table.Columns[name]
			if column.MaterializedExpr != "" && !scope.wildcardMaterialized || column.AliasExpr != "" && !scope.wildcardAlias {
				continue
			}
			items = append(items, &clickhouse.SelectItem{Expr: &clickhouse.Ident{Name: name, QuoteType: clickhouse.BackTicks, NamePos: item.Expr.Pos(), NameEnd: item.Expr.End()}})
		}
	}
	query.SelectItems = items
	return nil
}

func configureWildcardSettings(query *clickhouse.SelectQuery, parent *queryScope, scope *queryScope) error {
	if parent != nil {
		scope.wildcardAlias = parent.wildcardAlias
		scope.wildcardMaterialized = parent.wildcardMaterialized
	}
	if query.Settings == nil {
		return nil
	}
	for _, item := range query.Settings.Items {
		if item == nil || item.Name == nil {
			continue
		}
		name := item.Name.Name
		if name != "asterisk_include_alias_columns" && name != "asterisk_include_materialized_columns" {
			continue
		}
		if err := validateSettingsClauseWithRoster(&clickhouse.SettingsClause{Items: []*clickhouse.SettingExpr{item}}, selectSettingRoster); err != nil {
			return err
		}
		enabled := clickhouse.Format(item.Expr) == "1" || strings.EqualFold(clickhouse.Format(item.Expr), "true")
		if name == "asterisk_include_alias_columns" {
			scope.wildcardAlias = enabled
		} else {
			scope.wildcardMaterialized = enabled
		}
	}
	return nil
}

func selectWildcardQualifier(expression clickhouse.Expr) (string, bool) {
	switch expr := unwrapColumnExpression(expression).(type) {
	case *clickhouse.Ident:
		return "", expr.Name == "*" && (expr.QuoteType == 0 || expr.QuoteType == clickhouse.Unquoted)
	case *clickhouse.NestedIdentifier:
		if expr.DotIdent != nil {
			_, star := selectWildcardQualifier(expr.DotIdent)
			return expr.Ident.Name, star
		}
	case *clickhouse.Path:
		if len(expr.Fields) == 2 {
			_, star := selectWildcardQualifier(expr.Fields[1])
			return expr.Fields[0].Name, star
		}
	}
	return "", false
}
