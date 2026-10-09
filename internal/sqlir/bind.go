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
	Kind   string `json:"kind,omitempty"`
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
	tables  []Table
	columns map[string]string
	merged  map[string]string
	scalars map[string]string
	parent  *BoundScope
	aliases map[string]Expr
	report  BindingReport
}

// ScopeContext is a typed lexical input, independent of parser AST nodes.
// Scalar signatures distinguish outer expressions from catalog columns.
type ScopeContext struct {
	Tables  []Table
	Merged  map[string]string
	Scalars map[string]string
	Parent  *ScopeContext
}

type BindingError struct {
	Name      string
	Qualifier string
	Span      *Span
	Message   string
	Code      string
}

func (e *BindingError) Error() string { return e.Message }

// BindingDomain leaves unmodeled expression scopes on the legacy
// path. Eligible relation trees are validated recursively, not partly erased.
func BindingDomain(document *Document) error {
	query := document.Select
	if len(query.From) != 1 {
		return ErrBindingUnmodeled
	}
	for _, cte := range query.With {
		if err := BindingDomain(&Document{Select: cte.Query}); err != nil {
			return err
		}
	}
	if err := bindingRelationDomain(query.From[0]); err != nil {
		return err
	}
	for _, item := range query.Items {
		if item.Alias != "" && item.Expr.Kind == "wildcard" {
			return ErrBindingUnmodeled
		}
		if err := bindingExprDomain(item.Expr); err != nil {
			return err
		}
	}
	for _, expression := range query.GroupBy {
		if err := bindingExprDomain(expression); err != nil {
			return err
		}
	}
	for _, expression := range []*Expr{query.Where, query.Having, query.Limit, query.Offset} {
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

func bindingRelationDomain(relation Relation) error {
	switch relation.Kind {
	case "table", "function":
		return nil
	case "derived":
		if relation.Query == nil {
			return ErrBindingUnmodeled
		}
		return BindingDomain(&Document{Select: *relation.Query})
	case "join":
		if relation.Left == nil {
			return ErrBindingUnmodeled
		}
		if err := bindingRelationDomain(*relation.Left); err != nil {
			return err
		}
		if relation.Right != nil {
			if err := bindingRelationDomain(*relation.Right); err != nil {
				return err
			}
		}
		for _, expression := range append(slices.Clone(relation.On), relation.Using...) {
			if err := bindingExprDomain(expression); err != nil {
				return err
			}
		}
		return nil
	default:
		return ErrBindingUnmodeled
	}
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
	return BindSources(document, []Table{table}, nil)
}

// BindSources consumes typed source signatures. Merged JOIN column types are
// supplied by the engine; binding does not infer nullability or common types.
func BindSources(document *Document, tables []Table, merged map[string]string) (*BoundScope, error) {
	return BindContext(document, &ScopeContext{Tables: tables, Merged: merged})
}

func contextScope(context *ScopeContext) *BoundScope {
	if context == nil {
		return nil
	}
	scope := &BoundScope{tables: context.Tables, merged: context.Merged, scalars: context.Scalars, columns: make(map[string]string), aliases: make(map[string]Expr), parent: contextScope(context.Parent), report: BindingReport{Backend: "sqlir", References: []Reference{}}}
	for _, table := range context.Tables {
		for _, column := range table.Columns {
			scope.columns[column.Name] = column.Type
		}
	}
	return scope
}

func BindContext(document *Document, context *ScopeContext) (*BoundScope, error) {
	if err := BindingDomain(document); err != nil {
		return nil, err
	}
	scope := contextScope(context)
	query := document.Select
	// Duplicate projections are still checked by the existing result validator.
	for _, item := range query.Items {
		if item.Alias == "" {
			continue
		}
		_, duplicate := scope.aliases[item.Alias]
		if duplicate {
			return nil, ErrBindingUnmodeled
		}
		scope.aliases[item.Alias] = item.Expr
	}
	for _, item := range query.Items {
		if err := scope.bindClause(item.Expr, "selected expression"); err != nil {
			return nil, err
		}
	}
	for _, expression := range query.GroupBy {
		if err := scope.bindClause(expression, "GROUP BY expression"); err != nil {
			return nil, err
		}
	}
	for index, expression := range []*Expr{query.Where, query.Having, query.Limit, query.Offset} {
		if expression != nil {
			if err := scope.bindClause(*expression, []string{"WHERE condition", "HAVING condition", "LIMIT expression", "LIMIT OFFSET expression"}[index]); err != nil {
				return nil, err
			}
		}
	}
	for _, order := range query.OrderBy {
		if err := scope.bindClause(order.Expr, "ORDER BY expression"); err != nil {
			return nil, err
		}
	}
	if _, err := scope.bindJoin(query.From[0], 0); err != nil {
		return nil, err
	}
	return scope, nil
}

func (scope *BoundScope) bindJoin(relation Relation, seen int) (int, error) {
	if relation.Kind != "join" {
		return seen + 1, nil
	}
	seen, err := scope.bindJoin(*relation.Left, seen)
	if err != nil {
		return seen, err
	}
	local := *scope
	local.tables = scope.tables[:seen]
	for _, expression := range relation.On {
		if err := local.bindClause(expression, "JOIN ON condition"); err != nil {
			return seen, err
		}
	}
	for _, expression := range relation.Using {
		if err := local.bindClause(expression, "JOIN USING column"); err != nil {
			return seen, err
		}
	}
	scope.report = local.report
	if relation.Right != nil {
		return scope.bindJoin(*relation.Right, seen)
	}
	return seen, nil
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
			if source, alias := scope.aliases[name]; alias && !(active[name] && scope.columns[name] != "") {
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
		table, typ, err := scope.resolveColumn(qualifier, name)
		if err != nil {
			return &BindingError{Name: name, Qualifier: qualifier, Span: expression.Span, Message: err.Error()}
		}
		span := expression.Span
		if useSpan != nil {
			span = useSpan
		}
		reference := Reference{Table: table, Column: name, Type: typ, Span: span}
		if table == "" {
			reference.Kind = "scalar"
		}
		scope.report.References = append(scope.report.References, reference)
	}
	if expression.Kind == "wildcard" && len(expression.Name) > 0 {
		known := slices.ContainsFunc(scope.tables, func(table Table) bool { return expression.Name[0] == table.Alias || expression.Name[0] == table.Name })
		if !known {
			return &BindingError{Name: expression.Name[0], Span: expression.Span, Message: fmt.Sprintf("wildcard qualifier %q is not a FROM source", expression.Name[0])}
		}
	}
	for _, argument := range expression.Args {
		if err := scope.bindExpr(argument, active, useSpan); err != nil {
			return err
		}
	}
	return nil
}

func (scope *BoundScope) Lookup(qualifier, name string) (string, error) {
	_, typ, err := scope.resolveColumn(qualifier, name)
	return typ, err
}

func (scope *BoundScope) resolveColumn(qualifier, name string) (string, string, error) {
	if qualifier == "" {
		if scalar, found := scope.scalars[name]; found {
			return "", scalar, nil
		}
	}
	var source, typ string
	count := 0
	localQualifier := false
	for _, table := range scope.tables {
		if qualifier != "" && qualifier != table.Alias && qualifier != table.Name {
			continue
		}
		localQualifier = localQualifier || qualifier != ""
		for _, column := range table.Columns {
			if column.Name != name {
				continue
			}
			if count == 0 {
				source, typ = table.Name, column.Type
			}
			count++
		}
	}
	key := name
	if qualifier != "" {
		key = qualifier + "." + name
	}
	if merged, ok := scope.merged[key]; ok {
		return source, merged, nil
	}
	if count == 0 {
		if scope.parent != nil && !localQualifier {
			return scope.parent.resolveColumn(qualifier, name)
		}
		return "", "", fmt.Errorf("column %q is not present in FROM tables", name)
	}
	// The measured two-source rule preserves left-source precedence.
	if count > 1 && qualifier == "" && len(scope.tables) != 2 {
		return "", "", fmt.Errorf("column %q is ambiguous", name)
	}
	return source, typ, nil
}

func (scope *BoundScope) Report() BindingReport { return scope.report }
