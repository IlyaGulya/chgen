package sqlir

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// ErrBindingUnmodeled is an explicit domain boundary, not invalid SQL.
var ErrBindingUnmodeled = errors.New("IR binding does not model this query")

type Column struct{ Name, Type string }
type Table struct {
	Name, Alias string
	Columns     []Column
}

type Reference struct {
	Table  string `json:"table"`
	Column string `json:"column"`
	Type   string `json:"type"`
	Span   *Span  `json:"span,omitempty"`
}

type BindingReport struct {
	Backend    string      `json:"backend"`
	References []Reference `json:"references"`
}

// BoundScope contains catalog identities and types, never parser AST nodes.
type BoundScope struct {
	table   Table
	columns map[string]string
	aliases map[string]Expr
	report  BindingReport
}

type BindingError struct {
	Name      string
	Qualifier string
	Span      *Span
	Message   string
	Code      string
}

func (e *BindingError) Error() string { return e.Message }

// BindingDomain limits the first production slice to one relation without
// CTE scope rules, grouping or correlation.
func BindingDomain(document *Document) error {
	query := document.Select
	if len(query.With) != 0 || len(query.From) != 1 || len(query.GroupBy) != 0 || query.Having != nil {
		return ErrBindingUnmodeled
	}
	if query.From[0].Kind != "table" && query.From[0].Kind != "function" {
		return ErrBindingUnmodeled
	}
	for _, item := range query.Items {
		if item.Alias != "" && item.Expr.Kind == "wildcard" {
			return ErrBindingUnmodeled
		}
		if err := bindingExprDomain(item.Expr); err != nil {
			return err
		}
	}
	for _, expression := range []*Expr{query.Where, query.Limit, query.Offset} {
		if expression != nil {
			if err := bindingExprDomain(*expression); err != nil {
				return err
			}
		}
	}
	for _, order := range query.OrderBy {
		if err := bindingExprDomain(order.Expr); err != nil {
			return err
		}
	}
	return nil
}

func bindingExprDomain(expression Expr) error {
	// A lambda introduces lexical names, not table column references.
	if expression.Kind == "operator" && expression.Value == "->" {
		return ErrBindingUnmodeled
	}
	switch expression.Kind {
	case "identifier":
		if len(expression.Name) < 1 || len(expression.Name) > 2 {
			return ErrBindingUnmodeled
		}
		// Literal versus quoted-name precedence still belongs to the legacy binder.
		if len(expression.Name) == 1 && slices.Contains([]string{"null", "true", "false"}, strings.ToLower(expression.Name[0])) {
			return ErrBindingUnmodeled
		}
	case "number", "wildcard", "operator", "call", "tuple":
	default:
		return ErrBindingUnmodeled
	}
	for _, argument := range expression.Args {
		if err := bindingExprDomain(argument); err != nil {
			return err
		}
	}
	return nil
}

func Bind(document *Document, table Table) (*BoundScope, error) {
	if err := BindingDomain(document); err != nil {
		return nil, err
	}
	scope := &BoundScope{table: table, columns: make(map[string]string), aliases: make(map[string]Expr), report: BindingReport{Backend: "sqlir", References: []Reference{}}}
	for _, column := range table.Columns {
		scope.columns[column.Name] = column.Type
	}
	query := document.Select
	// Name collisions and duplicate aliases retain legacy precedence rules.
	for _, item := range query.Items {
		if item.Alias == "" {
			continue
		}
		_, collision := scope.columns[item.Alias]
		_, duplicate := scope.aliases[item.Alias]
		if collision || duplicate {
			return nil, ErrBindingUnmodeled
		}
		scope.aliases[item.Alias] = item.Expr
	}
	for _, item := range query.Items {
		if err := scope.bindClause(item.Expr, "selected expression"); err != nil {
			return nil, err
		}
	}
	for index, expression := range []*Expr{query.Where, query.Limit, query.Offset} {
		if expression != nil {
			if err := scope.bindClause(*expression, []string{"WHERE condition", "LIMIT expression", "LIMIT OFFSET expression"}[index]); err != nil {
				return nil, err
			}
		}
	}
	for _, order := range query.OrderBy {
		if err := scope.bindClause(order.Expr, "ORDER BY expression"); err != nil {
			return nil, err
		}
	}
	return scope, nil
}

func (scope *BoundScope) bindClause(expression Expr, clause string) error {
	if err := scope.bindExpr(expression, make(map[string]bool), nil); err != nil {
		return fmt.Errorf("%s: %w", clause, err)
	}
	return nil
}

func (scope *BoundScope) bindExpr(expression Expr, active map[string]bool, useSpan *Span) error {
	if expression.Kind == "identifier" {
		qualifier, name := "", expression.Name[0]
		if len(expression.Name) == 2 {
			qualifier, name = expression.Name[0], expression.Name[1]
		}
		if qualifier == "" {
			if source, alias := scope.aliases[name]; alias {
				if active[name] {
					return &BindingError{Name: name, Span: expression.Span, Code: "ir-alias-cycle", Message: fmt.Sprintf("cyclic projection alias %q", name)}
				}
				active[name] = true
				if useSpan == nil {
					useSpan = expression.Span
				}
				err := scope.bindExpr(source, active, useSpan)
				delete(active, name)
				return err
			}
		}
		typ, err := scope.Lookup(qualifier, name)
		if err != nil {
			return &BindingError{Name: name, Qualifier: qualifier, Span: expression.Span, Message: err.Error()}
		}
		span := expression.Span
		if useSpan != nil {
			span = useSpan
		}
		scope.report.References = append(scope.report.References, Reference{Table: scope.table.Name, Column: name, Type: typ, Span: span})
	}
	if expression.Kind == "wildcard" && len(expression.Name) > 0 && expression.Name[0] != scope.table.Alias && expression.Name[0] != scope.table.Name {
		return &BindingError{Name: expression.Name[0], Span: expression.Span, Message: fmt.Sprintf("wildcard qualifier %q is not a FROM source", expression.Name[0])}
	}
	for _, argument := range expression.Args {
		if err := scope.bindExpr(argument, active, useSpan); err != nil {
			return err
		}
	}
	return nil
}

func (scope *BoundScope) Lookup(qualifier, name string) (string, error) {
	if qualifier != "" && qualifier != scope.table.Alias && qualifier != scope.table.Name {
		return "", fmt.Errorf("column %q is not present in FROM tables", name)
	}
	typ, found := scope.columns[name]
	if !found {
		return "", fmt.Errorf("column %q is not present in FROM tables", name)
	}
	return typ, nil
}

func (scope *BoundScope) Report() BindingReport { return scope.report }
